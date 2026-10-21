package pngopt

import (
	"cmp"
	"errors"
	"image"
	"image/color"
	"math"
	"slices"
)

// QuantizeOptions configures Quantize.
type QuantizeOptions struct {
	// Colors is the maximum palette size, 2..256. Zero means 256.
	Colors int
	// Dither is the error-diffusion strength in [0, 1]. Zero means the
	// default 0.5; a negative value disables dithering.
	Dither float64
}

// Perceptual weights (square roots of the distance weights) applied to the
// premultiplied red, green, blue and alpha coordinates.
var qWeights = [4]float32{0.78, 1, 0.63, 1}

const (
	ditherMaxEdge = 160
	ditherSoftEdg = 32
	errClamp      = 36
	memoCap       = 1 << 14
	kmeansIters   = 4
)

type vec [4]float32

func premul(r, g, b, a byte) vec {
	af := float32(a) / 255
	return vec{float32(r) * af, float32(g) * af, float32(b) * af, float32(a)}
}

func weighted(v vec) vec {
	return vec{v[0] * qWeights[0], v[1] * qWeights[1], v[2] * qWeights[2], v[3] * qWeights[3]}
}

// cell is a histogram bin of similar colours.
type cell struct {
	n   float64
	sum [4]uint64
	v   vec
	w   vec
}

func cellKey(r, g, b, a byte) uint32 {
	var ak uint32
	switch a {
	case 255:
		ak = 16
	case 0:
		ak = 17
	default:
		ak = uint32(a >> 4)
	}
	return uint32(r>>3)<<15 | uint32(g>>3)<<10 | uint32(b>>3)<<5 | ak
}

func (c *cell) finish() {
	if c.n == 0 {
		return
	}
	d := 255 * c.n
	c.v = vec{float32(float64(c.sum[0]) / d), float32(float64(c.sum[1]) / d), float32(float64(c.sum[2]) / d), float32(float64(c.sum[3]) / d)}
	c.w = weighted(c.v)
}

// Alpha classes never share a cluster, so opaque pixels stay opaque and
// fully transparent ones stay transparent.
const (
	clsOpaque = iota
	clsSemi
	clsClear
)

func alphaClass(a float32) int {
	switch {
	case a >= 255:
		return clsOpaque
	case a <= 0:
		return clsClear
	}
	return clsSemi
}

func byteClass(a byte) int { return alphaClass(float32(a)) }

type box struct {
	lo, hi int
	sse    float64
	axis   int
}

func evalBox(cells []cell, idx []int32, lo, hi int) box {
	var mean [4]float64
	var tot float64
	for _, i := range idx[lo:hi] {
		c := &cells[i]
		tot += c.n
		for k := range 4 {
			mean[k] += c.n * float64(c.w[k])
		}
	}
	var varc [4]float64
	for _, i := range idx[lo:hi] {
		c := &cells[i]
		for k := range 4 {
			d := float64(c.w[k]) - mean[k]/tot
			varc[k] += c.n * d * d
		}
	}
	b := box{lo: lo, hi: hi}
	for k := range 4 {
		b.sse += varc[k]
		if varc[k] > varc[b.axis] {
			b.axis = k
		}
	}
	return b
}

// splitClasses reorders idx so that opaque cells come before semi-transparent
// ones and returns the boundary.
func splitClasses(cells []cell, idx []int32) int {
	slices.SortStableFunc(idx, func(a, b int32) int {
		return cmp.Compare(alphaClass(cells[a].v[3]), alphaClass(cells[b].v[3]))
	})
	n := 0
	for n < len(idx) && alphaClass(cells[idx[n]].v[3]) == clsOpaque {
		n++
	}
	return n
}

