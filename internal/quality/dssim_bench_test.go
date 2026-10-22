package quality

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func benchDSSIM(b *testing.B, w, h int, opts ...Options) {
	ref := synthPhoto(w, h, 1)
	mod := dsJPEG(b, ref, 60)
	r, err := NewDSSIMReference(ref, opts...)
	require.NoError(b, err)
	measured := r.scales[0].w * r.scales[0].h
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := r.Distance(mod); err != nil {
			b.Fatal(err)
		}
	}
	perOp := float64(b.Elapsed().Nanoseconds()) / float64(b.N) / 1e6
	b.ReportMetric(perOp/(float64(w*h)/1e6), "ms/Mpx-source")
	b.ReportMetric(perOp/(float64(measured)/1e6), "ms/Mpx-measured")
}

func BenchmarkDSSIMDistance768x512(b *testing.B)   { benchDSSIM(b, 768, 512) }
func BenchmarkDSSIMDistance1600x1200(b *testing.B) { benchDSSIM(b, 1600, 1200) }
func BenchmarkDSSIMDistance1920x1080(b *testing.B) { benchDSSIM(b, 1920, 1080) }
func BenchmarkDSSIMDistance4000x3000(b *testing.B) { benchDSSIM(b, 4000, 3000) }

func BenchmarkDSSIMReference768x512(b *testing.B) {
	ref := synthPhoto(768, 512, 1)
	b.ReportAllocs()
	for range b.N {
		if _, err := NewDSSIMReference(ref); err != nil {
			b.Fatal(err)
		}
	}
}
