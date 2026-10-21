// Package textmin minifies HTML, CSS, JavaScript, SVG and JSON responses
// with granular, safe-by-default semantics.
//
// Every content type has its own [Mode]. The default configuration only
// enables the Safe mode, which removes bytes that can never change how a page
// renders or what a script or stylesheet does:
//
//   - HTML Safe removes comments (except conditional comments, SSI directives
//     and other special comments) and collapses redundant whitespace. It keeps
//     document tags, end tags, attribute quotes, default attribute values and
//     inter-element whitespace. Inline <script> content is only touched when
//     JS is enabled, inline <style> and style attributes only when CSS is
//     enabled, and everything else (JSON scripts, <pre>, <textarea>, <xmp>,
//     templates) is left byte-identical. The document is returned unchanged
//     when it contains server-side template markers ({{, {%, <%, <?php, and
//     ${ outside <script> and <style>), a doctype other than <!doctype html>
//     (rewriting it could switch the rendering mode) or an inline
//     white-space: pre* declaration. Whitespace collapsing cannot see
//     external stylesheets: a non-<pre> element made whitespace-sensitive by
//     an external stylesheet would be affected.
//   - CSS Safe uses the library's default Version and Precision 0 (existing
//     numbers are never rounded). Vendor-prefixed and fallback declarations,
//     declaration order, @charset, custom property values, url() contents and
//     strings are preserved. Stylesheets with constructs the minifier
//     mishandles (quoted generic font families, case-sensitive selectors such
//     as ::part() or camelCase SVG element names, @namespace) are returned
//     unchanged.
//   - JS Safe never renames identifiers. It still rewrites syntax (merges
//     statements, drops dead code, shortens literals), which only changes
//     observable behavior for code that inspects its own source, such as
//     Function.prototype.toString. JS is Off in [DefaultConfig].
//   - SVG Safe never rounds numbers and keeps ids, viewBox, styles, the
//     xml namespace and every declared namespace except well-known editor ones.
//     SVG documents with scripts, mixed text content (<tspan>), xml:space,
//     xml-stylesheet processing instructions or prefixed svg: elements are
//     returned unchanged.
//   - JSON Safe only removes insignificant whitespace; numbers and strings
//     stay byte-identical. Invalid JSON is rejected.
//
// Aggressive uses the minifier library defaults: HTML may drop optional tags,
// quotes and default attribute values, JS renames local variables, and JSON
// numbers are rewritten to their shortest form. The structural checks apply
// in every mode: HTML with template markers is left alone, JSON must be valid,
// minified JavaScript must parse, and SVG must have balanced tags.
// application/xhtml+xml is only minified in Aggressive HTML mode, because the
// HTML minifier emits syntax that is not well-formed XML.
//
// Subresource Integrity: an integrity="sha384-..." attribute pins the exact
// bytes of a script or stylesheet. Minifying ANY such resource (or the HTML
// that references it, if it changes the attribute) makes the browser reject
// it. This is why JavaScript minification is opt-in and why a proxy should only
// enable minification for deployments that do not rely on SRI.
//
// A Minifier writes nothing to the destination on error, so callers can fall
// back to the original body. Inputs that are not minified (templates, already
// minified, or too risky) are copied to the destination unchanged.
package textmin

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
	"github.com/tdewolff/minify/v2/json"
	"github.com/tdewolff/minify/v2/svg"
	"github.com/tdewolff/parse/v2"
	parsejs "github.com/tdewolff/parse/v2/js"
)

// MaxSize is the largest input accepted by [Minifier.Minify].
const MaxSize = 8 << 20

var (
	// ErrTooLarge is returned for inputs larger than [MaxSize].
	ErrTooLarge = errors.New("textmin: input too large")
	// ErrUnsupported is returned for media types that are unknown or whose
	// mode is Off.
	ErrUnsupported = errors.New("textmin: media type not handled")

	errInvalidJSON = errors.New("invalid JSON")
)

// Mode selects how much a content type is minified.
type Mode string

const (
	// Off disables minification of the content type.
	Off Mode = "off"
	// Safe never changes how a page renders or what a script or stylesheet does.
	Safe Mode = "safe"
	// Aggressive uses the library defaults: it may drop optional tags and
	// quotes and rename local JavaScript variables.
	Aggressive Mode = "aggressive"
)

