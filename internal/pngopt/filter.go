package pngopt

import (
	"errors"
	"math"
)

const (
	filterNone = iota
	filterSub
	filterUp
	filterAvg
	filterPaeth
)

// strategy selects how the filter type of each row is chosen. Values 0..4
// apply that filter to every row.
type strategy int

const (
	stNone strategy = iota
	stSub
	stUp
	stAvg
	stPaeth
	stMSAD
	stEntropy
)

var errBadFilter = errors.New("pngopt: invalid filter type")

var absTab = func() (t [256]uint8) {
	for i := range t {
		v := int(int8(i))
		if v < 0 {
			v = -v
		}
		t[i] = uint8(v)
	}
	return
}()

func paeth(a, b, c byte) byte {
	ia, ib, ic := int32(a), int32(b), int32(c)
	pa := ib - ic
	pb := ia - ic
	pc := pa + pb
	pa = (pa ^ pa>>31) - pa>>31
	pb = (pb ^ pb>>31) - pb>>31
	pc = (pc ^ pc>>31) - pc>>31
	d1 := (pc - pb) >> 31
	r := ib ^ ((ib ^ ic) & d1)
	m := pb ^ ((pb ^ pc) & d1)
	d2 := (m - pa) >> 31
	return byte(ia ^ ((ia ^ r) & d2))
}

// applyFilter writes the filtered form of cur into dst. prev is the previous
// unfiltered row (all zero for the first row).
func applyFilter(ft int, dst, cur, prev []byte, bpp int) {
	n := len(cur)
	dst = dst[:n]
	prev = prev[:n]
	switch ft {
	case filterNone:
		copy(dst, cur)
	case filterSub:
		copy(dst[:bpp], cur[:bpp])
		for i := bpp; i < n; i++ {
			dst[i] = cur[i] - cur[i-bpp]
		}
	case filterUp:
		for i := range n {
			dst[i] = cur[i] - prev[i]
		}
	case filterAvg:
		for i := 0; i < bpp && i < n; i++ {
			dst[i] = cur[i] - prev[i]>>1
		}
		for i := bpp; i < n; i++ {
			dst[i] = cur[i] - byte((int(cur[i-bpp])+int(prev[i]))>>1)
		}
	case filterPaeth:
		for i := 0; i < bpp && i < n; i++ {
			dst[i] = cur[i] - prev[i]
		}
		for i := bpp; i < n; i++ {
			dst[i] = cur[i] - paeth(cur[i-bpp], prev[i], prev[i-bpp])
		}
	}
}

// unfilter reverses applyFilter in place; cur holds the filtered row on entry.
func unfilter(ft byte, cur, prev []byte, bpp int) error {
	n := len(cur)
	prev = prev[:n]
	switch ft {
	case filterNone:
	case filterSub:
		for i := bpp; i < n; i++ {
			cur[i] += cur[i-bpp]
		}
	case filterUp:
		for i := range n {
			cur[i] += prev[i]
		}
	case filterAvg:
		for i := 0; i < bpp && i < n; i++ {
			cur[i] += prev[i] >> 1
		}
		for i := bpp; i < n; i++ {
			cur[i] += byte((int(cur[i-bpp]) + int(prev[i])) >> 1)
		}
	case filterPaeth:
		for i := 0; i < bpp && i < n; i++ {
			cur[i] += prev[i]
		}
		for i := bpp; i < n; i++ {
			cur[i] += paeth(cur[i-bpp], prev[i], prev[i-bpp])
		}
	default:
		return errBadFilter
	}
	return nil
}

