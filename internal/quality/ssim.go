package quality

const (
	ssimC1 = 6.5025  // (0.01*255)^2
	ssimC2 = 58.5225 // (0.03*255)^2
)

// ssimWindow evaluates SSIM for one window of n pixels from the sums of the
// reference values (sa, saa), the candidate values (sb, sbb) and their
// products (sab). All sums are exact integers in float64, so identical
// inputs give exactly 1.
func ssimWindow(n, sa, sb, saa, sbb, sab float64) float64 {
	c1 := ssimC1 * n * n
	c2 := ssimC2 * n * n
	cov := n*sab - sa*sb
	vr := n*(saa+sbb) - sa*sa - sb*sb
	return (2*sa*sb + c1) * (2*cov + c2) / ((sa*sa + sb*sb + c1) * (vr + c2))
}

// blockSums returns the sum and sum of squares of every 4x4 block of the
// plane, in row-major order of (w/4)x(h/4) blocks.
func blockSums(pix []uint8, w, h int) (sa, saa []int32) {
	bw, bh := w/blockSize, h/blockSize
	sa = make([]int32, bw*bh)
	saa = make([]int32, bw*bh)
	for j := range bh {
		for i := range bw {
			var s, ss int32
			for r := range blockSize {
				o := (j*blockSize+r)*w + i*blockSize
				for _, v := range pix[o : o+blockSize : o+blockSize] {
					s += int32(v)
					ss += int32(v) * int32(v)
				}
			}
			sa[j*bw+i], saa[j*bw+i] = s, ss
		}
	}
	return sa, saa
}

// ssim returns the mean SSIM of cand against the reference plane p.
func (s *scratch) ssim(p *refPlane, cand []uint8) float64 {
	if p.sa == nil {
		return ssimSmall(p.pix, cand, p.w, p.h)
	}
	bw, bh := p.w/blockSize, p.h/blockSize
	s.blocks = grow(s.blocks, 6*bw)
	cur, prev := s.blocks[:3*bw], s.blocks[3*bw:]
	var total float64
	for j := range bh {
		candBlockSums(cur, p.pix, cand, p.w, j, bw)
		if j > 0 {
			total += windowRow(p, prev, cur, j-1, bw)
		}
		cur, prev = prev, cur
	}
	return total / float64((bw-1)*(bh-1))
}

// candBlockSums fills dst with, for each 4x4 block of block row j, the sum of
// candidate values, of their squares and of their products with the reference.
func candBlockSums(dst []int32, ref, cand []uint8, w, j, bw int) {
	sb, sbb, sab := dst[:bw], dst[bw:2*bw], dst[2*bw:3*bw]
	base := j * blockSize * w
	for i := range bw {
		var t, tt, ta int32
		x := base + i*blockSize
		for range blockSize {
			a := ref[x : x+4 : x+4]
			b := cand[x : x+4 : x+4]
			b0, b1, b2, b3 := int32(b[0]), int32(b[1]), int32(b[2]), int32(b[3])
			t += b0 + b1 + b2 + b3
			tt += b0*b0 + b1*b1 + b2*b2 + b3*b3
			ta += int32(a[0])*b0 + int32(a[1])*b1 + int32(a[2])*b2 + int32(a[3])*b3
			x += w
		}
		sb[i], sbb[i], sab[i] = t, tt, ta
	}
}

// windowRow sums SSIM over the windows spanning block rows j and j+1, given
// the candidate block sums of both rows.
func windowRow(p *refPlane, top, bot []int32, j, bw int) float64 {
	tb, tbb, tab := top[:bw], top[bw:2*bw], top[2*bw:3*bw]
	bb, bbb, bab := bot[:bw], bot[bw:2*bw], bot[2*bw:3*bw]
	ra0 := p.sa[j*bw : (j+1)*bw]
	ra1 := p.sa[(j+1)*bw : (j+2)*bw]
	rq0 := p.saa[j*bw : (j+1)*bw]
	rq1 := p.saa[(j+1)*bw : (j+2)*bw]
	var total float64
	for i := range bw - 1 {
		sa := ra0[i] + ra0[i+1] + ra1[i] + ra1[i+1]
		saa := rq0[i] + rq0[i+1] + rq1[i] + rq1[i+1]
		sb := tb[i] + tb[i+1] + bb[i] + bb[i+1]
		sbb := tbb[i] + tbb[i+1] + bbb[i] + bbb[i+1]
		sab := tab[i] + tab[i+1] + bab[i] + bab[i+1]
		total += ssimWindow(windowSize*windowSize, float64(sa), float64(sb), float64(saa), float64(sbb), float64(sab))
	}
	return total
}

// ssimSmall is the direct implementation used for planes smaller than 8x8 in
// a dimension: the window is shrunk to min(8, size) in that dimension, so a
// single window covers all of it.
func ssimSmall(a, b []uint8, w, h int) float64 {
	wx, wy := min(windowSize, w), min(windowSize, h)
	n := float64(wx * wy)
	var total float64
	var count int
	for y := 0; y+wy <= h; y += blockSize {
		for x := 0; x+wx <= w; x += blockSize {
			var sa, sb, saa, sbb, sab int64
			for r := range wy {
				o := (y+r)*w + x
				for c := range wx {
					va, vb := int64(a[o+c]), int64(b[o+c])
					sa += va
					sb += vb
					saa += va * va
					sbb += vb * vb
					sab += va * vb
				}
			}
			total += ssimWindow(n, float64(sa), float64(sb), float64(saa), float64(sbb), float64(sab))
			count++
		}
	}
	return total / float64(count)
}
