package quality

import (
	"image"
	"testing"

	"github.com/stretchr/testify/require"
)

func benchSimilarity(b *testing.B, src *image.RGBA, cand image.Image, opts ...Options) {
	ref, err := NewReference(src, opts...)
	require.NoError(b, err)
	b.ReportAllocs()
	b.SetBytes(int64(src.Bounds().Dx() * src.Bounds().Dy()))
	b.ResetTimer()
	for range b.N {
		if _, err := ref.Similarity(cand); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSimilarity768x512RGBA(b *testing.B) {
	src := synthPhoto(768, 512, 1)
	benchSimilarity(b, src, noisy(src, 8))
}

func BenchmarkSimilarity768x512YCbCr420(b *testing.B) {
	src := synthPhoto(768, 512, 1)
	benchSimilarity(b, src, jpegRoundTrip(b, src, 60))
}

func BenchmarkSimilarity768x512NRGBA(b *testing.B) {
	src := synthPhoto(768, 512, 1)
	cand := image.NewNRGBA(src.Rect)
	copy(cand.Pix, noisy(src, 8).Pix)
	benchSimilarity(b, src, cand)
}

func BenchmarkSimilarityLarge4000x3000(b *testing.B) {
	src := synthPhoto(4000, 3000, 1)
	benchSimilarity(b, src, noisy(src, 8))
}

func BenchmarkSimilarityLarge4000x3000NoReduction(b *testing.B) {
	src := synthPhoto(4000, 3000, 1)
	benchSimilarity(b, src, noisy(src, 8), Options{MaxPixels: 1 << 30})
}

func BenchmarkSimilarityLarge4000x3000YCbCr420(b *testing.B) {
	src := synthPhoto(4000, 3000, 1)
	benchSimilarity(b, src, jpegRoundTrip(b, src, 60))
}

func BenchmarkNewReference768x512(b *testing.B) {
	src := synthPhoto(768, 512, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := NewReference(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNewReferenceLarge4000x3000(b *testing.B) {
	src := synthPhoto(4000, 3000, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := NewReference(src); err != nil {
			b.Fatal(err)
		}
	}
}
