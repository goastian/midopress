package magic

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

const testHost = "midopress.it"

func makeCert(t *testing.T, isCA bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "MidoPress CA"},
		NotBefore:             time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2036, 1, 2, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newHandler(t *testing.T, mod func(*Options)) (http.Handler, []byte) {
	t.Helper()
	certPEM := makeCert(t, true)
	o := Options{
		Host:      testHost,
		CertPEM:   certPEM,
		ProxyAddr: "192.168.1.10:8080",
		Version:   "v1.2.3",
	}
	if mod != nil {
		mod(&o)
	}
	h, err := New(o)
	require.NoError(t, err)
	return h, certPEM
}

func do(h http.Handler, method, target, ua string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = testHost
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNewValidation(t *testing.T) {
	_, err := New(Options{CertPEM: []byte("junk")})
	require.Error(t, err)
	_, err = New(Options{CertPEM: makeCert(t, false)})
	require.Error(t, err)
	_, err = New(Options{Host: `bad"host`, CertPEM: makeCert(t, true)})
	require.Error(t, err)
	h, err := New(Options{CertPEM: makeCert(t, true)})
	require.NoError(t, err)
	assert.Equal(t, 200, do(h, "GET", "/health", "").Code)
}

func TestIsMagic(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"midopress.it", true},
		{"MidoPress.IT", true},
		{"midopress.it:80", true},
		{"midopress.it:443", true},
		{"midopress.it.", true},
		{"midopress.it.:8080", true},
		{"midopress.it.evil.com", false},
		{"evil.midopress.it", false},
		{"xmidopress.it", false},
		{"[::1]", false},
		{"[::1]:80", false},
		{"127.0.0.1", false},
		{"", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, IsMagic(c.host, testHost), c.host)
	}
	assert.False(t, IsMagic("", ""))
	assert.True(t, IsMagic("[2001:db8::1]:80", "[2001:db8::1]"))
}

func TestMethods(t *testing.T) {
	h, _ := newHandler(t, nil)
	for _, p := range []string{"/", "/ca.crt", "/health", "/check", "/nope"} {
		w := do(h, "POST", p, "")
		assert.Equal(t, 405, w.Code, p)
		assert.Equal(t, "GET, HEAD", w.Header().Get("Allow"))
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	for _, m := range []string{"PUT", "DELETE", "OPTIONS", "PATCH"} {
		assert.Equal(t, 405, do(h, m, "/", "").Code, m)
	}
}

func TestHead(t *testing.T) {
	h, _ := newHandler(t, nil)
	for _, p := range []string{"/", "/ca.crt", "/ca.der", "/ca.p12", "/health", "/check", "/qr.svg?u=http://midopress.it/"} {
		get := do(h, "GET", p, "")
		head := do(h, "HEAD", p, "")
		assert.Equal(t, 200, head.Code, p)
		assert.Empty(t, head.Body.Bytes(), p)
		assert.Equal(t, get.Header().Get("Content-Length"), head.Header().Get("Content-Length"), p)
		assert.Equal(t, get.Header().Get("Content-Type"), head.Header().Get("Content-Type"), p)
	}
}

func TestCertificateFiles(t *testing.T) {
	h, certPEM := newHandler(t, nil)
	want, err := parsePEM(certPEM)
	require.NoError(t, err)

	for _, p := range []string{"/ca.crt", "/ca.pem"} {
		w := do(h, "GET", p, "")
		assert.Equal(t, 200, w.Code)
		assert.Equal(t, "application/x-x509-ca-cert", w.Header().Get("Content-Type"))
		assert.Equal(t, `attachment; filename="midopress-ca.crt"`, w.Header().Get("Content-Disposition"))
		assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		blk, rest := pem.Decode(w.Body.Bytes())
		require.NotNil(t, blk)
		assert.Empty(t, bytes.TrimSpace(rest))
		assert.Equal(t, "CERTIFICATE", blk.Type)
		assert.Equal(t, want.Raw, blk.Bytes)
	}

	w := do(h, "GET", "/ca.der", "")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "application/x-x509-ca-cert", w.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="midopress-ca.der"`, w.Header().Get("Content-Disposition"))
	assert.Equal(t, want.Raw, w.Body.Bytes())
	der, err := x509.ParseCertificate(w.Body.Bytes())
	require.NoError(t, err)
	assert.True(t, der.Equal(want))
}

func TestP12(t *testing.T) {
	h, certPEM := newHandler(t, nil)
	want, err := parsePEM(certPEM)
	require.NoError(t, err)

	w := do(h, "GET", "/ca.p12", "")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "application/x-pkcs12", w.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="midopress-ca.p12"`, w.Header().Get("Content-Disposition"))

	certs, err := pkcs12.DecodeTrustStore(w.Body.Bytes(), "midopress")
	require.NoError(t, err)
	require.Len(t, certs, 1)
	assert.True(t, certs[0].Equal(want))

	_, err = pkcs12.DecodeTrustStore(w.Body.Bytes(), "wrong")
	require.ErrorIs(t, err, pkcs12.ErrIncorrectPassword)

	other, err := P12(certPEM, "other")
	require.NoError(t, err)
	_, err = pkcs12.DecodeTrustStore(other, "midopress")
	require.Error(t, err)
	_, err = P12([]byte("junk"), "x")
	require.Error(t, err)
}

