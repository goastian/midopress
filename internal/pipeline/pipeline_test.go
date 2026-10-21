package pipeline

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"image"
	"image/color"
	"image/color/palette"
	stdgif "image/gif"
	stdjpeg "image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chai2010/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
)

var testCodec = codec.NewText(codec.Levels{Brotli: 5, Gzip: 6, Zstd: 3})

func htmlBody(n int) []byte {
	return []byte("<!DOCTYPE html>\n<html>\n  <body>\n" +
		strings.Repeat("    <p>Call me Ishmael. Some years ago, never mind how long precisely.</p>\n", n) +
		"  </body>\n</html>\n")
}

func noisy(w, h int) *image.RGBA {
	rng := rand.New(rand.NewPCG(3, 4))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := uint8(rng.IntN(48))
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*255/w) ^ n, G: uint8(y*255/h) ^ n, B: n * 3, A: 255})
		}
	}
	return img
}

func jpegBytes(t testing.TB, w, h, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, stdjpeg.Encode(&b, noisy(w, h), &stdjpeg.Options{Quality: q}))
	return b.Bytes()
}

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func noisyPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, noisy(w, h)))
	return b.Bytes()
}

func gifBytes(t testing.TB, frames int) []byte {
	t.Helper()
	g := &stdgif.GIF{}
	for f := range frames {
		img := image.NewPaletted(image.Rect(0, 0, 96, 96), palette.Plan9)
		for i := range img.Pix {
			img.Pix[i] = uint8(i/96 + f*30)
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 5)
	}
	var b bytes.Buffer
	require.NoError(t, stdgif.EncodeAll(&b, g))
	return b.Bytes()
}

func compress(t testing.TB, enc codec.Encoding, src []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, testCodec.Compress(enc, &b, src))
	return b.Bytes()
}

func decode(t testing.TB, enc codec.Encoding, src []byte) []byte {
	t.Helper()
	if enc == codec.None {
		return src
	}
	var b bytes.Buffer
	require.NoError(t, testCodec.Decode(enc, src, &b, 64<<20))
	return b.Bytes()
}

type respOpt func(*http.Response)

