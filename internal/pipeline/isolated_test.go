package pipeline

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/codecworker"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "codec-worker" {
		if err := codecworker.Run(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDefaultPipelineRequiresRealSandbox(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Image.Background = false
	cfg.Image.Warmup = false
	cfg.Image.Timeout = 20 * time.Second
	cfg.Text.Minify.HTML = "off"
	p := New(cfg)
	defer p.Close()
	state := p.Sandbox()
	if !testutil.SandboxSupported() {
		require.False(t, state.Enforced)
		require.NotEmpty(t, state.Error)
		body := htmlBody(250)
		rec, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/html", body))
		assert.Equal(t, PassError, out.Reason)
		assert.Equal(t, body, rec.Body.Bytes())
		assert.Empty(t, rec.Header().Get("Content-Encoding"))
		return
	}
	require.True(t, state.Enforced, state.Error)
	require.False(t, state.VerifiedAt.IsZero())
	assert.Equal(t, "denied", state.Network)
	assert.Equal(t, state.VerifiedAt, p.Sandbox().VerifiedAt)
	body := htmlBody(250)
	for _, unknown := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "https://example.test/page", nil)
		req.Header.Set("Accept-Encoding", "br")
		resp := newResp("text/html", body)
		if unknown {
			resp.ContentLength = -1
			resp.Header.Del("Content-Length")
		}
		rec, out := run(p, req, resp)
		if unknown {
			require.Equal(t, PassStream, out.Reason, out.Cause)
			assert.Equal(t, body, rec.Body.Bytes())
			assert.Empty(t, rec.Header().Get("Content-Encoding"))
		} else {
			require.Equal(t, Transformed, out.Reason, out.Cause)
			assert.Equal(t, body, decode(t, codec.Brotli, rec.Body.Bytes()))
		}
	}
	font, err := os.ReadFile(filepath.Join("..", "fonts", "testdata", "AnonymousPro-Regular.ttf"))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "https://example.test/font.ttf", nil)
	req.Header.Set("Sec-Fetch-Dest", "font")
	rec, out := run(p, req, newResp("font/ttf", font))
	require.Equal(t, Transformed, out.Reason, out.Cause)
	assert.Equal(t, "wOF2", rec.Body.String()[:4])
	input := jpegBytes(t, 192, 128, 95)
	req = httptest.NewRequest(http.MethodGet, "https://example.test/photo.jpg", nil)
	req.Header.Set("Accept", "image/webp")
	rec, out = run(p, req, newResp("image/jpeg", input))
	require.Equal(t, Transformed, out.Reason, out.Cause)
	assert.Contains(t, []img.Format{img.WebP, img.JPEG}, img.Sniff(rec.Body.Bytes()))
	assertImagePolicy(t, p, input, rec, out)
}

func TestMissingSandboxFailsClosedAndUnboundedStreamStaysOriginal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WorkerExecutable = filepath.Join(t.TempDir(), "no-worker")
	cfg.Image.Background = false
	p := New(cfg)
	defer p.Close()
	assert.False(t, p.Sandbox().Enforced)
	assert.NotEmpty(t, p.Sandbox().Error)
	for _, mt := range []string{"text/html", "image/jpeg", "font/ttf"} {
		body := htmlBody(100)
		if mt == "image/jpeg" {
			body = jpegBytes(t, 32, 32, 90)
		}
		req := httptest.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		req.Header.Set("Accept-Encoding", "br")
		req.Header.Set("Accept", "image/webp")
		rec, out := run(p, req, newResp(mt, body))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.Equal(t, body, rec.Body.Bytes())
	}
	body := bytes.Repeat([]byte("bounded streaming original\n"), int(cfg.Text.BufferMax)/20)
	req := httptest.NewRequest(http.MethodGet, "https://example.test/large", nil)
	req.Header.Set("Accept-Encoding", "br")
	resp := newResp("text/html", body)
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	rec, out := run(p, req, resp)
	assert.Equal(t, PassStream, out.Reason)
	assert.Equal(t, body, rec.Body.Bytes())
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assert.True(t, p.WaitBackground(ctx))
	_, err := io.Copy(io.Discard, resp.Body)
	assert.NoError(t, err)
}

type streamProbe struct {
	header  http.Header
	headers chan int
	chunks  chan string
}

func (s *streamProbe) Header() http.Header    { return s.header }
func (s *streamProbe) WriteHeader(status int) { s.headers <- status }
func (s *streamProbe) Write(b []byte) (int, error) {
	select {
	case s.chunks <- string(b):
	default:
	}
	return len(b), nil
}
func (s *streamProbe) Flush() {}

func TestIsolatedUnknownStreamSendsHeadersAndFirstChunkBeforeEOF(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WorkerExecutable = filepath.Join(t.TempDir(), "no-worker")
	p := New(cfg)
	defer p.Close()
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()
	resp := newResp("text/plain", nil, withUnknownLength())
	resp.Body = pr
	rec := &streamProbe{header: make(http.Header), headers: make(chan int, 1), chunks: make(chan string, 1)}
	done := make(chan Outcome, 1)
	go func() { done <- p.Handle(rec, newReq("Accept-Encoding", "gzip"), resp) }()
	select {
	case status := <-rec.headers:
		assert.Equal(t, 200, status)
	case <-time.After(time.Second):
		t.Fatal("stream headers waited for EOF")
	}
	_, err := pw.Write([]byte("first chunk"))
	require.NoError(t, err)
	select {
	case chunk := <-rec.chunks:
		assert.Equal(t, "first chunk", chunk)
	case <-time.After(time.Second):
		t.Fatal("first chunk waited for EOF")
	}
	assert.Empty(t, rec.header.Get("Content-Encoding"))
	require.NoError(t, pw.Close())
	select {
	case out := <-done:
		assert.Equal(t, PassStream, out.Reason)
	case <-time.After(time.Second):
		t.Fatal("stream did not finish")
	}
}