func TestCheck(t *testing.T) {
	h, _ := newHandler(t, nil)
	w := do(h, "GET", "/check", "")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	assert.JSONEq(t, `{"ok":true}`, w.Body.String())
}

func TestHealthAndNotFound(t *testing.T) {
	h, _ := newHandler(t, nil)
	w := do(h, "GET", "/health", "")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "ok\n", w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")

	w = do(h, "GET", "/does/not/exist", "")
	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, w.Body.String(), `href="/"`)
	wellFormed(t, w.Body.String())
	assertCSPMatches(t, w)
}

func TestMe(t *testing.T) {
	h, _ := newHandler(t, nil)
	assert.Equal(t, 404, do(h, "GET", "/api/v1/me", "").Code)

	want := Me{Device: "pixel", Addr: "10.0.0.5", Profile: "slow", Requests: 12, BytesIn: 5000, BytesOut: 2000, Saved: 3000, CATrustedHint: "hint"}
	h, _ = newHandler(t, func(o *Options) {
		o.Me = func(r *http.Request) Me { return want }
	})
	w := do(h, "GET", "/api/v1/me", "")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"device":"pixel","addr":"10.0.0.5","profile":"slow","requests":12,"bytes_in":5000,"bytes_out":2000,"saved":3000,"ca_trusted_hint":"hint"}`, w.Body.String())
	var got Me
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, want, got)

	page := do(h, "GET", "/", "").Body.String()
	assert.Contains(t, page, `id="h-me"`)
	assert.Contains(t, page, "pixel")
	assert.Contains(t, page, "2.9 KB")
	assert.NotContains(t, do(newOnly(t), "GET", "/", "").Body.String(), `id="h-me"`)
}

func newOnly(t *testing.T) http.Handler {
	h, _ := newHandler(t, nil)
	return h
}

func TestQR(t *testing.T) {
	h, _ := newHandler(t, nil)
	ok := []string{
		"http://midopress.it/ca.crt",
		"https://midopress.it/",
		"http://MIDOPRESS.it:80/x?y=1",
		"http://midopress.it./ca.crt",
	}
	for _, u := range ok {
		w := do(h, "GET", "/qr.svg?u="+url.QueryEscape(u), "")
		assert.Equal(t, 200, w.Code, u)
		assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Body.String(), "<svg")
		assert.Contains(t, w.Body.String(), `xmlns="http://www.w3.org/2000/svg"`)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	bad := []string{
		"",
		"http://example.com/",
		"http://midopress.it.evil.com/",
		"http://midopress.it@evil.com/",
		"http://evil.com@midopress.it/",
		"http://evil.com/?http://midopress.it/",
		"ftp://midopress.it/",
		"javascript:alert(1)",
		"midopress.it/ca.crt",
		"//midopress.it/ca.crt",
		"WIFI:S:x;T:WPA;P:y;;",
		"http://midopress.it/" + strings.Repeat("a", 300),
	}
	for _, u := range bad {
		w := do(h, "GET", "/qr.svg?u="+url.QueryEscape(u), "")
		assert.Equal(t, 400, w.Code, u)
	}
	assert.Equal(t, 400, do(h, "GET", "/qr.svg", "").Code)
	long := "http://midopress.it/" + strings.Repeat("a", 280)
	assert.Equal(t, 200, do(h, "GET", "/qr.svg?u="+url.QueryEscape(long), "").Code)
}

var (
	reStyle  = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	reScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)
)

func sri(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func assertCSPMatches(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	csp := w.Header().Get("Content-Security-Policy")
	require.NotEmpty(t, csp)
	body := w.Body.String()
	st := reStyle.FindStringSubmatch(body)
	require.NotNil(t, st)
	assert.Contains(t, csp, "style-src "+sri(st[1]))
	assert.NotContains(t, csp, "unsafe-inline")
	assert.NotContains(t, csp, "unsafe-eval")
	assert.NotContains(t, body, ` style="`)
	if sc := reScript.FindStringSubmatch(body); sc != nil {
		assert.Contains(t, csp, "script-src "+sri(sc[1]))
	} else {
		assert.NotContains(t, csp, "script-src")
	}
}