func newResp(contentType string, body []byte, opts ...respOpt) *http.Response {
	r := &http.Response{
		StatusCode:    200,
		Header:        http.Header{"Content-Type": {contentType}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	for _, o := range opts {
		o(r)
	}
	return r
}

func withHeader(k, v string) respOpt { return func(r *http.Response) { r.Header.Set(k, v) } }
func withStatus(s int) respOpt       { return func(r *http.Response) { r.StatusCode = s } }
func withUnknownLength() respOpt {
	return func(r *http.Response) {
		r.ContentLength = -1
		r.Header.Del("Content-Length")
	}
}

func newReq(headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.test/x", nil)
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func run(p *Pipeline, req *http.Request, resp *http.Response) (*httptest.ResponseRecorder, Outcome) {
	rec := httptest.NewRecorder()
	out := p.Handle(rec, req, resp)
	return rec, out
}

func newPipeline(mod ...func(*Config)) *Pipeline {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 4
	for _, m := range mod {
		m(&cfg)
	}
	return New(cfg)
}

func TestPassthroughDecisions(t *testing.T) {
	body := htmlBody(100)
	tests := []struct {
		name   string
		req    func() *http.Request
		resp   func() *http.Response
		reason Reason
	}{
		{"HEAD", func() *http.Request { r := newReq("Accept-Encoding", "gzip"); r.Method = http.MethodHead; return r },
			func() *http.Response { return newResp("text/html", body) }, PassMethod},
		{"PUT", func() *http.Request { r := newReq("Accept-Encoding", "gzip"); r.Method = http.MethodPut; return r },
			func() *http.Response { return newResp("text/html", body) }, PassMethod},
		{"404", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/html", body, withStatus(404)) }, PassStatus},
		{"206", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/html", body, withStatus(206)) }, PassStatus},
		{"304", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/html", nil, withStatus(304)) }, PassStatus},
		{"range request", func() *http.Request { return newReq("Accept-Encoding", "gzip", "Range", "bytes=0-10") },
			func() *http.Response { return newResp("text/html", body) }, PassRange},
		{"content-range", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/html", body, withHeader("Content-Range", "bytes 0-9/10")) }, PassRange},
		{"no-transform response", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response {
				return newResp("text/html", body, withHeader("Cache-Control", "public, no-transform"))
			}, PassNoTransform},
		{"no-transform request", func() *http.Request { return newReq("Accept-Encoding", "gzip", "Cache-Control", "no-transform") },
			func() *http.Response { return newResp("text/html", body) }, PassNoTransform},
		{"event stream", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/event-stream", body) }, PassStream},
		{"pdf", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("application/pdf", body) }, PassType},
		{"octet-stream", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("application/octet-stream", body) }, PassType},
		{"no content type", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("", body) }, PassType},
		{"unknown encoding", func() *http.Request { return newReq("Accept-Encoding", "gzip, br") },
			func() *http.Response { return newResp("text/html", body, withHeader("Content-Encoding", "compress")) }, PassEncoding},
		{"chained encoding", func() *http.Request { return newReq("Accept-Encoding", "gzip, br") },
			func() *http.Response { return newResp("text/html", body, withHeader("Content-Encoding", "gzip, br")) }, PassEncoding},
		{"client accepts no coding", func() *http.Request { return newReq() },
			func() *http.Response { return newResp("text/html", body) }, PassNoCodec},
		{"identity only client", func() *http.Request { return newReq("Accept-Encoding", "identity") },
			func() *http.Response { return newResp("text/html", body) }, PassNoCodec},
		{"too small", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/html", []byte("<p>hi</p>")) }, PassTooSmall},
		{"empty body", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response { return newResp("text/html", nil) }, PassTooSmall},
		{"already gzip, client gzip", func() *http.Request { return newReq("Accept-Encoding", "gzip") },
			func() *http.Response {
				return newResp("text/html", compress(t, codec.Gzip, body), withHeader("Content-Encoding", "gzip"))
			}, PassAlreadyCompressed},
		{"already br", func() *http.Request { return newReq("Accept-Encoding", "gzip, br") },
			func() *http.Response {
				return newResp("text/html", compress(t, codec.Brotli, body), withHeader("Content-Encoding", "br"))
			}, PassAlreadyCompressed},
		{"already zstd, client zstd", func() *http.Request { return newReq("Accept-Encoding", "gzip, zstd") },
			func() *http.Response {
				return newResp("text/html", compress(t, codec.Zstd, body), withHeader("Content-Encoding", "zstd"))
			}, PassAlreadyCompressed},
	}
	p := newPipeline()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := tt.resp()
			wantHeader := resp.Header.Clone()
			wantBody, _ := io.ReadAll(tt.resp().Body)

			rec, out := run(p, tt.req(), resp)

			assert.Equal(t, tt.reason, out.Reason)
			assert.Equal(t, string(wantBody), rec.Body.String(), "body must be untouched")
			assert.Equal(t, resp.StatusCode, rec.Code)
			for k, v := range wantHeader {
				assert.Equal(t, v, rec.Header()[k], "header %s must be untouched", k)
			}
			assert.NoError(t, out.Err)
		})
	}
}

func TestTextCompression(t *testing.T) {
	body := htmlBody(400)
	tests := []struct {
		name       string
		acceptEnc  string
		wantEnc    codec.Encoding
		wantHeader string
	}{
		{"gzip", "gzip", codec.Gzip, "gzip"},
		{"brotli preferred", "gzip, deflate, br", codec.Brotli, "br"},
		{"no spaces after comma", "gzip,deflate,br", codec.Brotli, "br"},
		{"firefox https", "gzip, deflate, br, zstd", codec.Brotli, "br"},
		{"zstd without br", "gzip, deflate, zstd", codec.Zstd, "zstd"},
		{"q=0 br is skipped", "br;q=0, gzip", codec.Gzip, "gzip"},
	}
	p := newPipeline()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := newResp("text/html; charset=utf-8", body,
				withHeader("ETag", `"v1"`), withHeader("Content-MD5", "abc"), withHeader("Accept-Ranges", "bytes"))
			rec, out := run(p, newReq("Accept-Encoding", tt.acceptEnc), resp)

			require.Equal(t, Transformed, out.Reason)
			assert.Equal(t, tt.wantHeader, rec.Header().Get("Content-Encoding"))
			assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
			assert.Equal(t, `W/"v1"`, rec.Header().Get("Etag"))
			assert.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding")
			assert.Empty(t, rec.Header().Get("Content-Md5"))
			assert.Empty(t, rec.Header().Get("Accept-Ranges"))
			assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			assert.Less(t, rec.Body.Len(), len(body)/5)
			assert.Equal(t, body, decode(t, tt.wantEnc, rec.Body.Bytes()))
			assert.Equal(t, int64(len(body)), out.BytesIn)
			assert.Equal(t, int64(rec.Body.Len()), out.BytesOut)
		})
	}
}

