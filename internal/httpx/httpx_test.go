package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRemoveHopByHop(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "keep-alive, X-Custom")
	h.Set("Proxy-Connection", "keep-alive")
	h.Set("Keep-Alive", "timeout=5")
	h.Set("Proxy-Authorization", "Basic abc")
	h.Set("Upgrade", "websocket")
	h.Set("X-Custom", "1")
	h.Set("Accept", "text/html")
	h.Set("Te", "gzip")

	RemoveHopByHop(h)

	assert.Equal(t, http.Header{"Accept": {"text/html"}}, h)
}

func TestRemoveHopByHopKeepsTrailersTE(t *testing.T) {
	h := http.Header{}
	h.Set("Te", "trailers")
	h.Set("Connection", "te")
	RemoveHopByHop(h)
	assert.Equal(t, "trailers", h.Get("Te"))
	assert.Empty(t, h.Get("Connection"))
}

func TestIsUpgrade(t *testing.T) {
	h := http.Header{}
	assert.False(t, IsUpgrade(h))
	h.Set("Upgrade", "websocket")
	assert.False(t, IsUpgrade(h), "Upgrade without Connection: upgrade")
	h.Set("Connection", "keep-alive, Upgrade")
	assert.True(t, IsUpgrade(h))
}

func TestHasToken(t *testing.T) {
	assert.True(t, HasToken("gzip, br", "br"))
	assert.True(t, HasToken("GZIP", "gzip"))
	assert.True(t, HasToken("private, no-transform;x=1", "no-transform"))
	assert.False(t, HasToken("gzipped", "gzip"))
	assert.False(t, HasToken("", "gzip"))
}

func TestHeaderHasDirective(t *testing.T) {
	h := http.Header{}
	h.Add("Cache-Control", "max-age=60")
	h.Add("Cache-Control", "No-Transform")
	assert.True(t, HeaderHasDirective(h, "Cache-Control", "no-transform"))
	assert.True(t, HeaderHasDirective(h, "Cache-Control", "max-age"))
	assert.False(t, HeaderHasDirective(h, "Cache-Control", "private"))
}

func TestMediaType(t *testing.T) {
	h := http.Header{}
	assert.Equal(t, "", MediaType(h))
	h.Set("Content-Type", "Text/HTML; charset=UTF-8")
	assert.Equal(t, "text/html", MediaType(h))
	h.Set("Content-Type", "image/png")
	assert.Equal(t, "image/png", MediaType(h))
}

func TestMediaTypeDoesNotAllocateWhenLowercase(t *testing.T) {
	h := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = MediaType(h) }))
}

func TestVaryAndETag(t *testing.T) {
	h := http.Header{}
	AddVary(h, "Accept-Encoding")
	AddVary(h, "accept-encoding")
	AddVary(h, "Accept")
	assert.Equal(t, []string{"Accept-Encoding", "Accept"}, h["Vary"])

	star := http.Header{"Vary": {"*"}}
	AddVary(star, "Accept")
	assert.Equal(t, []string{"*"}, star["Vary"])

	e := http.Header{"Etag": {`"abc"`}}
	WeakenETag(e)
	WeakenETag(e)
	assert.Equal(t, `W/"abc"`, e.Get("Etag"))
}

func TestDropValidators(t *testing.T) {
	h := http.Header{}
	h.Set("Content-MD5", "x")
	h.Set("Repr-Digest", "x")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Cache-Control", "max-age=1")
	DropValidators(h)
	assert.Equal(t, http.Header{"Cache-Control": {"max-age=1"}}, h)
}
