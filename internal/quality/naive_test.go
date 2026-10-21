package quality

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/require"
)

// naivePlanes converts an image to the three planes using the textbook
// definitions: per-pixel RGB over mid-grey, BT.601 YCbCr, then the chroma of
// the (rounded) average RGB of every 2x2 block.
func naivePlanes(img image.Image) (planes [3][]float64, dims [3][2]int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgb := make([][3]int, w*h)
	for y := range h {
		for x := range w {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			r, g, bl, a = r>>8, g>>8, bl>>8, a>>8
			over := func(c uint32) int { return min(int(c)+(128*(255-int(a))+127)/255, 255) }
			rgb[y*w+x] = [3]int{over(r), over(g), over(bl)}
		}
	}
	luma := make([]float64, w*h)
	for i, p := range rgb {
		y, _, _ := color.RGBToYCbCr(uint8(p[0]), uint8(p[1]), uint8(p[2]))
		luma[i] = float64(y)
	}
	cw, ch := (w+1)/2, (h+1)/2
	cb := make([]float64, cw*ch)
	cr := make([]float64, cw*ch)
	for cy := range ch {
		for cx := range cw {
			var sum [3]int
			n := 0
			for dy := range 2 {
				for dx := range 2 {
					x, y := 2*cx+dx, 2*cy+dy
					if x >= w || y >= h {
						continue
					}
					for k := range 3 {
						sum[k] += rgb[y*w+x][k]
					}
					n++
				}
			}
			_, u, v := color.RGBToYCbCr(uint8((sum[0]+n/2)/n), uint8((sum[1]+n/2)/n), uint8((sum[2]+n/2)/n))
			cb[cy*cw+cx], cr[cy*cw+cx] = float64(u), float64(v)
		}
	}
	return [3][]float64{luma, cb, cr}, [3][2]int{{w, h}, {cw, ch}, {cw, ch}}
}

func naiveSSIM(a, b []float64, w, h int) float64 {
	wx, wy := min(8, w), min(8, h)
	n := float64(wx * wy)
	const c1, c2 = (0.01 * 255) * (0.01 * 255), (0.03 * 255) * (0.03 * 255)
	var total float64
	count := 0
	for y := 0; y+wy <= h; y += 4 {
		for x := 0; x+wx <= w; x += 4 {
			var ma, mb float64
			for j := range wy {
				for i := range wx {
					ma += a[(y+j)*w+x+i]
					mb += b[(y+j)*w+x+i]
				}
			}
			ma /= n
			mb /= n
			var va, vb, cov float64
			for j := range wy {
				for i := range wx {
					da, db := a[(y+j)*w+x+i]-ma, b[(y+j)*w+x+i]-mb
					va += da * da
					vb += db * db
					cov += da * db
				}
			}
			va /= n
			vb /= n
			cov /= n
			total += (2*ma*mb + c1) * (2*cov + c2) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			count++
		}
	}
	return total / float64(count)
}

func naiveDetail(ref, got image.Image) Detail {
	pa, dims := naivePlanes(ref)
	pb, _ := naivePlanes(got)
	var v [3]float64
	for i := range v {
		v[i] = naiveSSIM(pa[i], pb[i], dims[i][0], dims[i][1])
	}
	score := 0.8*v[0] + 0.1*v[1] + 0.1*v[2]
	return Detail{Luma: v[0], Cb: v[1], Cr: v[2], Score: min(max(score, 0), 1)}
}

func TestMatchesNaiveImplementation(t *testing.T) {
	sizes := [][2]int{{37, 53}, {8, 8}, {9, 9}, {100, 7}, {1, 1}, {3, 5}, {7, 8}, {8, 7}, {15, 15}, {16, 16}, {17, 33}, {64, 64}, {5, 90}}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		t.Run(image.Rect(0, 0, w, h).Size().String(), func(t *testing.T) {
			cases := map[string][2]*image.RGBA{
				"random":     {randomRGBA(w, h, 1), randomRGBA(w, h, 2)},
				"correlated": {synthPhoto(w, h, 3), noisy(synthPhoto(w, h, 3), 12)},
				"identical":  {synthPhoto(w, h, 4), synthPhoto(w, h, 4)},
			}
			for name, c := range cases {
				ref, err := NewReference(c[0])
				require.NoError(t, err)
				got, err := ref.Detail(c[1])
				require.NoError(t, err)
				want := naiveDetail(c[0], c[1])
				require.InDelta(t, want.Luma, got.Luma, 1e-6, name)
				require.InDelta(t, want.Cb, got.Cb, 1e-6, name)
				require.InDelta(t, want.Cr, got.Cr, 1e-6, name)
				require.InDelta(t, want.Score, got.Score, 1e-6, name)
			}
		})
	}
}

func TestNaiveMatchesWithAlpha(t *testing.T) {
	w, h := 41, 29
	a := buildImage(t, "nrgba", image.Rect(0, 0, w, h), 5)
	b := buildImage(t, "nrgba", image.Rect(0, 0, w, h), 5)
	mutate(b, 6)
	ref, err := NewReference(a)
	require.NoError(t, err)
	got, err := ref.Detail(b)
	require.NoError(t, err)
	want := naiveDetail(a, b)
	require.InDelta(t, want.Luma, got.Luma, 1e-6)
	require.InDelta(t, want.Cb, got.Cb, 1e-6)
	require.InDelta(t, want.Cr, got.Cr, 1e-6)
}
