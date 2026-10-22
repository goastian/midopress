package quality

import "testing"

func benchSSIMULACRA2(b *testing.B, w, h int) {
	ref := synthPhoto(w, h, 1)
	cand := noisy(ref, 5)
	r, err := NewSSIMULACRA2Reference(ref, Options{MaxPixels: w * h})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(w * h * 4))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Score(cand); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Milliseconds())/float64(b.N)/(float64(w*h)/1e6), "ms/Mpx")
}

func BenchmarkSSIMULACRA2Score768x512(b *testing.B)   { benchSSIMULACRA2(b, 768, 512) }
func BenchmarkSSIMULACRA2Score4000x3000(b *testing.B) { benchSSIMULACRA2(b, 4000, 3000) }

func BenchmarkSSIMULACRA2Reference768x512(b *testing.B) {
	ref := synthPhoto(768, 512, 1)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := NewSSIMULACRA2Reference(ref); err != nil {
			b.Fatal(err)
		}
	}
}
