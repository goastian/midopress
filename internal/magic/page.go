package magic

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

type step struct {
	Text string
	Code string
}

type platform struct {
	ID    string
	Title string
	Open  bool
	Lead  string
	Steps []step
	Note  string
	Undo  string
}

type pageData struct {
	CSS       template.CSS
	Script    template.JS
	Host      string
	Version   string
	Source    string
	ProxyAddr string
	CN        string
	Finger    string
	NotBefore string
	NotAfter  string
	QR        template.HTML
	Platforms []platform
	Me        *Me
	P12Pass   string
}

func (h *handler) page(w http.ResponseWriter, r *http.Request) {
	d := pageData{
		CSS:       template.CSS(h.css),   //nolint:gosec // static stylesheet
		Script:    template.JS(h.script), //nolint:gosec // static script, host validated in New
		Host:      h.host,
		Version:   h.opts.Version,
		Source:    sourceURL,
		ProxyAddr: h.opts.ProxyAddr,
		CN:        h.cert.Subject.CommonName,
		Finger:    h.finger,
		NotBefore: h.notBefore,
		NotAfter:  h.notAfter,
		QR:        h.qrSVG,
		Platforms: platforms(h.cert.Subject.CommonName, r.UserAgent()),
		P12Pass:   P12Password,
	}
	if h.opts.Me != nil {
		m := h.opts.Me(r)
		d.Me = &m
	}
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, d); err != nil {
		h.text(w, r, http.StatusInternalServerError, "error\n")
		return
	}
	w.Header().Set("Content-Security-Policy", h.pageCSP)
	h.send(w, r, http.StatusOK, mimeHTML, buf.Bytes())
}

// detect returns the ids of the platform sections that match the User-Agent.
func detect(ua string) map[string]bool {
	l := strings.ToLower(ua)
	out := map[string]bool{}
	switch {
	case strings.Contains(l, "android"):
		out["android"] = true
	case strings.Contains(l, "iphone"), strings.Contains(l, "ipad"), strings.Contains(l, "ipod"):
		out["ios"] = true
	case strings.Contains(l, "windows"):
		out["windows"] = true
	case strings.Contains(l, "macintosh"), strings.Contains(l, "mac os x"):
		out["macos"] = true
	case strings.Contains(l, "linux"), strings.Contains(l, "x11"), strings.Contains(l, "cros"):
		out["linux"] = true
	}
	if strings.Contains(l, "firefox/") || strings.Contains(l, "midori") || strings.Contains(l, "librewolf") {
		if !strings.Contains(l, "fxios") {
			out["firefox"] = true
		}
	}
	return out
}

