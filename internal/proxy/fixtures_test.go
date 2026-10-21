package proxy_test

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fixtures struct {
	html []byte
	png  []byte
	jpeg []byte
	gif  []byte
}

var (
	fixturesOnce sync.Once
	fixturesData fixtures
)

func loadFixtures() *fixtures {
	fixturesOnce.Do(func() {
		fixturesData = fixtures{
			html: buildHTML(),
			png:  buildPNG(),
			jpeg: buildJPEG(),
			gif:  buildGIF(),
		}
	})
	return &fixturesData
}

func buildHTML() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n  <head>\n    <title>fixture</title>\n  </head>\n  <body>\n")
	for range 200 {
		b.WriteString("    <p>Call me Ishmael. Some years ago, never mind how long precisely.</p>\n")
	}
	b.WriteString("  </body>\n</html>\n")
	return []byte(b.String())
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: uint8((x + y) * 255 / (w + h)), A: 255})
		}
	}
	return img
}

func buildPNG() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(256, 256)); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// buildJPEG makes a noisy image so that the encoded size depends on quality.
func buildJPEG() []byte {
	rng := rand.New(rand.NewPCG(1, 2))
	img := gradient(320, 240)
	for y := range 240 {
		for x := range 320 {
			c := img.RGBAAt(x, y)
			n := uint8(rng.IntN(64))
			img.SetRGBA(x, y, color.RGBA{R: c.R ^ n, G: c.G ^ n, B: c.B ^ n, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func buildGIF() []byte {
	img := image.NewPaletted(image.Rect(0, 0, 128, 128), palette.Plan9)
	for y := range 128 {
		for x := range 128 {
			img.SetColorIndex(x, y, uint8((x/8+y/8)%len(palette.Plan9)))
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func newOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	f := loadFixtures()

	mux := http.NewServeMux()
	mux.HandleFunc("/status/200", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	serve := func(contentType string, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("/html", serve("text/html; charset=utf-8", f.html))
	mux.HandleFunc("/image/png", serve("image/png", f.png))
	mux.HandleFunc("/image/jpeg", serve("image/jpeg", f.jpeg))
	mux.HandleFunc("/image/gif", serve("image/gif", f.gif))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
