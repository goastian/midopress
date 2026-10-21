package pngopt

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"hash/crc32"
	"slices"
	"sync"

	"github.com/klauspost/compress/zlib"
)

// meta carries what has to survive from the input file to the output.
type meta struct {
	color, late []chunk
	lockGray    bool
	lockRGB     bool
}

func metaFrom(p *parsed) *meta {
	m := &meta{color: p.color, late: p.late}
	if p.hasICCP {
		// An ICC profile is tied to a colour model, so the model is kept.
		if p.hdr.ctype == 0 || p.hdr.ctype == 4 {
			m.lockGray = true
		} else {
			m.lockRGB = true
		}
	}
	return m
}

// rep is a target pixel representation.
type rep struct {
	ctype, depth byte
	pal          [][4]byte
	tab          *colorTable
}

func (rp *rep) header() header { return header{ctype: rp.ctype, depth: rp.depth} }

func (rp *rep) overhead() int {
	if rp.ctype != 3 {
		return 0
	}
	return 12 + 3*len(rp.pal) + len(rp.trns())
}

// trns returns the tRNS payload of a palette representation.
func (rp *rep) trns() []byte {
	last := -1
	for i, c := range rp.pal {
		if c[3] != 255 {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	out := make([]byte, last+1)
	for i := range out {
		out[i] = rp.pal[i][3]
	}
	return out
}

func paletteDepth(n int) byte {
	switch {
	case n <= 2:
		return 1
	case n <= 4:
		return 2
	case n <= 16:
		return 4
	}
	return 8
}

type palOrder int

const (
	orderFreq palOrder = iota
	orderFirst
	orderLuma
)

func paletteRep(an *analysis, ord palOrder) *rep {
	n := an.tab.n
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	switch ord {
	case orderFreq:
		slices.SortStableFunc(perm, func(a, b int) int { return cmp.Compare(an.counts[b], an.counts[a]) })
	case orderLuma:
		luma := func(i int) int {
			c := unpackKey(an.tab.list[i])
			return 299*int(c[0]) + 587*int(c[1]) + 114*int(c[2])
		}
		slices.SortStableFunc(perm, func(a, b int) int { return cmp.Compare(luma(a), luma(b)) })
	}
	// Non-opaque entries first keep the tRNS chunk as short as possible.
	slices.SortStableFunc(perm, func(a, b int) int {
		oa := unpackKey(an.tab.list[a])[3] == 255
		ob := unpackKey(an.tab.list[b])[3] == 255
		switch {
		case oa == ob:
			return 0
		case !oa:
			return -1
		}
		return 1
	})
	rp := &rep{ctype: 3, depth: paletteDepth(n), tab: newColorTable()}
	for _, i := range perm {
		k := an.tab.list[i]
		rp.tab.add(k)
		rp.pal = append(rp.pal, unpackKey(k))
	}
	return rp
}

func chooseReps8(an *analysis, level Level, m *meta, px int) []*rep {
	grayOK := an.gray && !m.lockRGB
	palOK := an.palOK && !m.lockGray
	std := 8
	if an.gray && an.opaque && an.palOK {
		std = stdGrayDepth(an.tab)
	}
	var tc *rep
	switch {
	case grayOK && an.opaque:
		tc = &rep{ctype: 0, depth: byte(std)}
	case grayOK:
		tc = &rep{ctype: 4, depth: 8}
	case an.opaque:
		tc = &rep{ctype: 2, depth: 8}
	default:
		tc = &rep{ctype: 6, depth: 8}
	}
	if !palOK {
		return []*rep{tc}
	}
	n := an.tab.n
	preferTrue := grayOK && an.opaque && (std < 8 || n > 16)
	var reps []*rep
	if preferTrue {
		reps = append(reps, tc)
		if level == Best && std == 8 && px <= bestAltPixels {
			reps = append(reps, paletteRep(an, orderFreq), paletteRep(an, orderLuma))
		}
		return reps
	}
	reps = append(reps, paletteRep(an, orderFreq))
	if (level >= Default && n > 16) || level == Best {
		reps = append(reps, tc)
	}
	if level == Best && n > 2 && px <= bestAltPixels {
		reps = append(reps, paletteRep(an, orderFirst), paletteRep(an, orderLuma))
	}
	return reps
}

func chooseReps16(r *raster, m *meta) *rep {
	opaque, gray := analyze16(r)
	gray = gray && !m.lockRGB
	switch {
	case gray && opaque:
		return &rep{ctype: 0, depth: 16}
	case gray:
		return &rep{ctype: 4, depth: 16}
	case opaque:
		return &rep{ctype: 2, depth: 16}
	}
	return &rep{ctype: 6, depth: 16}
}

// pack converts the raster to scanlines of rp, each preceded by a zero
// filter byte.
func pack(r *raster, rp *rep) []byte {
	h := rp.header()
	rb := h.rowBytes(r.w)
	stride := rb + 1
	buf := make([]byte, stride*r.h)
	ps := r.pixSize()
	step := 1
	if rp.ctype == 0 && rp.depth < 8 {
		step = 255 / (1<<rp.depth - 1)
	}
	for y := range r.h {
		src := r.pix[y*r.w*ps : (y+1)*r.w*ps]
		dst := buf[y*stride+1 : (y+1)*stride]
		switch {
		case rp.ctype == 6:
			copy(dst, src)
		case rp.ctype == 2 && !r.deep:
			for i := range r.w {
				copy(dst[3*i:3*i+3], src[4*i:4*i+3])
			}
		case rp.ctype == 2:
			for i := range r.w {
				copy(dst[6*i:6*i+6], src[8*i:8*i+6])
			}
		case rp.ctype == 0 && r.deep:
			for i := range r.w {
				dst[2*i], dst[2*i+1] = src[8*i], src[8*i+1]
			}
		case rp.ctype == 0 && rp.depth == 8:
			for i := range r.w {
				dst[i] = src[4*i]
			}
		case rp.ctype == 0:
			for i := range r.w {
				putBits(dst, i, int(rp.depth), int(src[4*i])/step)
			}
		case rp.ctype == 4 && !r.deep:
			for i := range r.w {
				dst[2*i], dst[2*i+1] = src[4*i], src[4*i+3]
			}
		case rp.ctype == 4:
			for i := range r.w {
				dst[4*i], dst[4*i+1], dst[4*i+2], dst[4*i+3] = src[8*i], src[8*i+1], src[8*i+6], src[8*i+7]
			}
		default:
			last, lastIdx := uint32(0), -1
			for i := range r.w {
				k := packKey(src[4*i], src[4*i+1], src[4*i+2], src[4*i+3])
				if lastIdx < 0 || k != last {
					last, lastIdx = k, rp.tab.find(k)
				}
				if rp.depth == 8 {
					dst[i] = byte(lastIdx)
				} else {
					putBits(dst, i, int(rp.depth), lastIdx)
				}
			}
		}
	}
	return buf
}

func putBits(dst []byte, i, depth, v int) {
	bit := i * depth
	dst[bit>>3] |= byte(v) << (8 - depth - bit&7)
}

var zwPools [10]sync.Pool

func deflateZ(data []byte, lvl int) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(data)/3 + 64)
	w, _ := zwPools[lvl].Get().(*zlib.Writer)
	if w == nil {
		var err error
		if w, err = zlib.NewWriterLevel(&buf, lvl); err != nil {
			return nil, err
		}
	} else {
		w.Reset(&buf)
	}
	defer zwPools[lvl].Put(w)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func strategies(level Level, indexed bool) []strategy {
	switch level {
	case Fast:
		if indexed {
			return []strategy{stNone}
		}
		return []strategy{stMSAD}
	case Default:
		if indexed {
			return []strategy{stNone, stMSAD, stEntropy, stSub, stUp}
		}
		return []strategy{stMSAD, stEntropy, stSub, stUp, stAvg, stPaeth}
	}
	return []strategy{stMSAD, stEntropy, stNone, stSub, stUp, stAvg, stPaeth}
}

const (
	tinyPixels    = 16384
	bestAltPixels = 1 << 18
	smallRaw      = 256 << 10
	trialWindow   = 192 << 10
	trialSmall    = 6
	trialSampled  = 6
	trialWindows  = 8
	fullTrialMax  = 4 << 20
	ratio9MaxRaw  = 4 << 20
	ratio9Min     = 3
)

// finalLevels lists the klauspost deflate levels tried for the winning
// strategy, best guess first. Levels 7 and 8 compress photographic data better
// than 9 and are usually faster (9 can be several times slower), level 3
// beats levels 4 to 6 on such data, and the best level of small images varies
// from image to image, so those get several tries. The effort shrinks with the
// image size to keep the running time bounded.
func finalLevels(level Level, rawLen int) []int {
	switch level {
	case Fast:
		return []int{3}
	case Default:
		switch {
		case rawLen <= smallRaw:
			return []int{8, 9, 7}
		case rawLen <= 640<<10:
			return []int{8, 9}
		case rawLen <= 16<<20:
			return []int{8}
		}
		return []int{7}
	}
	switch {
	case rawLen <= 32<<10:
		return []int{9, 8, 7, 6, 5, 4, 3}
	case rawLen <= 128<<10:
		return []int{9, 8, 7, 6}
	case rawLen <= 512<<10:
		return []int{8, 7, 9}
	case rawLen <= 16<<20:
		return []int{8, 7}
	}
	return []int{7}
}

// rankStrategies orders the strategies by the size of a fast deflate of
// their output and returns the best ones. Small images (and, for Best, images
// up to fullTrialMax) are compressed whole; larger ones are sampled with
// windows of rows spread over the image.
func rankStrategies(f *filterer, raw, out []byte, strats []strategy, level Level, h, rb int) ([]strategy, error) {
	stride := rb + 1
	type window struct{ y0, y1 int }
	windows := []window{{0, h}}
	lvl := trialSmall
	if len(raw) > smallRaw && (level != Best || len(raw) > fullTrialMax) {
		rows := min(h, max(4, trialWindow/trialWindows/stride))
		windows = windows[:0]
		for k := range trialWindows {
			y0 := (h - rows) * k / (trialWindows - 1)
			windows = append(windows, window{y0, y0 + rows})
		}
		lvl = trialSampled
	}
	type result struct {
		st   strategy
		size int
	}
	results := make([]result, 0, len(strats))
	for _, st := range strats {
		total := 0
		for _, w := range windows {
			z, err := deflateZ(f.run(st, raw, out, w.y0, w.y1)[w.y0*stride:w.y1*stride], lvl)
			if err != nil {
				return nil, err
			}
			total += len(z)
		}
		results = append(results, result{st, total})
	}
	slices.SortStableFunc(results, func(a, b result) int { return cmp.Compare(a.size, b.size) })
	keep := 1
	if level == Best || (level == Default && len(raw) <= 1<<20) {
		keep = 2
	}
	rank := make([]strategy, 0, keep)
	for _, r := range results[:keep] {
		rank = append(rank, r.st)
	}
	return rank, nil
}

// compressRaw filters raw (see pack) with the best strategy found and
// returns the zlib stream.
func compressRaw(raw []byte, rp *rep, w, h int, level Level) ([]byte, error) {
	hd := rp.header()
	rb := hd.rowBytes(w)
	f := newFilterer(h, rb, hd.filterBPP())
	strats := strategies(level, rp.ctype == 3 || rp.depth < 8)
	out := make([]byte, len(raw))
	levels := finalLevels(level, len(raw))

	rank := strats
	if len(strats) > 1 {
		var err error
		if rank, err = rankStrategies(f, raw, out, strats, level, h, rb); err != nil {
			return nil, err
		}
	}
	var best []byte
	var bestSt strategy
	tried9 := false
	for i, st := range rank {
		filtered := f.run(st, raw, out, 0, h)
		lv := levels
		if i > 0 {
			lv = levels[:1]
		}
		for _, l := range lv {
			z, err := deflateZ(filtered, l)
			if err != nil {
				return nil, err
			}
			if best == nil || len(z) < len(best) {
				best, bestSt = z, st
			}
			tried9 = tried9 || l == 9
		}
	}
	// Level 9 is slow on photographs and no better there, but finds the long
	// repeats of graphics, which compress by a large factor.
	if !tried9 && level >= Default && len(raw) <= ratio9MaxRaw && len(best)*ratio9Min <= len(raw) {
		z, err := deflateZ(f.run(bestSt, raw, out, 0, h), 9)
		if err != nil {
			return nil, err
		}
		if len(z) < len(best) {
			best = z
		}
	}
	if level == Fast && len(best)*8 <= len(raw) {
		// Very compressible data is cheap to compress and gains a lot from level 6.
		z, err := deflateZ(f.run(bestSt, raw, out, 0, h), 6)
		if err != nil {
			return nil, err
		}
		if len(z) < len(best) {
			best = z
		}
	}
	return best, nil
}

func sbitValid(sbit []byte, rp *rep) bool {
	want := rp.header().channels()
	if rp.ctype == 3 {
		want = 3
	}
	if len(sbit) != want {
		return false
	}
	maxd := rp.depth
	if rp.ctype == 3 {
		maxd = 8
	}
	for _, v := range sbit {
		if v == 0 || v > maxd {
			return false
		}
	}
	return true
}

func appendChunk(dst []byte, typ string, data []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data)))
	start := len(dst)
	dst = append(dst, typ...)
	dst = append(dst, data...)
	return binary.BigEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:]))
}