func TestTextTypes(t *testing.T) {
	body := []byte(strings.Repeat(`{"key":"value","list":[1,2,3,4,5]}`, 100))
	p := newPipeline()
	for _, ct := range []string{
		"text/html", "text/css", "text/javascript", "application/javascript", "application/json",
		"application/ld+json", "application/manifest+json", "application/xml", "text/xml", "image/svg+xml",
		"application/rss+xml", "text/plain", "text/csv", "application/wasm", "font/ttf", "image/x-icon",
		"APPLICATION/JSON; charset=utf-8",
	} {
		rec, out := run(p, newReq("Accept-Encoding", "gzip"), newResp(ct, body))
		assert.Equal(t, Transformed, out.Reason, ct)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), ct)
	}
}

func TestRecompress(t *testing.T) {
	body := htmlBody(400)
	for _, srcEnc := range []codec.Encoding{codec.Gzip, codec.Zstd} {
		t.Run(srcEnc.Token(), func(t *testing.T) {
			p := newPipeline()
			wire := compress(t, srcEnc, body)
			rec, out := run(p, newReq("Accept-Encoding", "gzip, br, zstd"),
				newResp("text/html", wire, withHeader("Content-Encoding", srcEnc.Token())))
			require.Equal(t, Transformed, out.Reason)
			assert.Equal(t, "br", rec.Header().Get("Content-Encoding"))
			assert.Equal(t, body, decode(t, codec.Brotli, rec.Body.Bytes()))
		})
	}

	t.Run("deflate zlib and raw", func(t *testing.T) {
		var zl, raw bytes.Buffer
		zw := zlib.NewWriter(&zl)
		_, _ = zw.Write(body)
		require.NoError(t, zw.Close())
		fw, _ := flate.NewWriter(&raw, flate.DefaultCompression)
		_, _ = fw.Write(body)
		require.NoError(t, fw.Close())

		for name, wire := range map[string][]byte{"zlib": zl.Bytes(), "raw": raw.Bytes()} {
			rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip, deflate, br"),
				newResp("text/html", wire, withHeader("Content-Encoding", "deflate")))
			require.Equal(t, Transformed, out.Reason, name)
			assert.Equal(t, body, decode(t, codec.Brotli, rec.Body.Bytes()), name)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Text.Recompress = false })
		_, out := run(p, newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", compress(t, codec.Gzip, body), withHeader("Content-Encoding", "gzip")))
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
	})
}

func TestTextFallbacks(t *testing.T) {
	t.Run("incompressible data is sent as is", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 1))
		noise := make([]byte, 4000)
		for i := range noise {
			noise[i] = byte(rng.IntN(256))
		}
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip"), newResp("text/plain", noise))
		assert.Equal(t, PassNotSmaller, out.Reason)
		assert.Equal(t, noise, rec.Body.Bytes())
		assert.Empty(t, rec.Header().Get("Content-Encoding"))
	})

	t.Run("corrupt gzip body is relayed byte for byte", func(t *testing.T) {
		wire := append([]byte{0x1f, 0x8b, 0x08, 0x00}, bytes.Repeat([]byte("garbage"), 100)...)
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", wire, withHeader("Content-Encoding", "gzip")))
		assert.Equal(t, PassError, out.Reason)
		assert.Equal(t, wire, rec.Body.Bytes())
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	})

	t.Run("decompression bomb stays under the expansion cap", func(t *testing.T) {
		var bomb bytes.Buffer
		require.NoError(t, testCodec.Compress(codec.Gzip, &bomb, make([]byte, 8<<20)))
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", bomb.Bytes(), withHeader("Content-Encoding", "gzip")))
		assert.Equal(t, PassTooLarge, out.Reason)
		assert.Equal(t, bomb.Bytes(), rec.Body.Bytes())
	})

	t.Run("decompression bomb over the configured limit", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Text.MaxDecoded = 1000 })
		wire := compress(t, codec.Gzip, htmlBody(2000))
		rec, out := run(p, newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", wire, withHeader("Content-Encoding", "gzip")))
		assert.Equal(t, PassTooLarge, out.Reason)
		assert.Equal(t, wire, rec.Body.Bytes())
	})

	t.Run("truncated upstream body before commit", func(t *testing.T) {
		resp := newResp("text/html", htmlBody(100))
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(htmlBody(100)[:100]), errReaderOnce{errors.New("upstream reset")}))
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip"), resp)
		assert.Error(t, out.Err)
		assert.False(t, out.Written, "nothing may reach the client so the caller can send a 502")
		assert.Zero(t, rec.Body.Len())
	})

	t.Run("memory budget exhausted", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.MemoryBudget = 100 })
		body := htmlBody(100)
		rec, out := run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", body))
		assert.Equal(t, PassBusy, out.Reason)
		assert.Equal(t, body, rec.Body.Bytes())
	})
}

