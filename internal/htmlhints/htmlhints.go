// Package htmlhints reads from an HTML page what its images and scripts say
// about themselves, so that the proxy can use it when those files are asked
// for a moment later: how wide an image is shown, and which files the page
// protects with a Subresource Integrity hash.
package htmlhints

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Page is what a page says about the files it loads.
type Page struct {
	// Images maps the address of an image to the width in CSS pixels the page
	// shows it at, the widest if it shows it at several.
	Images map[string]int
	// Integrity is the addresses of the scripts and style sheets the page loads
	// with an integrity attribute: changing a byte of them would block them.
	Integrity map[string]struct{}
}

// maxTags bounds the work for a page that is nothing but tags.
const maxTags = 20000

// Scan reads the page. base is its address, which relative addresses are
// resolved against (a <base href> inside the page replaces it). Only the first
// maxBytes of the page are read.
func Scan(page []byte, base *url.URL, maxBytes int) Page {
	if maxBytes > 0 && len(page) > maxBytes {
		page = page[:maxBytes]
	}
	out := Page{Images: map[string]int{}, Integrity: map[string]struct{}{}}
	z := html.NewTokenizer(bytes.NewReader(page))
	baseSeen := false
	for tags := 0; tags < maxTags; {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			tags++
		default:
			continue
		}
		name, hasAttr := z.TagName()
		tag := string(name)
		if !hasAttr {
			continue
		}
		var attrs map[string]string
		switch tag {
		case "img", "script", "link", "base":
			attrs = readAttrs(z)
		default:
			continue
		}
		switch tag {
		case "base":
			if href := attrs["href"]; href != "" && !baseSeen {
				baseSeen = true
				if u, err := base.Parse(href); err == nil {
					base = u
				}
			}
		case "img":
			if w := displayWidth(attrs); w > 0 {
				if abs := resolve(base, attrs["src"]); abs != "" && w > out.Images[abs] {
					out.Images[abs] = w
				}
			}
		case "script":
			if attrs["integrity"] != "" {
				if abs := resolve(base, attrs["src"]); abs != "" {
					out.Integrity[abs] = struct{}{}
				}
			}
		case "link":
			if attrs["integrity"] != "" {
				if abs := resolve(base, attrs["href"]); abs != "" {
					out.Integrity[abs] = struct{}{}
				}
			}
		}
	}
	return out
}

func readAttrs(z *html.Tokenizer) map[string]string {
	m := map[string]string{}
	for {
		k, v, more := z.TagAttr()
		if len(k) > 0 {
			if _, dup := m[string(k)]; !dup { // the first one counts, as in a browser
				m[string(k)] = string(v)
			}
		}
		if !more {
			return m
		}
	}
}

// displayWidth is the width the tag asks for, from its width attribute or a
// width in its style, in CSS pixels, 0 when it does not say or says it in a
// unit that depends on the screen.
func displayWidth(a map[string]string) int {
	if w, ok := pixels(a["width"]); ok {
		return w
	}
	for _, decl := range strings.Split(a["style"], ";") {
		k, v, ok := strings.Cut(decl, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "width") {
			if w, ok := pixels(strings.TrimSpace(v)); ok {
				return w
			}
		}
	}
	return 0
}

func pixels(v string) (int, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimSuffix(v, "px")
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 1 || n > 20000 {
		return 0, false
	}
	return int(n + 0.5), true
}

// resolve turns a reference into the address the request will have, without the
// fragment. It returns "" for what is not fetched over HTTP: data: URLs and
// the like.
func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "blob:") || strings.HasPrefix(ref, "javascript:") {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return Key(u)
}

// Key is how an address is remembered: lower-case host, no fragment, no
// default port.
func Key(u *url.URL) string {
	c := *u
	c.Fragment, c.RawFragment, c.User = "", "", nil
	c.Host = strings.ToLower(c.Host)
	if p := c.Port(); (c.Scheme == "http" && p == "80") || (c.Scheme == "https" && p == "443") {
		c.Host = c.Hostname()
	}
	if c.Path == "" {
		c.Path = "/"
	}
	return c.String()
}