// Config selects the mode per content type. Unknown or empty modes are Off.
type Config struct {
	HTML Mode
	CSS  Mode
	// JS is Off by default: JavaScript minification stays opt-in.
	JS   Mode
	SVG  Mode
	JSON Mode
	// SkipMinified skips inputs that already look minified (see LooksMinified).
	SkipMinified bool
}

// DefaultConfig returns Safe for HTML, CSS, SVG and JSON, Off for JS and
// SkipMinified enabled.
func DefaultConfig() Config {
	return Config{HTML: Safe, CSS: Safe, JS: Off, SVG: Safe, JSON: Safe, SkipMinified: true}
}

// Enabled reports whether any content type is not Off.
func (c Config) Enabled() bool {
	return validMode(c.HTML) || validMode(c.CSS) || validMode(c.JS) || validMode(c.SVG) || validMode(c.JSON)
}

func validMode(m Mode) bool { return m == Safe || m == Aggressive }

func (c Config) normalized() Config {
	for _, m := range []*Mode{&c.HTML, &c.CSS, &c.JS, &c.SVG, &c.JSON} {
		if !validMode(*m) {
			*m = Off
		}
	}
	return c
}

type kind uint8

const (
	kindNone kind = iota
	kindHTML
	kindCSS
	kindJS
	kindSVG
	kindJSON
)

// Minifier minifies response bodies. It is safe for concurrent use.
type Minifier struct {
	cfg    Config
	nested *minify.M
}

// New returns a Minifier for the configuration.
func New(c Config) *Minifier {
	m := &Minifier{cfg: c.normalized()}
	m.nested = minify.New()
	if m.cfg.CSS != Off {
		m.nested.AddFunc("text/css", m.nestedFunc(kindCSS))
	}
	if m.cfg.JS != Off {
		for _, t := range []string{"text/javascript", "application/javascript", "application/x-javascript", "application/ecmascript"} {
			m.nested.AddFunc(t, m.nestedFunc(kindJS))
		}
	}
	if m.cfg.SVG != Off {
		m.nested.AddFunc("image/svg+xml", m.nestedFunc(kindSVG))
	}
	return m
}

func (m *Minifier) mode(k kind) Mode {
	switch k {
	case kindHTML:
		return m.cfg.HTML
	case kindCSS:
		return m.cfg.CSS
	case kindJS:
		return m.cfg.JS
	case kindSVG:
		return m.cfg.SVG
	case kindJSON:
		return m.cfg.JSON
	}
	return Off
}

// classify maps a media type (parameters allowed) to a content kind,
// regardless of the configured modes.
func classify(mediaType string) kind {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return kindHTML
	case "text/css":
		return kindCSS
	case "text/javascript", "application/javascript", "application/x-javascript", "application/ecmascript":
		return kindJS
	case "image/svg+xml":
		return kindSVG
	case "application/json", "text/json":
		return kindJSON
	}
	if strings.HasSuffix(mediaType, "+json") && strings.Count(mediaType, "/") == 1 {
		return kindJSON
	}
	return kindNone
}

func (m *Minifier) kindOf(mediaType string) kind {
	k := classify(mediaType)
	if m.mode(k) == Off {
		return kindNone
	}
	if k == kindHTML && m.cfg.HTML == Safe && isXHTML(mediaType) {
		// The HTML minifier emits <br>, bare boolean attributes and other
		// HTML-only syntax, which makes an XML document ill-formed.
		return kindNone
	}
	return k
}

func isXHTML(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "application/xhtml+xml")
}

// Handles reports whether Minify accepts the media type under the current
// configuration. application/xhtml+xml is only handled in Aggressive HTML
// mode because the HTML minifier output is not well-formed XML.
func (m *Minifier) Handles(mediaType string) bool {
	return m.kindOf(mediaType) != kindNone
}

