package transcoder

import (
	"net/http"

	"github.com/goastian/midopress/internal/proxy"
)

type Identity struct{}

func (i *Identity) Transcode(w *proxy.ResponseWriter, r *proxy.ResponseReader, headers http.Header) error {
	return w.CopyFrom(r)
}