// build encodes r with the best representation and returns the PNG file.
func build(r *raster, m *meta, level Level) ([]byte, error) {
	if level < Fast {
		level = Fast
	} else if level > Best {
		level = Best
	}
	if level == Default && r.w*r.h <= tinyPixels {
		level = Best
	}
	if r.deep {
		if red := reduce16(r); red != nil {
			r = red
		}
	}
	var reps []*rep
	if r.deep {
		reps = []*rep{chooseReps16(r, m)}
	} else {
		reps = chooseReps8(analyze8(r), level, m, r.w*r.h)
	}
	var bestRep *rep
	var bestZ []byte
	bestSize := 0
	for _, rp := range reps {
		z, err := compressRaw(pack(r, rp), rp, r.w, r.h, level)
		if err != nil {
			return nil, err
		}
		if size := len(z) + rp.overhead(); bestRep == nil || size < bestSize {
			bestRep, bestZ, bestSize = rp, z, size
		}
	}
	rp := bestRep
	out := make([]byte, 0, len(bestZ)+512)
	out = append(out, pngSig...)
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:], uint32(r.w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(r.h))
	ihdr[8], ihdr[9] = rp.depth, rp.ctype
	out = appendChunk(out, "IHDR", ihdr[:])
	for _, c := range m.color {
		if c.typ == "sBIT" && !sbitValid(c.data, rp) {
			continue
		}
		out = appendChunk(out, c.typ, c.data)
	}
	if rp.ctype == 3 {
		plte := make([]byte, 0, 3*len(rp.pal))
		for _, c := range rp.pal {
			plte = append(plte, c[0], c[1], c[2])
		}
		out = appendChunk(out, "PLTE", plte)
		if t := rp.trns(); t != nil {
			out = appendChunk(out, "tRNS", t)
		}
	}
	for _, c := range m.late {
		out = appendChunk(out, c.typ, c.data)
	}
	out = appendChunk(out, "IDAT", bestZ)
	return appendChunk(out, "IEND", nil), nil
}