type errReaderOnce struct{ err error }

func (e errReaderOnce) Read([]byte) (int, error) { return 0, e.err }

func TestTextStreaming(t *testing.T) {
	body := htmlBody(5000)

	t.Run("unknown length", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip, br"), newResp("text/html", body, withUnknownLength()))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "br", rec.Header().Get("Content-Encoding"))
		assert.Empty(t, rec.Header().Get("Content-Length"))
		assert.True(t, rec.Flushed, "chunked responses must be flushed as they arrive")
		assert.Equal(t, body, decode(t, codec.Brotli, rec.Body.Bytes()))
	})

	t.Run("larger than the buffer limit", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Text.BufferMax = 4096 })
		rec, out := run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", body))
		require.Equal(t, Transformed, out.Reason)
		assert.Empty(t, rec.Header().Get("Content-Length"))
		assert.Equal(t, body, decode(t, codec.Gzip, rec.Body.Bytes()))
	})

	t.Run("a large gzip body is not decoded", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Text.BufferMax = 1024 })
		wire := compress(t, codec.Gzip, body)
		rec, out := run(p, newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", wire, withHeader("Content-Encoding", "gzip")))
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
		assert.Equal(t, wire, rec.Body.Bytes())
	})

	t.Run("an encoded body of unknown length is not decoded", func(t *testing.T) {
		wire := compress(t, codec.Gzip, body)
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", wire, withHeader("Content-Encoding", "gzip"), withUnknownLength()))
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
		assert.Equal(t, wire, rec.Body.Bytes())
	})

	t.Run("a decompression bomb of unknown length costs nothing", func(t *testing.T) {
		var bomb bytes.Buffer
		require.NoError(t, testCodec.Compress(codec.Gzip, &bomb, make([]byte, 64<<20)))
		start := time.Now()
		rec, out := run(newPipeline(), newReq("Accept-Encoding", "gzip, br"),
			newResp("text/html", bomb.Bytes(), withHeader("Content-Encoding", "gzip"), withUnknownLength()))
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
		assert.Equal(t, bomb.Bytes(), rec.Body.Bytes())
		assert.Less(t, time.Since(start), 500*time.Millisecond)
	})

	t.Run("upstream failure after commit", func(t *testing.T) {
		resp := newResp("text/html", nil, withUnknownLength())
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body[:2000]), errReaderOnce{errors.New("reset")}))
		_, out := run(newPipeline(), newReq("Accept-Encoding", "gzip"), resp)
		assert.True(t, out.Written)
		assert.Error(t, out.Err, "the proxy must abort the client connection")
	})
}

func TestMinify(t *testing.T) {
	src := []byte("<!DOCTYPE html>\n<html>\n  <head>\n    <title>x</title>\n  </head>\n  <body>\n" +
		strings.Repeat("    <p>   hello     world   </p>\n\n", 200) + "  </body>\n</html>\n")
	p := newPipeline(func(c *Config) { c.Text.Minify = true })

	rec, out := run(p, newReq("Accept-Encoding", "identity"), newResp("text/html", src))
	require.Equal(t, Transformed, out.Reason)
	assert.Less(t, rec.Body.Len(), len(src))
	assert.Contains(t, rec.Body.String(), "hello")
	assert.Empty(t, rec.Header().Get("Content-Encoding"))

	rec, out = run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", src))
	require.Equal(t, Transformed, out.Reason)
	plain := decode(t, codec.Gzip, rec.Body.Bytes())
	assert.Less(t, len(plain), len(src))

	rec, out = run(p, newReq("Accept-Encoding", "gzip"), newResp("application/json", src))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, src, decode(t, codec.Gzip, rec.Body.Bytes()), "json is never minified")
}

