// Package httpx has small HTTP helpers shared by the proxy and the pipeline.
package httpx

import (
	"net/http"
	"strings"
)

// hopByHop lists the headers that apply to a single connection, RFC 9110
// section 7.6.1, plus the proxy headers.
var hopByHop = [...]string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// RemoveHopByHop deletes the hop-by-hop headers and every header named in the
// Connection header. "Te: trailers" is kept, gRPC needs it end to end.
func RemoveHopByHop(h http.Header) {
	teTrailers := false
	if te := h["Te"]; len(te) > 0 {
		teTrailers = HasToken(te[0], "trailers")
	}
	for _, v := range h["Connection"] {
		for tok := range strings.SplitSeq(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				h.Del(tok)
			}
		}
	}
	for _, k := range hopByHop {
		delete(h, k)
	}
	if teTrailers {
		h.Set("Te", "trailers")
	}
}

// IsUpgrade reports whether the headers ask for a protocol switch, such as a
// WebSocket handshake.
func IsUpgrade(h http.Header) bool {
	return h.Get("Upgrade") != "" && headerHasToken(h, "Connection", "upgrade")
}

func headerHasToken(h http.Header, key, token string) bool {
	for _, v := range h[key] {
		if HasToken(v, token) {
			return true
		}
	}
	return false
}

// HasToken reports whether the comma separated list v contains token,
// ignoring case and parameters.
func HasToken(v, token string) bool {
	for v != "" {
		var item string
		if i := strings.IndexByte(v, ','); i >= 0 {
			item, v = v[:i], v[i+1:]
		} else {
			item, v = v, ""
		}
		if i := strings.IndexByte(item, ';'); i >= 0 {
			item = item[:i]
		}
		if strings.EqualFold(strings.TrimSpace(item), token) {
			return true
		}
	}
	return false
}

// HeaderHasDirective reports whether any value of the named header contains
// the directive, for example "no-transform" in Cache-Control.
func HeaderHasDirective(h http.Header, key, directive string) bool {
	for _, v := range h[key] {
		for v != "" {
			var item string
			if i := strings.IndexByte(v, ','); i >= 0 {
				item, v = v[:i], v[i+1:]
			} else {
				item, v = v, ""
			}
			if i := strings.IndexByte(item, '='); i >= 0 {
				item = item[:i]
			}
			if strings.EqualFold(strings.TrimSpace(item), directive) {
				return true
			}
		}
	}
	return false
}

// MediaType returns the media type of the Content-Type header, lowercased and
// without parameters. It does not allocate when the header is already
// lowercase.
func MediaType(h http.Header) string {
	v := h.Get("Content-Type")
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	for i := 0; i < len(v); i++ {
		if c := v[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(v)
		}
	}
	return v
}

// AddVary adds token to the Vary header unless it is already listed.
func AddVary(h http.Header, token string) {
	for _, v := range h["Vary"] {
		if v == "*" || HasToken(v, token) {
			return
		}
	}
	h.Add("Vary", token)
}

// WeakenETag turns a strong entity tag into a weak one, as the representation
// is no longer byte identical to the origin's.
func WeakenETag(h http.Header) {
	if v := h.Get("Etag"); v != "" && !strings.HasPrefix(v, "W/") {
		h.Set("Etag", "W/"+v)
	}
}

// DropValidators removes the headers that describe the exact bytes of the
// origin representation.
func DropValidators(h http.Header) {
	for _, k := range [...]string{"Content-Md5", "Digest", "Repr-Digest", "Content-Digest", "Accept-Ranges"} {
		delete(h, k)
	}
}

// CopyHeader copies src into dst sharing the value slices, which is safe as
// long as neither side modifies them in place.
func CopyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = v
	}
}