// rowCosts returns the sum of absolute residuals of every filter for a row.
func rowCosts(cur, prev []byte, bpp int) (costs [5]int) {
	n := len(cur)
	prev = prev[:n]
	for i := 0; i < bpp && i < n; i++ {
		x, b := cur[i], prev[i]
		costs[0] += int(absTab[x])
		costs[1] += int(absTab[x])
		costs[2] += int(absTab[x-b])
		costs[3] += int(absTab[x-b>>1])
		costs[4] += int(absTab[x-b])
	}
	var s0, s1, s2, s3, s4 int
	for i := bpp; i < n; i++ {
		x, a, b, c := cur[i], cur[i-bpp], prev[i], prev[i-bpp]
		s0 += int(absTab[x])
		s1 += int(absTab[x-a])
		s2 += int(absTab[x-b])
		s3 += int(absTab[x-byte((int(a)+int(b))>>1)])
		s4 += int(absTab[x-paeth(a, b, c)])
	}
	costs[0] += s0
	costs[1] += s1
	costs[2] += s2
	costs[3] += s3
	costs[4] += s4
	return costs
}

// xlogx[c] is c*log2(c), the per-symbol term of the entropy score.
var xlogx = func() []float32 {
	t := make([]float32, 1<<16)
	for c := 2; c < len(t); c++ {
		f := float64(c)
		t[c] = float32(f * math.Log2(f))
	}
	return t
}()

func scoreHist(hist *[256]int32) float64 {
	s := 0.0
	for _, c := range hist {
		switch {
		case c < 2:
		case int(c) < len(xlogx):
			s += float64(xlogx[c])
		default:
			f := float64(c)
			s += f * math.Log2(f)
		}
	}
	return s
}

// pickEntropy returns the filter whose residuals have the lowest entropy.
func pickEntropy(cur, prev []byte, bpp int, hists *[5][256]int32) int {
	*hists = [5][256]int32{}
	n := len(cur)
	prev = prev[:n]
	for i := 0; i < bpp && i < n; i++ {
		x, b := cur[i], prev[i]
		hists[0][x]++
		hists[1][x]++
		hists[2][x-b]++
		hists[3][x-b>>1]++
		hists[4][x-b]++
	}
	h0, h1, h2, h3, h4 := &hists[0], &hists[1], &hists[2], &hists[3], &hists[4]
	for i := bpp; i < n; i++ {
		x, a, b, c := cur[i], cur[i-bpp], prev[i], prev[i-bpp]
		h0[x]++
		h1[x-a]++
		h2[x-b]++
		h3[x-byte((int(a)+int(b))>>1)]++
		h4[x-paeth(a, b, c)]++
	}
	best, bestScore := 0, -1.0
	for i := range hists {
		if sc := scoreHist(&hists[i]); sc > bestScore {
			best, bestScore = i, sc
		}
	}
	return best
}

// filterer applies a strategy to a whole image of h rows of rb bytes. Input
// and output use the layout [filter byte][rb bytes] per row.
type filterer struct {
	h, rb, bpp int
	zero       []byte
	hists      [5][256]int32
}

func newFilterer(h, rb, bpp int) *filterer {
	return &filterer{h: h, rb: rb, bpp: bpp, zero: make([]byte, rb)}
}

// run filters rows y0..y1 of raw (which must hold zero filter bytes) into
// out, which has the same layout, and returns the buffer holding the result.
// That buffer is raw itself for stNone.
func (f *filterer) run(st strategy, raw, out []byte, y0, y1 int) []byte {
	stride := f.rb + 1
	if st == stNone {
		return raw
	}
	prev := f.zero
	if y0 > 0 {
		prev = raw[(y0-1)*stride+1 : y0*stride]
	}
	for y := y0; y < y1; y++ {
		off := y * stride
		cur := raw[off+1 : off+stride]
		dst := out[off+1 : off+stride]
		ft := int(st)
		switch st {
		case stMSAD:
			costs := rowCosts(cur, prev, f.bpp)
			ft = 0
			for i := 1; i < 5; i++ {
				if costs[i] < costs[ft] {
					ft = i
				}
			}
		case stEntropy:
			ft = pickEntropy(cur, prev, f.bpp, &f.hists)
		}
		applyFilter(ft, dst, cur, prev, f.bpp)
		out[off] = byte(ft)
		prev = cur
	}
	return out
}