func TestPassthroughTrailersAndStreamingFlush(t *testing.T) {
	resp := newResp("application/grpc", []byte("payload"), withUnknownLength())
	resp.Trailer = http.Header{"Grpc-Status": {"0"}}
	rec, out := run(newPipeline(), newReq(), resp)
	assert.Equal(t, PassType, out.Reason)
	assert.True(t, rec.Flushed)
	assert.Equal(t, "payload", rec.Body.String())
	assert.Equal(t, []string{"0"}, rec.Header()[http.TrailerPrefix+"Grpc-Status"])
}

func TestPassthroughUpstreamErrorMidBody(t *testing.T) {
	resp := newResp("application/octet-stream", nil)
	resp.Body = io.NopCloser(io.MultiReader(strings.NewReader("partial"), errReaderOnce{errors.New("boom")}))
	resp.ContentLength = 100
	_, out := run(newPipeline(), newReq(), resp)
	assert.Error(t, out.Err)
	assert.True(t, out.Written)
}

func TestImages(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)

	t.Run("jpeg is recompressed", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq(), newResp("image/jpeg", jpg, withHeader("ETag", `"j"`)))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
		assert.Less(t, rec.Body.Len(), len(jpg))
		assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
		assert.Equal(t, `W/"j"`, rec.Header().Get("Etag"))
		assert.Contains(t, rec.Header().Values("Vary"), "Accept")
		_, err := stdjpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
		assert.NoError(t, err)
	})

	t.Run("image/jpg alias", func(t *testing.T) {
		_, out := run(newPipeline(), newReq(), newResp("image/jpg", jpg))
		assert.Equal(t, Transformed, out.Reason)
	})

	t.Run("quality headers", func(t *testing.T) {
		size := func(headers ...string) int {
			rec, out := run(newPipeline(), newReq(headers...), newResp("image/jpeg", jpg))
			require.Equal(t, Transformed, out.Reason)
			return rec.Body.Len()
		}
		low, high := size("X-MidoPress-Quality", "10"), size("X-MidoPress-Quality", "90")
		assert.Less(t, low, high)
		assert.Equal(t, low, size("X-Compy-Quality", "10"), "legacy header still works")
		assert.Equal(t, size(), size("X-MidoPress-Quality", "nonsense"), "invalid value falls back to the default")
		assert.Equal(t, size(), size("X-MidoPress-Quality", "0"))
		assert.Equal(t, size(), size("X-MidoPress-Quality", "500"))
	})

	t.Run("webp when accepted", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq("Accept", "image/avif,image/webp,*/*"), newResp("image/jpeg", jpg))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
		_, err := webp.Decode(bytes.NewReader(rec.Body.Bytes()))
		assert.NoError(t, err)
	})

	t.Run("png to webp", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq("Accept", "image/webp"), newResp("image/png", pngBytes(t, 300, 300)))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
	})

	t.Run("static gif to webp", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq("Accept", "image/webp"), newResp("image/gif", gifBytes(t, 1)))
		if out.Reason == Transformed {
			assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
		} else {
			assert.Equal(t, PassNotSmaller, out.Reason)
		}
	})

	t.Run("animated gif is kept", func(t *testing.T) {
		g := gifBytes(t, 4)
		rec, out := run(newPipeline(), newReq("Accept", "image/webp"), newResp("image/gif", g))
		assert.Equal(t, PassAnimated, out.Reason)
		assert.Equal(t, g, rec.Body.Bytes())
		assert.Equal(t, "image/gif", rec.Header().Get("Content-Type"))
	})

	t.Run("detected format wins over the declared one", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq("Accept", "image/webp"), newResp("image/jpeg", pngBytes(t, 300, 300)))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
	})

	t.Run("not an image at all", func(t *testing.T) {
		junk := bytes.Repeat([]byte("<html>this is not a jpeg</html>"), 100)
		rec, out := run(newPipeline(), newReq(), newResp("image/jpeg", junk))
		assert.Equal(t, PassType, out.Reason)
		assert.Equal(t, junk, rec.Body.Bytes())
	})

	t.Run("corrupt jpeg is relayed", func(t *testing.T) {
		bad := append(append([]byte{}, jpg[:30]...), bytes.Repeat([]byte{0xAB}, 3000)...)
		rec, out := run(newPipeline(), newReq(), newResp("image/jpeg", bad))
		assert.Equal(t, PassError, out.Reason)
		assert.Equal(t, bad, rec.Body.Bytes())
	})

	t.Run("already small and optimized", func(t *testing.T) {
		first, out := run(newPipeline(), newReq("X-MidoPress-Quality", "20"), newResp("image/jpeg", jpg))
		require.Equal(t, Transformed, out.Reason)
		already := first.Body.Bytes()

		rec, out := run(newPipeline(), newReq("X-MidoPress-Quality", "90"), newResp("image/jpeg", already))
		assert.Equal(t, PassNotSmaller, out.Reason)
		assert.Equal(t, already, rec.Body.Bytes())
	})

	t.Run("too many pixels", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Image.MaxPixels = 1000 })
		rec, out := run(p, newReq(), newResp("image/jpeg", jpg))
		assert.Equal(t, PassTooLarge, out.Reason)
		assert.Equal(t, jpg, rec.Body.Bytes())
	})

	t.Run("too many bytes with known length", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Image.MaxBytes = 1000 })
		rec, out := run(p, newReq(), newResp("image/jpeg", jpg))
		assert.Equal(t, PassTooLarge, out.Reason)
		assert.Equal(t, jpg, rec.Body.Bytes())
	})

	t.Run("too many bytes with unknown length keeps the whole body", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Image.MaxBytes = 5000 })
		rec, out := run(p, newReq(), newResp("image/jpeg", jpg, withUnknownLength()))
		assert.Equal(t, PassTooLarge, out.Reason)
		assert.Equal(t, jpg, rec.Body.Bytes(), "bytes read while probing the size must not be lost")
		assert.Equal(t, int64(len(jpg)), out.BytesIn)
	})

	t.Run("disabled formats", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Image.JPEGQuality = 0; c.Image.PNG = false; c.Image.GIF = false })
		for ct, body := range map[string][]byte{"image/jpeg": jpg, "image/png": pngBytes(t, 200, 200), "image/gif": gifBytes(t, 1)} {
			rec, out := run(p, newReq(), newResp(ct, body))
			assert.Equal(t, PassDisabled, out.Reason, ct)
			assert.Equal(t, body, rec.Body.Bytes(), ct)
		}
	})

	t.Run("range request", func(t *testing.T) {
		rec, out := run(newPipeline(), newReq("Range", "bytes=0-99"), newResp("image/jpeg", jpg[:100], withStatus(206)))
		assert.Equal(t, PassStatus, out.Reason)
		assert.Equal(t, 206, rec.Code)
	})

	t.Run("gzip encoded png is decoded first", func(t *testing.T) {
		src := noisyPNG(t, 200, 200)
		rec, out := run(newPipeline(), newReq("Accept", "image/webp", "Accept-Encoding", "gzip"),
			newResp("image/png", compress(t, codec.Gzip, src), withHeader("Content-Encoding", "gzip")))
		require.Equal(t, Transformed, out.Reason)
		assert.Empty(t, rec.Header().Get("Content-Encoding"))
		assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
	})

	t.Run("too small", func(t *testing.T) {
		_, out := run(newPipeline(), newReq(), newResp("image/png", []byte("\x89PNG\r\n\x1a\n")))
		assert.Equal(t, PassTooSmall, out.Reason)
	})
}