func medianCut(cells []cell, idx []int32, target int) []box {
	var boxes []box
	if n := splitClasses(cells, idx); n > 0 && n < len(idx) && target >= 2 {
		boxes = []box{evalBox(cells, idx, 0, n), evalBox(cells, idx, n, len(idx))}
	} else {
		boxes = []box{evalBox(cells, idx, 0, len(idx))}
	}
	for len(boxes) < target {
		bi := -1
		for i, b := range boxes {
			if b.hi-b.lo >= 2 && b.sse > 0 && (bi < 0 || b.sse > boxes[bi].sse) {
				bi = i
			}
		}
		if bi < 0 {
			break
		}
		b := boxes[bi]
		seg := idx[b.lo:b.hi]
		slices.SortFunc(seg, func(x, y int32) int {
			if c := cmp.Compare(cells[x].w[b.axis], cells[y].w[b.axis]); c != 0 {
				return c
			}
			return cmp.Compare(x, y)
		})
		var tot float64
		for _, i := range seg {
			tot += cells[i].n
		}
		mid, cum := b.lo, 0.0
		for p, i := range seg {
			cum += cells[i].n
			if cum >= tot/2 {
				mid = b.lo + p + 1
				break
			}
		}
		mid = min(max(mid, b.lo+1), b.hi-1)
		boxes[bi] = evalBox(cells, idx, b.lo, mid)
		boxes = append(boxes, evalBox(cells, idx, mid, b.hi))
	}
	return boxes
}

// nearest returns the index of the palette entry closest to v among the
// entries of class cls (or among all when cls is negative).
func nearest(pw []vec, cl []uint8, v vec, cls int) int {
	best, bi := float32(math.MaxFloat32), 0
	for i := range pw {
		if cls >= 0 && int(cl[i]) != cls {
			continue
		}
		p := &pw[i]
		d := v[1] - p[1]
		d *= d
		if d >= best {
			continue
		}
		e := v[0] - p[0]
		d += e * e
		if d >= best {
			continue
		}
		e = v[3] - p[3]
		d += e * e
		if d >= best {
			continue
		}
		e = v[2] - p[2]
		d += e * e
		if d < best {
			best, bi = d, i
		}
	}
	if best == math.MaxFloat32 && cls >= 0 {
		return nearest(pw, cl, v, -1)
	}
	return bi
}

func toNRGBA(v vec) color.NRGBA {
	a := int(math.Round(float64(v[3])))
	a = min(max(a, 0), 255)
	if alphaClass(v[3]) == clsSemi {
		a = min(max(a, 1), 254)
	}
	if a == 0 {
		return color.NRGBA{}
	}
	f := 255 / float64(a)
	ch := func(x float32) uint8 {
		return uint8(min(max(math.Round(float64(x)*f), 0), 255))
	}
	return color.NRGBA{ch(v[0]), ch(v[1]), ch(v[2]), uint8(a)}
}

// refine runs a few weighted k-means iterations over the histogram cells.
func refine(cells []cell, idx []int32, cent []vec, cl []uint8) {
	assign := make([]int, len(idx))
	for i := range assign {
		assign[i] = -1
	}
	pw := make([]vec, len(cent))
	for range kmeansIters {
		for i, c := range cent {
			pw[i] = weighted(c)
		}
		changed := false
		sums := make([][4]float64, len(cent))
		ns := make([]float64, len(cent))
		for j, ci := range idx {
			c := &cells[ci]
			k := nearest(pw, cl, c.w, alphaClass(c.v[3]))
			if k != assign[j] {
				assign[j], changed = k, true
			}
			ns[k] += c.n
			for d := range 4 {
				sums[k][d] += c.n * float64(c.v[d])
			}
		}
		if !changed {
			return
		}
		for k := range cent {
			if ns[k] > 0 {
				for d := range 4 {
					cent[k][d] = float32(sums[k][d] / ns[k])
				}
			}
		}
	}
}

