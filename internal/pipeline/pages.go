package pipeline

import (
	"net/http"
	"net/url"
	"time"

	"github.com/goastian/midopress/internal/htmlhints"
	"github.com/goastian/midopress/internal/lru"
	"github.com/goastian/midopress/internal/textmin"
)

// What a page says about the files it loads is learned when the page goes by,
// and used when those files are asked for a moment later: how wide an image is
// shown, which is how profiles that resize can do it without being told the size
// of the screen, and which files carry an integrity hash, which must reach the
// browser byte for byte.
const (
	pageMemory    = 20 * time.Minute
	pageEntries   = 50000
	pageScanBytes = 768 << 10
	// the streaming scan reads the page again at these intervals, so that the
	// images near its top are known before the browser asks for them
	pageScanStep = 32 << 10
)

type pageHints struct {
	widths    *lru.Cache[string, int]
	integrity *lru.Cache[string, struct{}]
}

func newPageHints() *pageHints {
	return &pageHints{
		widths:    lru.New[string, int](pageEntries, pageMemory),
		integrity: lru.New[string, struct{}](pageEntries, pageMemory),
	}
}

func (h *pageHints) learn(req *http.Request, page []byte) {
	pg := htmlhints.Scan(page, requestURL(req), pageScanBytes)
	for u, w := range pg.Images {
		if cur, _ := h.widths.Get(u); w > cur {
			h.widths.Add(u, w)
		}
	}
	for u := range pg.Integrity {
		h.integrity.Add(u, struct{}{})
	}
}

// width is the width in CSS pixels that a page showed the image at, 0 when no
// page said.
func (h *pageHints) width(u *url.URL) int {
	w, _ := h.widths.Get(htmlhints.Key(u))
	return w
}

// pinned reports whether a page loads the file with an integrity hash.
func (h *pageHints) pinned(u *url.URL) bool {
	_, ok := h.integrity.Get(htmlhints.Key(u))
	return ok
}

func requestURL(req *http.Request) *url.URL {
	u := *req.URL
	if u.Host == "" {
		u.Host = req.Host
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	return &u
}

func isHTML(mt string) bool { return mt == "text/html" || mt == "application/xhtml+xml" }

// wantsPages says whether reading the pages is worth it for this flow: a profile
// that resizes images by the width a page shows them at, or any minification
// that an integrity hash would turn into a blocked file.
func (p *Pipeline) wantsPages(fl *flow, mt string) bool {
	if !p.cfg.Image.PageHints || !isHTML(mt) {
		return false
	}
	m := fl.opts.minify
	return fl.prof.ResizeToViewport || m.CSS != textmin.Off || m.JS != textmin.Off
}

// dprGuess is the pixel density assumed for a width that came from a page: the
// page says how wide the image is shown, not how dense the screen is, so the
// guess leans to keeping detail.
func (p *Pipeline) dprGuess() float64 {
	if d := p.cfg.Image.HintDPR; d >= 1 {
		return d
	}
	return 2
}
