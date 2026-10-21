package server_test

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/negotiate"
)

type corpusEntry struct {
	URL     string
	Host    string
	Key     string // host + request URI
	Status  int
	Headers http.Header
	Body    []byte // as received from the origin, possibly still encoded
}

func loadCorpus(t *testing.T) []corpusEntry {
	t.Helper()
	data, err := os.ReadFile("../../testdata/corpus/corpus.har")
	require.NoError(t, err)
	var doc struct {
		Log struct {
			Entries []struct {
				Request struct {
					URL string `json:"url"`
				} `json:"request"`
				Response struct {
					Status  int `json:"status"`
					Headers []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"headers"`
					Content struct {
						Text     string `json:"text"`
						Encoding string `json:"encoding"`
					} `json:"content"`
				} `json:"response"`
			} `json:"entries"`
		} `json:"log"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	var out []corpusEntry
	for _, e := range doc.Log.Entries {
		u, err := url.Parse(e.Request.URL)
		require.NoError(t, err)
		body, err := base64.StdEncoding.DecodeString(e.Response.Content.Text)
		require.NoError(t, err)
		h := http.Header{}
		for _, nv := range e.Response.Headers {
			h.Add(nv.Name, nv.Value)
		}
		out = append(out, corpusEntry{
			URL: e.Request.URL, Host: u.Hostname(), Key: u.Hostname() + u.RequestURI(),
			Status: e.Response.Status, Headers: h, Body: body,
		})
	}
	return out
}

// replayOrigin serves the recorded responses the way a real origin would: it
// sends the body in the recorded encoding when the request accepts it, and
// decoded otherwise.
type replayOrigin struct {
	entries map[string]corpusEntry
	text    *codec.Text

	mu   sync.Mutex
	sent map[string]int // key -> bytes of body sent
	h2   int            // requests that arrived over HTTP/2
}

func (o *replayOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	key := host + r.URL.RequestURI()
	e, ok := o.entries[key]
	if !ok {
		http.NotFound(w, r)
		return
	}

	body := e.Body
	enc, _ := codec.ParseEncoding(e.Headers.Get("Content-Encoding"))
	accepted := negotiate.ParseAcceptEncoding(r.Header.Get("Accept-Encoding"))
	switch {
	case enc == codec.None:
	case enc == codec.Gzip && accepted.Has(negotiate.Gzip),
		enc == codec.Brotli && accepted.Has(negotiate.Brotli),
		enc == codec.Zstd && accepted.Has(negotiate.Zstd),
		enc == codec.Deflate && accepted.Has(negotiate.Deflate):
	default:
		var plain bytes.Buffer
		if err := o.text.Decode(enc, e.Body, &plain, 64<<20); err != nil {
			http.Error(w, "replay: "+err.Error(), http.StatusInternalServerError)
			return
		}
		body, enc = plain.Bytes(), codec.None
	}

	for k, vals := range e.Headers {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Content-Encoding", "Alt-Svc":
			continue
		}
		w.Header()[http.CanonicalHeaderKey(k)] = vals
	}
	if enc != codec.None {
		w.Header().Set("Content-Encoding", enc.Token())
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)

	o.mu.Lock()
	o.sent[key] = len(body)
	if r.ProtoMajor == 2 {
		o.h2++
	}
	o.mu.Unlock()
}

func startReplayOrigin(t *testing.T, entries []corpusEntry) (*replayOrigin, *httptest.Server, *x509.CertPool) {
	t.Helper()
	o := &replayOrigin{entries: map[string]corpusEntry{}, text: codec.NewText(codec.Levels{Brotli: 5, Gzip: 6, Zstd: 3}), sent: map[string]int{}}
	for _, e := range entries {
		o.entries[e.Key] = e
	}

	originCA, err := mitm.GenerateCA("replay origin CA", 24*time.Hour)
	require.NoError(t, err)
	cfg := mitm.NewIssuer(originCA, mitm.IssuerOptions{}).TLSConfig(nil)
	cfg.NextProtos = []string{"h2", "http/1.1"}

	srv := httptest.NewUnstartedServer(o)
	srv.EnableHTTP2 = true
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(originCA.Cert)
	return o, srv, pool
}

func mediaType(h http.Header) string {
	v := h.Get("Content-Type")
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// TestRealWorldCorpus replays real responses, recorded from real origins with
// tools/record-corpus, through the proxy with HTTPS interception on. No page
// may come out different in a way that matters: text must be identical once
// decoded, and images must still decode and keep their size.
func TestRealWorldCorpus(t *testing.T) {
	entries := loadCorpus(t)
	require.GreaterOrEqual(t, len(entries), 30, "the corpus must have at least 30 real URLs")

	origin, srv, pool := startReplayOrigin(t, entries)
	// the default passthrough list has its own test; here everything is intercepted
	h := newHarness(t, withOrigin(srv, pool), withConfig(func(c *configT) { c.MITM.DefaultPassthrough = false }))
	text := codec.NewText(codec.Levels{Brotli: 5, Gzip: 6, Zstd: 3})

	profiles := []struct {
		name, accept, encoding string
		bounded                bool // the proxy may never send more bytes than the origin did
	}{
		{"firefox", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8", "gzip, deflate, br, zstd", true},
		{"older browser", "*/*", "gzip", true},
		{"no encodings", "*/*", "", false},
	}

	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			c := h.client()
			var totalIn, totalOut int
			var imgIn, imgOut int
			var recoded, compressed int

			for _, e := range entries {
				req, err := http.NewRequest(http.MethodGet, e.URL, nil)
				require.NoError(t, err)
				req.Header.Set("Accept", p.accept)
				if p.encoding != "" {
					req.Header.Set("Accept-Encoding", p.encoding)
				}
				resp, err := c.Do(req)
				require.NoError(t, err, e.URL)
				got, err := readAll(resp)
				require.NoError(t, err, e.URL)

				assert.Equal(t, e.Status, resp.StatusCode, e.URL)
				if resp.ContentLength >= 0 {
					assert.Equal(t, int(resp.ContentLength), len(got), "Content-Length must match the body: %s", e.URL)
				}

				// what the origin would have sent to this client, decoded
				wantEnc, _ := codec.ParseEncoding(e.Headers.Get("Content-Encoding"))
				var want bytes.Buffer
				require.NoError(t, text.Decode(wantEnc, e.Body, &want, 64<<20), e.URL)

				gotEnc, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
				require.True(t, ok, "%s: unusable Content-Encoding %q", e.URL, resp.Header.Get("Content-Encoding"))
				var plain bytes.Buffer
				require.NoError(t, text.Decode(gotEnc, got, &plain, 64<<20), "%s: the body must decode with the declared encoding", e.URL)

				origin.mu.Lock()
				in := origin.sent[e.Key]
				origin.mu.Unlock()
				totalIn += in
				totalOut += len(got)
				if strings.HasPrefix(mediaType(e.Headers), "image/") {
					imgIn += in
					imgOut += len(got)
				}
				if gotEnc != codec.None && gotEnc != wantEnc {
					compressed++
				}

				switch mt := mediaType(resp.Header); {
				case (mt == "image/webp" || mt == "image/avif" || mt == "image/jxl") && mediaType(e.Headers) != mt:
					recoded++
					assertSameSize(t, e, want.Bytes(), plain.Bytes(), mt)
				case strings.HasPrefix(mt, "image/") && !bytes.Equal(plain.Bytes(), want.Bytes()):
					recoded++
					assertSameSize(t, e, want.Bytes(), plain.Bytes(), "")
					assert.Equal(t, mediaType(e.Headers), mt, e.URL)
				default:
					assert.True(t, bytes.Equal(plain.Bytes(), want.Bytes()), "%s: the decoded body differs from the origin's", e.URL)
					assert.Equal(t, mediaType(e.Headers), mt, e.URL)
				}

				if p.bounded {
					assert.LessOrEqual(t, len(got), in, "%s: the proxy must never send more than it received", e.URL)
				}
				if strings.Contains(e.Headers.Get("Cache-Control"), "no-transform") {
					assert.Equal(t, in, len(got), "%s: no-transform must be respected", e.URL)
				}
			}

			saved := 100 - float64(totalOut)*100/float64(totalIn)
			t.Logf("%d responses: origin sent %d bytes, client received %d (%.1f%% less); %d recompressed, %d images recoded",
				len(entries), totalIn, totalOut, saved, compressed, recoded)
			t.Logf("  of which text and other lossless: %d -> %d (%.1f%% less); images: %d -> %d (%.1f%% less)",
				totalIn-imgIn, totalOut-imgOut, 100-float64(totalOut-imgOut)*100/float64(totalIn-imgIn),
				imgIn, imgOut, 100-float64(imgOut)*100/float64(imgIn))
			if p.bounded {
				assert.LessOrEqual(t, totalOut, totalIn)
			}
		})
	}
}

func TestUpstreamUsesHTTP2(t *testing.T) {
	entries := loadCorpus(t)[:3]
	origin, srv, pool := startReplayOrigin(t, entries)
	h := newHarness(t, withOrigin(srv, pool), withConfig(func(c *configT) { c.MITM.DefaultPassthrough = false }))
	for _, e := range entries {
		resp, err := h.client().Get(e.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	assert.Equal(t, len(entries), origin.h2, "the proxy must speak HTTP/2 to origins that support it")
}

func readAll(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	_, err := b.ReadFrom(resp.Body)
	return b.Bytes(), err
}

// assertSameSize checks that a recoded image decodes and has the dimensions of
// the original.
func assertSameSize(t *testing.T, e corpusEntry, orig, recoded []byte, modern string) {
	t.Helper()
	oc, _, err := image.DecodeConfig(bytes.NewReader(orig))
	require.NoError(t, err, e.URL)
	var got image.Image
	if modern != "" {
		got, err = decodeModern(modern, recoded)
	} else {
		got, _, err = image.Decode(bytes.NewReader(recoded))
	}
	require.NoError(t, err, "%s: the recoded image must decode", e.URL)
	b := got.Bounds()
	assert.Equal(t, [2]int{oc.Width, oc.Height}, [2]int{b.Dx(), b.Dy()}, "%s: dimensions must not change", e.URL)
}

func decodeModern(mt string, b []byte) (image.Image, error) {
	f := map[string]img.Format{"image/webp": img.WebP, "image/avif": img.AVIF, "image/jxl": img.JXL}[mt]
	return img.NewWASM().Decode(f, b)
}