// Quantize reduces img to at most opts.Colors palette entries with alpha
// support. It is lossy unless the image already has that few colours, in
// which case the palette is exact and no dithering happens.
//
// Colours are clustered by median cut on a histogram, refined with k-means in
// a premultiplied, luma-weighted space, and mapped with serpentine
// Floyd-Steinberg error diffusion that is disabled on flat areas and
// attenuated towards strong edges. The result is deterministic.
func Quantize(img image.Image, opts QuantizeOptions) (*image.Paletted, error) {
	if opts.Colors == 0 {
		opts.Colors = 256
	}
	if opts.Colors < 2 || opts.Colors > 256 {
		return nil, errors.New("pngopt: Colors must be in 2..256")
	}
	strength := opts.Dither
	switch {
	case strength == 0:
		strength = 0.5
	case strength < 0:
		strength = 0
	}
	strength = min(strength, 1)
	r0, err := fromImage(img)
	if err != nil {
		return nil, err
	}
	r := to8(r0)
	w, h := r.w, r.h
	rect := image.Rect(0, 0, w, h)

	if an := analyze8(r); an.palOK && an.tab.n <= opts.Colors {
		pal := make(color.Palette, an.tab.n)
		for i := range pal {
			c := unpackKey(an.tab.list[i])
			pal[i] = color.NRGBA{c[0], c[1], c[2], c[3]}
		}
		out := image.NewPaletted(rect, pal)
		for i := range w * h {
			p := r.pix[4*i : 4*i+4]
			out.Pix[i] = uint8(an.tab.find(packKey(p[0], p[1], p[2], p[3])))
		}
		return out, nil
	}

	cells, transparent := histogram(r)
	target := opts.Colors
	var idx []int32
	for i := range cells {
		cells[i].finish()
		if i == transparent {
			continue
		}
		idx = append(idx, int32(i))
	}
	if transparent >= 0 {
		target--
	}
	var cent []vec
	if len(idx) <= target {
		for _, i := range idx {
			cent = append(cent, cells[i].v)
		}
	} else {
		boxes := medianCut(cells, idx, target)
		cent = boxCentroids(cells, idx, boxes)
		cl := make([]uint8, len(cent))
		for i, c := range cent {
			cl[i] = uint8(alphaClass(c[3]))
		}
		refine(cells, idx, cent, cl)
	}
	if transparent >= 0 {
		cent = append(cent, vec{})
	}
	pal := make(color.Palette, len(cent))
	pu := make([]vec, len(cent))
	pw := make([]vec, len(cent))
	cl := make([]uint8, len(cent))
	for i, c := range cent {
		n := toNRGBA(c)
		pal[i] = n
		pu[i] = premul(n.R, n.G, n.B, n.A)
		pw[i] = weighted(pu[i])
		cl[i] = uint8(byteClass(n.A))
	}
	out := image.NewPaletted(rect, pal)
	mapPixels(r, out, pu, pw, cl, float32(strength))
	return out, nil
}

func histogram(r *raster) (cells []cell, transparent int) {
	transparent = -1
	index := make(map[uint32]int32, 1<<14)
	lastKey, last := uint32(math.MaxUint32), int32(0)
	for i := 0; i+3 < len(r.pix); i += 4 {
		p := r.pix[i : i+4]
		k := cellKey(p[0], p[1], p[2], p[3])
		if k != lastKey {
			ci, ok := index[k]
			if !ok {
				ci = int32(len(cells))
				index[k] = ci
				cells = append(cells, cell{})
				if p[3] == 0 {
					transparent = int(ci)
				}
			}
			lastKey, last = k, ci
		}
		c := &cells[last]
		a := uint64(p[3])
		c.n++
		c.sum[0] += uint64(p[0]) * a
		c.sum[1] += uint64(p[1]) * a
		c.sum[2] += uint64(p[2]) * a
		c.sum[3] += a * 255
	}
	return cells, transparent
}

func boxCentroids(cells []cell, idx []int32, boxes []box) []vec {
	cent := make([]vec, len(boxes))
	for bi, b := range boxes {
		var s [4]float64
		var n float64
		for _, i := range idx[b.lo:b.hi] {
			c := &cells[i]
			n += c.n
			for d := range 4 {
				s[d] += c.n * float64(c.v[d])
			}
		}
		for d := range 4 {
			cent[bi][d] = float32(s[d] / n)
		}
	}
	return cent
}

type mapper struct {
	pw     []vec
	pu     []vec
	cl     []uint8
	opaque []uint16
	semi   map[uint32]uint8
	memo   map[uint32]uint8
}