// Minify writes the minified src to dst. On error nothing is written to dst.
// When the input is not changed (template markers, already minified, a risky
// construct, or the result would be larger) src is written unchanged.
// src is never modified.
func (m *Minifier) Minify(mediaType string, dst io.Writer, src []byte) error {
	k := m.kindOf(mediaType)
	if k == kindNone {
		return ErrUnsupported
	}
	if len(src) > MaxSize {
		return ErrTooLarge
	}
	if len(src) == 0 {
		return nil
	}
	if m.cfg.SkipMinified && LooksMinified(src) {
		_, err := dst.Write(src)
		return err
	}

	in := getBuf()
	defer putBuf(in)
	in.Write(src)
	in.Grow(1)
	out := getBuf()
	defer putBuf(out)

	skipped, err := m.run(k, out, in.Bytes(), nil)
	if err != nil {
		return fmt.Errorf("textmin: %w", err)
	}
	if skipped || out.Len() > len(src) {
		_, err = dst.Write(src)
		return err
	}
	_, err = dst.Write(out.Bytes())
	return err
}

// run minifies in (which it may modify) into out. skipped reports that the
// input must be kept as is, in which case out is unspecified.
func (m *Minifier) run(k kind, out *bytes.Buffer, in []byte, params map[string]string) (skipped bool, err error) {
	mode := m.mode(k)
	r := bytes.NewBuffer(in)
	switch k {
	case kindHTML:
		if hasTemplateMarkers(in) || mode == Safe && htmlRisky(in) {
			return true, nil
		}
		o := &html.Minifier{}
		if mode == Safe {
			o = &html.Minifier{
				KeepSpecialComments: true,
				KeepDefaultAttrVals: true,
				KeepDocumentTags:    true,
				KeepEndTags:         true,
				KeepQuotes:          true,
				KeepWhitespace:      true,
			}
		}
		return false, o.Minify(m.nested, out, r, params)
	case kindCSS:
		if mode == Safe && cssRisky(in) {
			return true, nil
		}
		return false, (&css.Minifier{}).Minify(m.nested, out, r, params)
	case kindJS:
		if err := (&js.Minifier{KeepVarNames: mode == Safe}).Minify(m.nested, out, r, params); err != nil {
			return false, err
		}
		// the minifier is known to emit duplicate declarations for some
		// inputs, so never trust output that does not parse
		verify := bytes.Clone(out.Bytes())
		_, err = parsejs.Parse(parse.NewInput(bytes.NewBuffer(verify)), parsejs.Options{Inline: params["inline"] == "1"})
		return false, err
	case kindSVG:
		if mode == Safe && svgRisky(in) || !svgWellFormed(in) {
			return true, nil
		}
		o := &svg.Minifier{}
		if mode == Safe {
			o.KeepNamespaces = svgKeepNamespaces(in)
		}
		return false, o.Minify(m.nested, out, r, params)
	case kindJSON:
		if !stdjson.Valid(in) {
			return false, errInvalidJSON
		}
		return false, (&json.Minifier{KeepNumbers: mode == Safe}).Minify(m.nested, out, r, params)
	}
	return true, nil
}

// nestedFunc adapts run for content embedded in HTML or SVG. Any failure
// leaves the embedded content untouched by reporting minify.ErrNotExist,
// which the embedding minifiers treat as "keep as is".
func (m *Minifier) nestedFunc(k kind) minify.MinifierFunc {
	return func(_ *minify.M, w io.Writer, r io.Reader, params map[string]string) error {
		var in []byte
		if b, ok := r.(interface{ Bytes() []byte }); ok {
			in = b.Bytes()
		} else {
			var err error
			if in, err = io.ReadAll(r); err != nil {
				return minify.ErrNotExist
			}
		}
		// minifiers edit their input in place, and on failure the caller
		// keeps using the original bytes
		cp := getBuf()
		defer putBuf(cp)
		cp.Write(in)
		cp.Grow(1)
		out := getBuf()
		defer putBuf(out)
		skipped, err := m.run(k, out, cp.Bytes(), params)
		if skipped || err != nil {
			return minify.ErrNotExist
		}
		_, err = w.Write(out.Bytes())
		return err
	}
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuf() *bytes.Buffer {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putBuf(b *bytes.Buffer) {
	if b.Cap() > 4*MaxSize {
		return
	}
	bufPool.Put(b)
}