func TestPageHeadersAndCSP(t *testing.T) {
	h, _ := newHandler(t, nil)
	w := do(h, "GET", "/", "")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assertCSPMatches(t, w)
	csp := w.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'none'")
	assert.Contains(t, csp, "connect-src https://midopress.it")
	assert.Equal(t, 1, strings.Count(w.Body.String(), "<script"))
	assert.Equal(t, 1, strings.Count(w.Body.String(), "<style"))
	assert.NotContains(t, w.Body.String(), " onclick=")
}

func TestPageContent(t *testing.T) {
	h, certPEM := newHandler(t, nil)
	c, err := parsePEM(certPEM)
	require.NoError(t, err)
	sum := sha256.Sum256(c.Raw)
	body := do(h, "GET", "/", "").Body.String()

	assert.Contains(t, body, fingerprint(sum[:]))
	assert.Contains(t, body, "2 Jan 2026")
	assert.Contains(t, body, "2 Jan 2036")
	for _, l := range []string{`href="/ca.crt"`, `href="/ca.der"`, `href="/ca.p12"`} {
		assert.Contains(t, body, l)
	}
	assert.Contains(t, body, "<svg")
	assert.Contains(t, body, "crispEdges")
	assert.Contains(t, body, "http://midopress.it/ca.crt")
	assert.Contains(t, body, "Install it with the steps below, then reload this page.")
	assert.Contains(t, body, "192.168.1.10:8080")
	assert.Contains(t, body, "v1.2.3")
	assert.Contains(t, body, "private key never leaves the server")
	assert.Contains(t, body, "certutil -addstore -f ROOT midopress-ca.crt")
	assert.Contains(t, body, "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain midopress-ca.crt")
	assert.Contains(t, body, "update-ca-certificates")
	assert.Contains(t, body, "update-ca-trust")
	assert.Contains(t, body, "security.enterprise_roots.enabled")
	assert.Contains(t, body, "Trust this CA to identify websites")
	assert.Contains(t, body, "Certificate Trust Settings")
	assert.Contains(t, body, "Install a certificate")
	assert.Equal(t, 6, strings.Count(body, "Remove it:"))
	assert.Contains(t, body, "prefers-color-scheme:dark")
	assert.Contains(t, body, `name="viewport"`)
	assert.Contains(t, body, "https://midopress.it/check")
}

func TestPageHTMLEscapes(t *testing.T) {
	h, _ := newHandler(t, func(o *Options) {
		o.ProxyAddr = `<script>alert(1)</script>`
		o.Version = `"><img src=x>`
	})
	body := do(h, "GET", "/", "").Body.String()
	assert.NotContains(t, body, "<script>alert(1)")
	assert.NotContains(t, body, `"><img`)
	assert.Equal(t, 1, strings.Count(body, "<script"))
}

