package imgmeta

import (
	"encoding/binary"
	"os"
	"testing"
)

func benchJPEG(b *testing.B) []byte {
	if raw, err := os.ReadFile("/tmp/opencode/img/gal3.jpg"); err == nil {
		return raw
	}
	return encodeJPEG(b, gradient(900, 700, 5, 60), 92)
}

func benchPNG(b *testing.B) []byte {
	if raw, err := os.ReadFile("/tmp/opencode/img/kodim01.png"); err == nil {
		return raw
	}
	return encodePNG(b, gradient(512, 512, 5, 120))
}

func BenchmarkParseJPEG(b *testing.B) {
	raw := benchJPEG(b)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseJPEG(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseJPEGWithICC(b *testing.B) {
	segs := append([][]byte{jseg(0xE1, exifPayload(binary.LittleEndian, 6))}, iccChunkSegs(srgbProfile(), 400)...)
	raw := inject(benchJPEG(b), segs...)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseJPEG(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStripJPEGNothing(b *testing.B) {
	raw := benchJPEG(b)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := StripJPEG(raw, DefaultStripOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStripJPEG(b *testing.B) {
	segs := [][]byte{
		jseg(0xE1, exifPayload(binary.LittleEndian, 6)),
		jseg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>padding padding padding</x:xmpmeta>")),
		jseg(0xFE, []byte("comment")),
	}
	segs = append(segs, iccChunkSegs(srgbProfile(), 400)...)
	raw := inject(benchJPEG(b), segs...)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := StripJPEG(raw, DefaultStripOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParsePNG(b *testing.B) {
	raw := benchPNG(b)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParsePNG(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStripPNG(b *testing.B) {
	raw := injectPNG(benchPNG(b), pngChunkBytes("tEXt", []byte("Comment\x00hello")), pngChunkBytes("pHYs", make([]byte, 9)))
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := StripPNG(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkClassifyICC(b *testing.B) {
	p := srgbProfile()
	b.ReportAllocs()
	for b.Loop() {
		ClassifyICC(p)
	}
}
