package pipeline

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/xcache"
)

type partialResponseWriter struct {
	*httptest.ResponseRecorder
	limit int
	err   error
}

func (w *partialResponseWriter) Write(b []byte) (int, error) {
	left := max(0, w.limit-w.Body.Len())
	if len(b) <= left {
		return w.ResponseRecorder.Write(b)
	}
	n, _ := w.ResponseRecorder.Write(b[:left])
	return n, w.err
}

type failedFlushWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *failedFlushWriter) FlushError() error { return w.err }

func TestBufferedWritesReportFailuresWithoutInventingBytes(t *testing.T) {
	p := newPipeline()
	defer p.Close()
	body := htmlBody(200)
	broken := errors.New("client disconnected")
	tests := []struct {
		name   string
		reason Reason
		write  func(http.ResponseWriter) Outcome
	}{
		{"passthrough", PassType, func(w http.ResponseWriter) Outcome {
			return p.passthrough(w, newResp("application/octet-stream", body), PassType)
		}},
		{"buffered fallback", PassBusy, func(w http.ResponseWriter) Outcome {
			return p.writeBuffered(w, newResp("text/html", body), body, int64(len(body)), PassBusy)
		}},
		{"recoded image", Transformed, func(w http.ResponseWriter) Outcome {
			return p.writeRecoded(w, newResp("image/png", body), codec.None,
				&xcache.Entry{Data: body[:100], ContentType: "image/webp"}, &img.Profile{}, img.Hints{}, int64(len(body)))
		}},
		{"compressed text", Transformed, func(w http.ResponseWriter) Outcome {
			return p.Handle(w, newReq("Accept-Encoding", "gzip"), newResp("text/html", body))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, err := range []error{broken, nil} {
				w := &partialResponseWriter{ResponseRecorder: httptest.NewRecorder(), limit: 7, err: err}
				out := tt.write(w)
				assert.Equal(t, tt.reason, out.Reason)
				assert.True(t, out.Written)
				assert.Equal(t, int64(len(body)), out.BytesIn)
				assert.Equal(t, int64(7), out.BytesOut)
				require.ErrorIs(t, out.Err, ErrClientWrite)
				if err == nil {
					assert.ErrorIs(t, out.Err, io.ErrShortWrite)
				} else {
					assert.ErrorIs(t, out.Err, broken)
				}
			}
		})
	}
}

func TestTextStreamReportsWriteFlushAndCloseFailures(t *testing.T) {
	p := newPipeline(func(c *Config) { c.Text.BufferMax = 1 })
	defer p.Close()
	body := htmlBody(200)
	broken := errors.New("client disconnected")
	for _, limit := range []int{0, 10} {
		t.Run("write or close", func(t *testing.T) {
			w := &partialResponseWriter{ResponseRecorder: httptest.NewRecorder(), limit: limit, err: broken}
			out := p.Handle(w, newReq("Accept-Encoding", "gzip"), newResp("text/html", body))
			assert.Equal(t, Transformed, out.Reason)
			require.ErrorIs(t, out.Err, ErrClientWrite)
			assert.ErrorIs(t, out.Err, broken)
			assert.Equal(t, int64(len(body)), out.BytesIn)
			assert.Equal(t, int64(w.Body.Len()), out.BytesOut)
			assert.Equal(t, int64(limit), out.BytesOut)
		})
	}
	t.Run("flush", func(t *testing.T) {
		w := &failedFlushWriter{ResponseRecorder: httptest.NewRecorder(), err: broken}
		out := p.Handle(w, newReq("Accept-Encoding", "gzip"), newResp("text/html", body, withUnknownLength()))
		assert.Equal(t, Transformed, out.Reason)
		require.ErrorIs(t, out.Err, ErrClientWrite)
		assert.ErrorIs(t, out.Err, broken)
		assert.Equal(t, int64(len(body)), out.BytesIn)
		assert.Equal(t, int64(w.Body.Len()), out.BytesOut)
	})
}