func platforms(cn, ua string) []platform {
	open := detect(ua)
	ps := []platform{
		{
			ID: "android", Title: "Android",
			Lead: "Android 7 or later. Menu names differ between makers. If you cannot find an item, search for \"CA certificate\" in Settings.",
			Steps: []step{
				{Text: "Download midopress-ca.crt with the first green button above. If the browser warns that this kind of file can be harmful, keep it."},
				{Text: "Open Settings > Security & privacy > More security & privacy > Encryption & credentials > Install a certificate > CA certificate. On Android 10 and older it is Settings > Security > Advanced > Encryption & credentials > Install from storage. On Samsung it is Settings > Security and privacy > More security settings > Install from device storage > CA certificate."},
				{Text: "Android warns that your network may be monitored. Tap Install anyway."},
				{Text: "Choose midopress-ca.crt in Downloads. Confirm with your PIN, pattern or fingerprint if asked. A screen lock must be set."},
				{Text: "Reload this page. The box at the top should say Installed and trusted."},
			},
			Note: "Chrome, Edge, Samsung Internet and other browsers trust it. Since Android 7 most other apps ignore certificates you install, so they are not decrypted. That is normal. Firefox-based browsers use their own list: if the box still says Not installed yet, see the Firefox section below.",
			Undo: "Settings > Security & privacy > More security & privacy > Encryption & credentials > Trusted credentials > User tab > MidoPress CA > Remove.",
		},
		{
			ID: "ios", Title: "iPhone and iPad",
			Lead: "Use Safari. Other browsers cannot install profiles.",
			Steps: []step{
				{Text: "In Safari, tap the first green button above. Tap Allow when it asks to download a configuration profile, then Close."},
				{Text: "Open Settings. Tap Profile Downloaded near the top (or General > VPN & Device Management) and choose MidoPress CA."},
				{Text: "Tap Install, enter your passcode, then tap Install twice more."},
				{Text: "Open Settings > General > About > Certificate Trust Settings. Under Enable full trust for root certificates, switch on MidoPress CA and tap Continue."},
				{Text: "Come back to Safari and reload this page."},
			},
			Note: "Without the last trust step the certificate is installed but not trusted. Apps that pin their own certificates will keep working without compression.",
			Undo: "Settings > General > VPN & Device Management > MidoPress CA > Remove Profile.",
		},
		{
			ID: "windows", Title: "Windows",
			Lead: "Windows 10 and 11. Chrome, Edge and Opera use the Windows store.",
			Steps: []step{
				{Text: "Download midopress-ca.crt with the second green button above."},
				{Text: "Double-click the file, then Install Certificate. Choose Current User (only you) or Local Machine (everyone, needs administrator rights). Click Next."},
				{Text: "Choose Place all certificates in the following store > Browse > Trusted Root Certification Authorities > OK > Next > Finish. Confirm the security warning with Yes."},
				{Text: "Restart the browser and reload this page."},
				{Text: "Or, in a terminal opened as administrator, in the folder with the file:", Code: "certutil -addstore -f ROOT midopress-ca.crt"},
			},
			Note: "Firefox ignores the Windows store by default. See the Firefox section below.",
			Undo: "Run certmgr.msc (or certlm.msc), open Trusted Root Certification Authorities > Certificates, right-click MidoPress CA > Delete. Or as administrator: certutil -delstore ROOT \"" + cn + "\"",
		},
		{
			ID: "macos", Title: "macOS",
			Lead: "Safari, Chrome and Edge use the system keychain.",
			Steps: []step{
				{Text: "Download midopress-ca.crt with the second green button above."},
				{Text: "Double-click the file. Keychain Access opens. Add it to System (everyone) or login (only you) and enter your password."},
				{Text: "In Keychain Access, find MidoPress CA and double-click it. Expand Trust. Next to When using this certificate, choose Always Trust. Close the window and enter your password."},
				{Text: "Restart the browser and reload this page."},
				{Text: "Or in Terminal, in the folder with the file:", Code: "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain midopress-ca.crt"},
			},
			Note: "Firefox keeps its own list. See the Firefox section below.",
			Undo: "Open Keychain Access, select MidoPress CA and press Delete. Or in Terminal: sudo security delete-certificate -c \"" + cn + "\" /Library/Keychains/System.keychain",
		},
		{
			ID: "linux", Title: "Linux",
			Lead: "This covers command-line tools and the system store. Firefox and Chromium may use their own stores.",
			Steps: []step{
				{Text: "Download midopress-ca.crt with the second green button above."},
				{Text: "Debian, Ubuntu and Mint, in the folder with the file:", Code: "sudo cp midopress-ca.crt /usr/local/share/ca-certificates/midopress-ca.crt\nsudo update-ca-certificates"},
				{Text: "Fedora, RHEL and derivatives:", Code: "sudo cp midopress-ca.crt /etc/pki/ca-trust/source/anchors/\nsudo update-ca-trust"},
				{Text: "Arch:", Code: "sudo trust anchor --store midopress-ca.crt"},
				{Text: "Chromium and Chrome keep certificates in a per-user database. Open chrome://settings/certificates, choose Authorities > Import, and pick the file. Firefox: see the section below."},
			},
			Undo: "Delete the file from /usr/local/share/ca-certificates and run sudo update-ca-certificates --fresh (Debian), or delete it from /etc/pki/ca-trust/source/anchors and run sudo update-ca-trust (Fedora).",
		},
		{
			ID: "firefox", Title: "Firefox and Midori",
			Lead: "Firefox, Midori and other Firefox-based browsers keep their own list of certificate authorities, so installing in the system is not enough.",
			Steps: []step{
				{Text: "Download midopress-ca.crt with the second green button above."},
				{Text: "Open Settings > Privacy & Security. Scroll to Certificates and click View Certificates."},
				{Text: "Open the Authorities tab and click Import. Pick midopress-ca.crt."},
				{Text: "Tick Trust this CA to identify websites and click OK."},
				{Text: "Reload this page."},
			},
			Note: "Instead, you can make Firefox use the system store: open about:config, search for security.enterprise_roots.enabled and set it to true. On Android, Firefox-based browsers read the certificates you installed in Android only if the browser option for third-party (user) CA certificates is on. Look for it in the browser settings; on some versions it is under Secret Settings.",
			Undo: "Settings > Privacy & Security > View Certificates > Authorities > MidoPress CA > Delete or Distrust.",
		},
	}
	for i := range ps {
		ps[i].Open = open[ps[i].ID]
	}
	return ps
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	f := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		f /= unit
		if f < unit || u == "TB" {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return ""
}

func buildScript(host string) string {
	return `(function(){
var box=document.getElementById("status"),txt=document.getElementById("status-text");
if(box&&txt&&window.fetch){
var set=function(s,m){box.setAttribute("data-state",s);txt.textContent=m};
var ctl=window.AbortController?new AbortController():null;
var timer=setTimeout(function(){if(ctl){ctl.abort()}},8000);
set("checking","Checking...");
fetch("https://` + host + `/check",{mode:"no-cors",cache:"no-store",signal:ctl?ctl.signal:undefined}).then(function(){
clearTimeout(timer);set("ok","Installed and trusted. You can close this page.");
},function(){
clearTimeout(timer);set("no","Not installed yet. Follow the steps below, then reload this page.");
});
}
var id=location.hash.replace("#","");
if(id){var el=document.getElementById(id);if(el&&el.tagName==="DETAILS"){el.open=true}}
})();`
}

var funcs = template.FuncMap{"human": humanBytes}

var pageTmpl = template.Must(template.New("page").Funcs(funcs).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="robots" content="noindex">
<title>MidoPress - install the certificate</title>
<style>{{.CSS}}</style>
</head>
<body>
<header class="top">
<div class="wrap">
<p class="brand">MidoPress</p>
<h1>Install the MidoPress certificate</h1>
<p class="lead">This certificate lets this device trust the MidoPress proxy for secure (HTTPS) sites, so pages can be made smaller.</p>
</div>
</header>
<main class="wrap">

<section class="card" aria-labelledby="h-status">
<h2 id="h-status">Status</h2>
<div id="status" class="status" data-state="unknown" role="status" aria-live="polite">
<p id="status-text">Install it with the steps below, then reload this page.</p>
</div>
{{if .ProxyAddr}}<p class="small">This device must use the proxy {{.ProxyAddr}}. You are seeing this page because it already does.</p>{{end}}
</section>

<section class="card" aria-labelledby="h-get">
<h2 id="h-get">1. Download</h2>
<p><a class="btn" href="/ca.crt" download="midopress-ca.crt">Android and iPhone/iPad (.crt)</a></p>
<p><a class="btn" href="/ca.crt" download="midopress-ca.crt">Windows, macOS, Linux (.crt)</a></p>
<p class="alt">Other formats:
<a href="/ca.der" download="midopress-ca.der">.der (binary)</a>,
<a href="/ca.p12" download="midopress-ca.p12">.p12 (password {{.P12Pass}})</a>.
The .p12 file holds only the certificate. Most devices need the .crt.</p>
<div class="qrbox">
<div class="qr" aria-hidden="true">{{.QR}}</div>
<p class="small">Scan this code with another device that uses this proxy to download the .crt there. Or open <strong>http://{{.Host}}/ca.crt</strong>.</p>
</div>
</section>

<section class="card" aria-labelledby="h-steps">
<h2 id="h-steps">2. Install</h2>
<p>Open the section for your system.</p>
{{range .Platforms}}
<details class="plat" id="{{.ID}}"{{if .Open}} open{{end}}>
<summary>{{.Title}}</summary>
<div class="body">
<p>{{.Lead}}</p>
<ol>
{{range .Steps}}<li>{{.Text}}{{if .Code}}<pre><code>{{.Code}}</code></pre>{{end}}</li>
{{end}}</ol>
{{if .Note}}<p class="note">{{.Note}}</p>{{end}}
<p class="undo"><strong>Remove it:</strong> {{.Undo}}</p>
</div>
</details>
{{end}}
</section>

{{if .Me}}
<section class="card" aria-labelledby="h-me">
<h2 id="h-me">This device</h2>
<dl class="me">
{{if .Me.Device}}<dt>Device</dt><dd>{{.Me.Device}}</dd>{{end}}
{{if .Me.Profile}}<dt>Profile</dt><dd>{{.Me.Profile}}</dd>{{end}}
<dt>Requests</dt><dd>{{.Me.Requests}}</dd>
<dt>Downloaded</dt><dd>{{human .Me.BytesIn}}</dd>
<dt>Sent to you</dt><dd>{{human .Me.BytesOut}}</dd>
<dt>Saved</dt><dd>{{human .Me.Saved}}</dd>
</dl>
{{if .Me.CATrustedHint}}<p class="small">{{.Me.CATrustedHint}}</p>{{end}}
</section>
{{end}}

<section class="card" aria-labelledby="h-about">
<h2 id="h-about">What you are trusting</h2>
<p>With this certificate installed, this proxy can read and compress your HTTPS traffic. Install it only on devices you own and that use this proxy.</p>
<p>The private key never leaves the server. This page only serves the public certificate.</p>
<dl class="fp">
<dt>Name</dt><dd>{{.CN}}</dd>
<dt>Valid</dt><dd>{{.NotBefore}} to {{.NotAfter}} (UTC)</dd>
<dt>SHA-256 fingerprint</dt><dd class="mono">{{.Finger}}</dd>
</dl>
<p class="small">Compare the fingerprint with the one your device shows before you trust it.</p>
</section>

</main>
<footer class="wrap foot">
<p>MidoPress{{if .Version}} {{.Version}}{{end}} - <a href="{{.Source}}">source code</a></p>
</footer>
<script>{{.Script}}</script>
</body>
</html>
`))

var notFoundTmpl = template.Must(template.New("404").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Not found - MidoPress</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="wrap">
<section class="card">
<p class="brand">MidoPress</p>
<h1>Nothing here</h1>
<p>This address does not exist on {{.Host}}.</p>
<p><a class="btn" href="/">Install the certificate</a></p>
</section>
</main>
</body>
</html>
`))