func TestImageLoadShedding(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)

	t.Run("no free slot", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.MaxConcurrent = 1; c.TranscodeWait = 5 * time.Millisecond })
		require.True(t, p.acquireCPU())
		defer p.releaseCPU()
		rec, out := run(p, newReq(), newResp("image/jpeg", jpg))
		assert.Equal(t, PassBusy, out.Reason)
		assert.Equal(t, jpg, rec.Body.Bytes())
	})

	t.Run("deadline", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Image.Timeout = time.Nanosecond })
		big := jpegBytes(t, 1600, 1200, 95)
		rec, out := run(p, newReq(), newResp("image/jpeg", big))
		assert.Equal(t, PassTimeout, out.Reason)
		assert.Equal(t, big, rec.Body.Bytes())
	})

	t.Run("slot is released after a timeout", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.MaxConcurrent = 1; c.Image.Timeout = time.Nanosecond })
		run(p, newReq(), newResp("image/jpeg", jpegBytes(t, 800, 600, 95)))
		assert.Eventually(t, func() bool {
			if p.acquireCPU() {
				p.releaseCPU()
				return true
			}
			return false
		}, 5*time.Second, 10*time.Millisecond)
	})
}

func TestConcurrentMinify(t *testing.T) {
	p := newPipeline(func(c *Config) { c.Text.Minify = true })
	src := []byte("<!DOCTYPE html>\n<html>\n  <body>\n" + strings.Repeat("    <p>  Ishmael   </p>\n", 300) + "  </body>\n</html>\n")
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				rec, out := run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", src))
				if assert.Equal(t, Transformed, out.Reason) {
					assert.Contains(t, string(decode(t, codec.Gzip, rec.Body.Bytes())), "Ishmael")
				}
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentMixedTraffic(t *testing.T) {
	p := newPipeline()
	html := htmlBody(300)
	jpg := jpegBytes(t, 200, 150, 95)
	pn := noisyPNG(t, 200, 200)

	var wg sync.WaitGroup
	for g := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				switch (g + i) % 4 {
				case 0:
					rec, out := run(p, newReq("Accept-Encoding", "gzip, br"), newResp("text/html", html))
					if assert.Equal(t, Transformed, out.Reason) {
						assert.Equal(t, html, decode(t, codec.Brotli, rec.Body.Bytes()))
					}
				case 1:
					q := strconv.Itoa(10 + (g*7+i)%80)
					rec, out := run(p, newReq("X-MidoPress-Quality", q), newResp("image/jpeg", jpg))
					assert.Contains(t, []Reason{Transformed, PassNotSmaller, PassBusy}, out.Reason)
					assert.NotZero(t, rec.Body.Len())
				case 2:
					rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/png", pn))
					assert.Contains(t, []Reason{Transformed, PassNotSmaller, PassBusy}, out.Reason)
					assert.NotZero(t, rec.Body.Len())
				default:
					rec, out := run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", html, withUnknownLength()))
					if assert.Equal(t, Transformed, out.Reason) {
						assert.Equal(t, html, decode(t, codec.Gzip, rec.Body.Bytes()))
					}
				}
			}
		}()
	}
	wg.Wait()

	var total uint64
	for _, n := range p.Stats() {
		total += n
	}
	assert.Equal(t, uint64(24*20), total)
}