func openSections(t *testing.T, body string) []string {
	t.Helper()
	re := regexp.MustCompile(`<details class="plat" id="([a-z]+)" open>`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestPlatformByUserAgent(t *testing.T) {
	h, _ := newHandler(t, nil)
	cases := []struct {
		name string
		ua   string
		want []string
	}{
		{"android chrome", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36", []string{"android"}},
		{"iphone safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", []string{"ios"}},
		{"ipad safari", "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", []string{"ios"}},
		{"windows edge", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", []string{"windows"}},
		{"macos safari", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", []string{"macos"}},
		{"linux firefox", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0", []string{"linux", "firefox"}},
		{"midori android", "Mozilla/5.0 (Android 14; Mobile; rv:128.0) Gecko/128.0 Firefox/128.0 Midori/1.0", []string{"android", "firefox"}},
		{"firefox ios", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/128.0 Mobile/15E148 Safari/605.1.15", []string{"ios"}},
		{"unknown", "curl/8.0", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := do(h, "GET", "/", c.ua).Body.String()
			assert.Equal(t, c.want, openSections(t, body))
		})
	}
}

func allowedURL(u string) bool {
	return strings.HasPrefix(u, "http://midopress.it") ||
		strings.HasPrefix(u, "https://midopress.it") ||
		strings.HasPrefix(u, sourceURL)
}

func TestNoExternalResources(t *testing.T) {
	h, _ := newHandler(t, nil)
	for _, ua := range []string{"", "Mozilla/5.0 (Linux; Android 14)"} {
		body := do(h, "GET", "/", ua).Body.String()
		for _, u := range regexp.MustCompile(`https?://[^\s"'<>)]*`).FindAllString(body, -1) {
			assert.True(t, allowedURL(u), "unexpected URL %q", u)
		}
		assert.NotContains(t, body, "<link")
		assert.NotContains(t, body, "<img")
		assert.NotContains(t, body, "@import")
		assert.NotContains(t, body, "url(")
		assert.NotContains(t, body, "src=")
	}
}

func TestPageHasNoEmoji(t *testing.T) {
	h, _ := newHandler(t, nil)
	body := do(h, "GET", "/", "").Body.String()
	for _, r := range body {
		assert.Less(t, r, rune(0x2190), "non-text symbol %q", r)
	}
}

var voidTags = map[string]bool{
	"meta": true, "link": true, "br": true, "img": true, "input": true, "hr": true,
	"path": true, "rect": true,
}

func wellFormed(t *testing.T, doc string) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(doc))
	var stack []string
	ids := map[string]bool{}
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			require.ErrorIs(t, z.Err(), io.EOF)
			assert.Empty(t, stack, "unclosed elements")
			return
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			for _, a := range tok.Attr {
				if a.Key == "id" {
					assert.False(t, ids[a.Val], "duplicate id %s", a.Val)
					ids[a.Val] = true
				}
			}
			if tt == html.StartTagToken && !voidTags[tok.Data] {
				stack = append(stack, tok.Data)
			}
		case html.EndTagToken:
			name := z.Token().Data
			require.NotEmpty(t, stack, "stray </%s>", name)
			require.Equal(t, stack[len(stack)-1], name, "mismatched </%s>", name)
			stack = stack[:len(stack)-1]
		}
	}
}

func TestPageWellFormedAndSmall(t *testing.T) {
	h, _ := newHandler(t, func(o *Options) {
		o.Me = func(*http.Request) Me { return Me{Device: "d", Profile: "p"} }
	})
	for _, ua := range []string{"", "Mozilla/5.0 (Linux; Android 14)", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"} {
		w := do(h, "GET", "/", ua)
		wellFormed(t, w.Body.String())
		assert.Less(t, w.Body.Len(), 40*1024)
		assert.True(t, strings.HasPrefix(w.Body.String(), "<!DOCTYPE html>"))
		assert.Equal(t, 1, strings.Count(w.Body.String(), "<h1>"))
		for _, id := range []string{"android", "ios", "windows", "macos", "linux", "firefox"} {
			assert.Contains(t, w.Body.String(), `id="`+id+`"`)
		}
	}
}

func TestDump(t *testing.T) {
	if os.Getenv("MAGIC_DUMP") != "1" {
		t.Skip("set MAGIC_DUMP=1 to write /tmp/opencode/magic-page.html")
	}
	h, _ := newHandler(t, func(o *Options) {
		o.Me = func(*http.Request) Me {
			return Me{Device: "Pixel 8", Addr: "192.168.1.23", Profile: "mobile-low", Requests: 421, BytesIn: 18_400_000, BytesOut: 6_100_000, Saved: 12_300_000}
		}
	})
	w := do(h, "GET", "/", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/126.0.0.0 Mobile Safari/537.36")
	require.NoError(t, os.MkdirAll("/tmp/opencode", 0o755))
	require.NoError(t, os.WriteFile("/tmp/opencode/magic-page.html", w.Body.Bytes(), 0o644))
	t.Logf("page size: %d bytes", w.Body.Len())
}