// lookup returns the palette index for v, a pixel of alpha class cls,
// through the quantized-colour cache.
func (m *mapper) lookup(v vec, cls int) int {
	q := [4]int{}
	for i := range 4 {
		q[i] = min(max(int(v[i]/4), 0), 63)
	}
	centre := vec{float32(q[0]*4 + 2), float32(q[1]*4 + 2), float32(q[2]*4 + 2), float32(q[3]*4 + 2)}
	if cls == clsOpaque {
		centre[3] = 255
		k := q[0]<<12 | q[1]<<6 | q[2]
		if c := m.opaque[k]; c != 0 {
			return int(c) - 1
		}
		i := nearest(m.pw, m.cl, weighted(centre), cls)
		m.opaque[k] = uint16(i + 1)
		return i
	}
	k := uint32(q[0]<<18 | q[1]<<12 | q[2]<<6 | q[3])
	if cls == clsClear {
		k = 1 << 30
	}
	if c, ok := m.semi[k]; ok {
		return int(c)
	}
	if cls == clsClear {
		centre = vec{}
	}
	i := nearest(m.pw, m.cl, weighted(centre), cls)
	m.semi[k] = uint8(i)
	return i
}

func edgeStrength(pix []byte, w, h, x, y int) int {
	o := (y*w + x) * 4
	diff := func(o2 int) int {
		d := 0
		for k := range 4 {
			v := int(pix[o+k]) - int(pix[o2+k])
			d += max(v, -v)
		}
		return d
	}
	e := 0
	if x > 0 {
		e = max(e, diff(o-4))
	}
	if x < w-1 {
		e = max(e, diff(o+4))
	}
	if y > 0 {
		e = max(e, diff(o-4*w))
	}
	if y < h-1 {
		e = max(e, diff(o+4*w))
	}
	return e
}

func mapPixels(r *raster, out *image.Paletted, pu, pw []vec, cl []uint8, strength float32) {
	w, h := r.w, r.h
	m := &mapper{pw: pw, pu: pu, cl: cl, opaque: make([]uint16, 1<<18), semi: map[uint32]uint8{}, memo: map[uint32]uint8{}}
	cur := make([]vec, w+2)
	next := make([]vec, w+2)
	for y := range h {
		x, dx := 0, 1
		if y&1 == 1 {
			x, dx = w-1, -1
		}
		for range w {
			o := (y*w + x) * 4
			p := r.pix[o : o+4]
			v := premul(p[0], p[1], p[2], p[3])
			cls := byteClass(p[3])
			s := float32(0)
			if strength > 0 {
				if e := edgeStrength(r.pix, w, h, x, y); e > 0 && e < ditherMaxEdge {
					s = strength * min(1, float32(ditherMaxEdge-e)/float32(ditherMaxEdge-ditherSoftEdg))
				}
			}
			var idx int
			if s > 0 {
				ev := cur[x+1]
				for k := range 4 {
					v[k] = min(max(v[k]+ev[k], 0), 255)
				}
				v[0] = min(v[0], v[3])
				v[1] = min(v[1], v[3])
				v[2] = min(v[2], v[3])
				idx = m.lookup(v, cls)
				pe := pu[idx]
				var e vec
				for k := range 4 {
					e[k] = min(max((v[k]-pe[k])*s, -errClamp), errClamp)
				}
				for k := range 4 {
					cur[x+1+dx][k] += e[k] * 7 / 16
					next[x+1-dx][k] += e[k] * 3 / 16
					next[x+1][k] += e[k] * 5 / 16
					next[x+1+dx][k] += e[k] / 16
				}
			} else {
				key := packKey(p[0], p[1], p[2], p[3])
				if c, ok := m.memo[key]; ok {
					idx = int(c)
				} else if len(m.memo) < memoCap {
					idx = nearest(pw, cl, weighted(v), cls)
					m.memo[key] = uint8(idx)
				} else {
					idx = m.lookup(v, cls)
				}
			}
			out.Pix[y*w+x] = uint8(idx)
			x += dx
		}
		cur, next = next, cur
		clear(next)
	}
}