func TestQualityHeaderDoesNotLeakBetweenRequests(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)
	p := newPipeline(func(c *Config) { c.MaxConcurrent = 8 })
	expected := func(q string) int {
		rec, _ := run(p, newReq("X-MidoPress-Quality", q), newResp("image/jpeg", jpg))
		return rec.Body.Len()
	}
	low, high, def := expected("10"), expected("90"), expected("50")
	require.Less(t, low, high)

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, want := "10", low
			switch g % 3 {
			case 1:
				q, want = "90", high
			case 2:
				q, want = "50", def
			}
			for range 10 {
				rec, _ := run(p, newReq("X-MidoPress-Quality", q), newResp("image/jpeg", jpg))
				assert.Equal(t, want, rec.Body.Len(), "quality %s", q)
			}
		}()
	}
	wg.Wait()
}

func TestReasonString(t *testing.T) {
	for r := Reason(0); r < numReasons; r++ {
		assert.NotEqual(t, "unknown", r.String())
	}
	assert.Equal(t, "unknown", numReasons.String())
}

func BenchmarkTextBuffered(b *testing.B) {
	body := htmlBody(300)
	p := newPipeline()
	req := newReq("Accept-Encoding", "gzip, br")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			p.Handle(rec, req, newResp("text/html", body))
		}
	})
}

func BenchmarkPassthrough(b *testing.B) {
	body := bytes.Repeat([]byte("x"), 32<<10)
	p := newPipeline()
	req := newReq("Accept-Encoding", "gzip")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			p.Handle(rec, req, newResp("application/octet-stream", body))
		}
	})
}

func BenchmarkJPEG(b *testing.B) {
	jpg := jpegBytes(b, 800, 600, 90)
	p := newPipeline()
	req := newReq()
	b.SetBytes(int64(len(jpg)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			p.Handle(rec, req, newResp("image/jpeg", jpg))
		}
	})
}
