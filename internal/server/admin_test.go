package server_test

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"software.sslmate.com/src/go-pkcs12"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/server"
)

// adminAPI talks to the administration server of a harness.
type adminAPI struct {
	t     *testing.T
	base  string
	token string
	c     *http.Client
}

func (h *harness) admin(t *testing.T) *adminAPI {
	t.Helper()
	addr := h.srv.AdminAddr()
	require.NotNil(t, addr, "the administration server must be on")
	token, _ := h.srv.AdminToken()
	return &adminAPI{t: t, base: "http://" + addr.String(), token: token, c: &http.Client{Timeout: 30 * time.Second}}
}

func (a *adminAPI) do(method, path string, body any, token string) (*http.Response, []byte) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(a.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	require.NoError(a.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.c.Do(req)
	require.NoError(a.t, err)
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (a *adminAPI) call(method, path string, body any) (*http.Response, []byte) {
	return a.do(method, path, body, a.token)
}

// into calls and decodes, requiring the status.
func (a *adminAPI) into(method, path string, body any, want int, v any) {
	a.t.Helper()
	resp, data := a.call(method, path, body)
	require.Equal(a.t, want, resp.StatusCode, string(data))
	if v != nil {
		require.NoError(a.t, json.Unmarshal(data, v), string(data))
	}
}

type obj = map[string]any

func (a *adminAPI) obj(method, path string, body any, want int) obj {
	a.t.Helper()
	var o obj
	a.into(method, path, body, want, &o)
	return o
}

func TestAdminNeedsTheToken(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)

	resp, body := a.do("GET", "/api/v1/stats", nil, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, string(body), `"code":"unauthorized"`)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	resp, _ = a.do("GET", "/api/v1/stats", nil, "not-the-token")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp, _ = a.do("GET", "/api/v1/stats", nil, a.token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body = a.do("GET", "/api/v1/health", nil, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "liveness needs no token")
	assert.JSONEq(t, `{"status":"ok"}`, string(body))

	resp, body = a.do("GET", "/api/v1/nothing-here", nil, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, string(body), `"code":"not_found"`)

	resp, _ = a.do("GET", "/", nil, "")
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html", "the interface needs no token")
	assert.NotEmpty(t, resp.Header.Get("X-Content-Type-Options"))
}

func TestAdminSlowsDownGuessing(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)
	for range 8 {
		resp, _ := a.do("GET", "/api/v1/about", nil, "guess")
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
	resp, _ := a.do("GET", "/api/v1/about", nil, "guess")
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
	resp, _ = a.do("GET", "/api/v1/about", nil, a.token)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "even the right token waits")
}

func TestAdminTokenIsKeptAndPrivate(t *testing.T) {
	h := newHarness(t, withAdmin())
	token, created := h.srv.AdminToken()
	assert.True(t, created)
	assert.Len(t, token, 64)
	path := h.cfg.Web.TokenPath()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, _ := os.ReadFile(path)
	assert.Equal(t, token, strings.TrimSpace(string(raw)))
}

func TestAdminOffByConfiguration(t *testing.T) {
	h := newHarness(t)
	assert.Nil(t, h.srv.AdminAddr())
}

func TestStatsFollowTheTraffic(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)
	c := h.client()
	h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip")
	h.get(c, h.plainURL("/html"), "Accept-Encoding", "br")
	h.get(c, h.plainURL("/image/jpeg"))
	h.get(c, h.plainURL("/status/204"))

	var s struct {
		Totals struct {
			Requests, Transformed uint64
			BytesIn               uint64 `json:"bytes_in"`
			BytesOut              uint64 `json:"bytes_out"`
			Saved                 int64
			SavedPercent          float64 `json:"saved_percent"`
		}
		ByKind      map[string]struct{ Requests, Transformed uint64 } `json:"by_kind"`
		ByCodec     map[string]struct{ Requests uint64 }              `json:"by_codec"`
		ByProfile   map[string]struct{ Requests uint64 }              `json:"by_profile"`
		ByDecision  map[string]uint64                                 `json:"by_decision"`
		TopDomains  *[]struct{ Domain string }                        `json:"top_domains"`
		Series      struct{ Seconds, Minutes []struct{ T int64 } }
		Connections struct {
			ClientsActive int `json:"clients_active"`
		}
		Version string
		Cache   struct{ Hits, Misses uint64 }
		CPU     struct{ Slots int }
	}
	a.into("GET", "/api/v1/stats", nil, 200, &s)
	assert.GreaterOrEqual(t, s.Totals.Requests, uint64(4))
	assert.GreaterOrEqual(t, s.Totals.Transformed, uint64(3))
	assert.Positive(t, s.Totals.Saved)
	assert.Positive(t, s.Totals.SavedPercent)
	assert.Contains(t, s.ByKind, "text")
	assert.Contains(t, s.ByKind, "image")
	assert.Contains(t, s.ByCodec, "br")
	assert.Contains(t, s.ByProfile, "balanced")
	assert.Positive(t, s.ByDecision["transformed"])
	assert.Nil(t, s.TopDomains, "domains are not aggregated unless asked")
	assert.Len(t, s.Series.Seconds, 120)
	assert.Len(t, s.Series.Minutes, 180)
	assert.Equal(t, 1, s.Connections.ClientsActive)
	assert.Positive(t, s.CPU.Slots)

	var set struct{ Flows, Domains, Capture bool }
	a.into("PUT", "/api/v1/settings", obj{"domains": true}, 200, &set)
	assert.True(t, set.Domains)
	h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip")
	a.into("GET", "/api/v1/stats", nil, 200, &s)
	require.NotNil(t, s.TopDomains)
	require.NotEmpty(t, *s.TopDomains)
	assert.Equal(t, "example.com", (*s.TopDomains)[0].Domain)

	a.call("PUT", "/api/v1/settings", obj{"domains": false})
	a.into("GET", "/api/v1/stats", nil, 200, &s)
	assert.Nil(t, s.TopDomains, "turning it off throws the numbers away")

	a.call("POST", "/api/v1/service/stats/reset", nil)
	a.into("GET", "/api/v1/stats", nil, 200, &s)
	assert.Zero(t, s.Totals.Requests)
}

func TestFlowsAreOffUntilAskedForAndKeepNoSecrets(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)
	c := h.client()

	var list struct {
		Enabled  bool
		Capacity int
		Total    int
		Flows    []map[string]any
	}
	h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip")
	a.into("GET", "/api/v1/flows", nil, 200, &list)
	assert.False(t, list.Enabled)
	assert.Empty(t, list.Flows, "nothing was kept while it was off")

	a.call("PUT", "/api/v1/settings", obj{"flows": true})
	h.get(c, h.plainURL("/html?secret=1"), "Accept-Encoding", "gzip", "Cookie", "session=abc", "Authorization", "Bearer xyz")
	h.get(c, h.plainURL("/image/jpeg"))
	h.get(c, h.plainURL("/status/204"))
	a.into("GET", "/api/v1/flows", nil, 200, &list)
	require.True(t, list.Enabled)
	assert.Equal(t, 500, list.Capacity)
	require.Len(t, list.Flows, 3)
	assert.Equal(t, "/status/204", list.Flows[0]["path"], "newest first")

	var htmlFlow map[string]any
	for _, f := range list.Flows {
		if strings.HasPrefix(f["path"].(string), "/html") {
			htmlFlow = f
		}
	}
	require.NotNil(t, htmlFlow)
	assert.Equal(t, "example.com", htmlFlow["host"])
	assert.Equal(t, "transformed", htmlFlow["decision"])
	assert.Equal(t, "gzip", htmlFlow["codec"])
	assert.Equal(t, "text", htmlFlow["kind"])
	assert.Equal(t, "balanced", htmlFlow["profile"])
	reqH := htmlFlow["request_headers"].(map[string]any)
	assert.Equal(t, []any{"<hidden>"}, reqH["Cookie"])
	assert.Equal(t, []any{"<hidden>"}, reqH["Authorization"])
	raw, _ := json.Marshal(htmlFlow)
	assert.NotContains(t, string(raw), "session=abc")

	t.Run("filters", func(t *testing.T) {
		a.into("GET", "/api/v1/flows?filter="+urlEscape(`~t image/`), nil, 200, &list)
		require.Len(t, list.Flows, 1)
		assert.Equal(t, "image", list.Flows[0]["kind"])
		a.into("GET", "/api/v1/flows?kind=text", nil, 200, &list)
		assert.Len(t, list.Flows, 1)
		a.into("GET", "/api/v1/flows?limit=1", nil, 200, &list)
		assert.Len(t, list.Flows, 1)
		bad := a.obj("GET", "/api/v1/flows?filter="+urlEscape(`~d (`), nil, 422)
		assert.Equal(t, "invalid", bad["code"])
		assert.NotEmpty(t, bad["issues"])
		a.obj("GET", "/api/v1/flows?kind=video", nil, 422)
		a.obj("GET", "/api/v1/flows?limit=0", nil, 422)
	})

	t.Run("an image can be compared with what it became", func(t *testing.T) {
		a.into("GET", "/api/v1/flows?kind=image", nil, 200, &list)
		require.Len(t, list.Flows, 1)
		f := list.Flows[0]
		id := f["id"].(string)
		assert.Equal(t, true, f["has_original"])
		assert.Equal(t, true, f["has_result"])
		resp, orig := a.call("GET", "/api/v1/flows/"+id+"/original", nil)
		require.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, fix().jpeg, orig)
		assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "sandbox")
		resp, res := a.call("GET", "/api/v1/flows/"+id+"/transformed", nil)
		require.Equal(t, 200, resp.StatusCode)
		assert.Less(t, len(res), len(orig))
		assert.Equal(t, f["out_content_type"], resp.Header.Get("Content-Type"))

		cmp := a.obj("GET", "/api/v1/flows/"+id+"/compare", nil, 200)
		assert.Equal(t, "dssim", cmp["metric"])
		assert.Equal(t, true, cmp["comparable"])
		assert.Positive(t, cmp["distance"])
		assert.Less(t, cmp["distance"], 0.05)
		assert.EqualValues(t, 320, cmp["width"])

		a.obj("GET", "/api/v1/flows/"+id, nil, 200)
		a.obj("GET", "/api/v1/flows/nope", nil, 404)
		resp, _ = a.call("GET", "/api/v1/flows/nope/original", nil)
		assert.Equal(t, 404, resp.StatusCode)
	})

	t.Run("HAR", func(t *testing.T) {
		resp, data := a.call("GET", "/api/v1/flows.har", nil)
		require.Equal(t, 200, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Disposition"), "midopress.har")
		var har struct {
			Log struct {
				Version string
				Entries []struct {
					Request struct{ URL string }
				}
			}
		}
		require.NoError(t, json.Unmarshal(data, &har))
		assert.Equal(t, "1.2", har.Log.Version)
		require.Len(t, har.Log.Entries, 3)
		assert.Equal(t, "http://example.com/html?secret=1", har.Log.Entries[0].Request.URL)
	})

	t.Run("clearing and turning off", func(t *testing.T) {
		resp, _ := a.call("DELETE", "/api/v1/flows", nil)
		assert.Equal(t, 204, resp.StatusCode)
		a.into("GET", "/api/v1/flows", nil, 200, &list)
		assert.Empty(t, list.Flows)
		h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip")
		a.call("PUT", "/api/v1/settings", obj{"flows": false})
		a.into("GET", "/api/v1/flows", nil, 200, &list)
		assert.False(t, list.Enabled)
		assert.Empty(t, list.Flows, "turning it off forgets what was there")
		var set struct{ Capture bool }
		a.into("GET", "/api/v1/settings", nil, 200, &set)
		assert.False(t, set.Capture)
	})
}

func urlEscape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", "&", "%26", "|", "%7C", "~", "%7E", "(", "%28", ")", "%29", "+", "%2B", "#", "%23")
	return r.Replace(s)
}

func TestConfigChangesApplyAtOnceAndAreSaved(t *testing.T) {
	f := fix()
	h := newHarness(t, withAdmin(), withConfig(func(c *config.Config) { c.Auth.Pass = "hunter2" }))
	a := h.admin(t)
	c := h.client()

	var view struct {
		DefaultProfile string `json:"default_profile"`
		Profiles       []struct {
			Name        string
			Builtin     bool
			Modified    bool
			TargetDSSIM float64 `json:"target_dssim"`
			Formats     []string
		}
		Rules       []map[string]any
		StateFile   string `json:"state_file"`
		ManagedByUI struct {
			DefaultProfile bool `json:"default_profile"`
			Profiles       bool
			Rules          bool
		} `json:"managed_by_ui"`
		Config map[string]any
	}
	a.into("GET", "/api/v1/config", nil, 200, &view)
	assert.Equal(t, "balanced", view.DefaultProfile)
	names := []string{}
	for _, p := range view.Profiles {
		names = append(names, p.Name)
		assert.True(t, p.Builtin)
	}
	assert.Equal(t, []string{"balanced", "extreme", "lossless", "saver"}, names)
	assert.False(t, view.ManagedByUI.Rules)
	assert.Equal(t, filepath.Join(h.cfg.Web.StateDir, "ui.yaml"), view.StateFile)
	assert.Equal(t, "<redacted>", view.Config["auth"].(map[string]any)["pass"], "secrets do not leave")
	_, raw := a.call("GET", "/api/v1/config", nil)
	assert.NotContains(t, string(raw), "hunter2")

	hints := []string{"Sec-CH-Viewport-Width", "100", "Sec-CH-DPR", "1"}
	_, balanced := h.get(c, h.plainURL("/image/jpeg"), hints...)

	t.Run("the default profile changes without a restart", func(t *testing.T) {
		a.into("PUT", "/api/v1/config/default-profile", obj{"profile": "extreme"}, 200, &view)
		assert.Equal(t, "extreme", view.DefaultProfile)
		assert.True(t, view.ManagedByUI.DefaultProfile)
		_, extreme := h.get(c, h.plainURL("/image/jpeg"), hints...)
		assert.Less(t, len(extreme), len(balanced)/2, "the next response already uses the new profile")
		assert.FileExists(t, view.StateFile)
		a.obj("PUT", "/api/v1/config/default-profile", obj{"profile": "ghost"}, 422)
	})

	t.Run("a profile can be changed and a new one defined", func(t *testing.T) {
		a.into("PUT", "/api/v1/config/profiles/balanced", obj{"target_dssim": 0.0015, "formats": []string{"webp"}}, 200, &view)
		for _, p := range view.Profiles {
			if p.Name == "balanced" {
				assert.True(t, p.Modified)
				assert.Equal(t, 0.0015, p.TargetDSSIM)
				assert.Equal(t, []string{"webp"}, p.Formats)
			}
		}
		a.into("PUT", "/api/v1/config/profiles/mobile", obj{"base": "saver", "animated_to_still": true}, 200, &view)
		assert.Len(t, view.Profiles, 5)
		assert.True(t, view.ManagedByUI.Profiles)

		bad := a.obj("PUT", "/api/v1/config/profiles/mobile", obj{"target_dssim": 7}, 422)
		assert.NotEmpty(t, bad["issues"])
		a.obj("PUT", "/api/v1/config/profiles/Bad%20Name", obj{}, 422)
		a.obj("PUT", "/api/v1/config/profiles/x", obj{"base": "nope"}, 422)

		a.obj("DELETE", "/api/v1/config/profiles/extreme", nil, 200)
		a.obj("DELETE", "/api/v1/config/profiles/nothing", nil, 404)
		resp, _ := a.call("DELETE", "/api/v1/config/profiles/mobile", nil)
		assert.Equal(t, 200, resp.StatusCode)
		a.into("GET", "/api/v1/config", nil, 200, &view)
		assert.Len(t, view.Profiles, 4)
	})

	t.Run("rules are checked, applied and saved", func(t *testing.T) {
		a.into("PUT", "/api/v1/config/default-profile", obj{"profile": "balanced"}, 200, &view)
		rules := []obj{{"match": `~t image/`, "action": "passthrough"}}
		a.into("PUT", "/api/v1/config/rules", rules, 200, &view)
		require.Len(t, view.Rules, 1)
		assert.Equal(t, "passthrough", view.Rules[0]["action"])
		_, body := h.get(c, h.plainURL("/image/jpeg"))
		assert.Equal(t, f.jpeg, body, "the rule works from the next request")

		raw, err := os.ReadFile(view.StateFile)
		require.NoError(t, err)
		assert.Contains(t, string(raw), "passthrough")

		bad := a.obj("PUT", "/api/v1/config/rules", []obj{{"match": `~d (`, "action": "passthrough"}, {"match": "~all"}, {"match": "~u /x", "action": "passthrough_tls"}}, 422)
		issues := bad["issues"].([]any)
		require.GreaterOrEqual(t, len(issues), 3)
		first := issues[0].(map[string]any)
		assert.EqualValues(t, 0, first["index"])
		assert.Equal(t, "match", first["field"])
		assert.Positive(t, first["column"])
		_, body = h.get(c, h.plainURL("/image/jpeg"))
		assert.Equal(t, f.jpeg, body, "a refused change leaves the rules as they were")

		a.obj("PUT", "/api/v1/config/rules", []obj{{"match": "~all", "set": obj{"colour": 1}}}, 422)
		a.obj("PUT", "/api/v1/config/rules", []obj{{"match": "~all", "set": obj{"quality": 300}}}, 422)
		a.obj("PUT", "/api/v1/config/rules", []obj{{"match": "~all", "profile": "ghost"}}, 422)
		a.into("PUT", "/api/v1/config/rules", []obj{{"match": "~all", "set": obj{"quality": 60}}}, 200, &view)
		assert.EqualValues(t, 60, view.Rules[0]["set"].(map[string]any)["quality"])

		var vr struct {
			OK     bool
			Issues []map[string]any
		}
		a.into("POST", "/api/v1/config/validate", obj{"rules": []obj{{"match": "~d ("}}}, 200, &vr)
		assert.False(t, vr.OK)
		require.Len(t, vr.Issues, 1)
		a.into("POST", "/api/v1/config/validate", obj{"rules": []obj{{"match": "~d example.com", "profile": "saver"}}}, 200, &vr)
		assert.True(t, vr.OK)
		assert.Empty(t, vr.Issues)
	})

	t.Run("what was saved is there after a restart, and can be forgotten", func(t *testing.T) {
		ov, err := config.LoadOverrides(view.StateFile)
		require.NoError(t, err)
		require.NotNil(t, ov)
		require.NotNil(t, ov.Rules)
		a.into("DELETE", "/api/v1/config/ui", nil, 200, &view)
		assert.NoFileExists(t, view.StateFile)
		assert.Empty(t, view.Rules)
		assert.False(t, view.ManagedByUI.Rules)
		_, body := h.get(c, h.plainURL("/image/jpeg"))
		assert.Less(t, len(body), len(f.jpeg))
	})
}

func TestTestAUrl(t *testing.T) {
	h := newHarness(t, withAdmin(), withConfig(func(c *config.Config) {
		c.Rules = nil
	}))
	a := h.admin(t)
	a.call("PUT", "/api/v1/config/rules", []obj{
		{"match": `~d (bank|paypal)\.`, "action": "passthrough_tls"},
		{"match": `~d cdn\.example\.com & ~t image/`, "profile": "extreme"},
		{"match": `~u /app\.js$`, "set": obj{"minify_js": true}},
		{"match": `~u \.(mp4|webm)$`, "action": "passthrough"},
	})
	var res struct {
		Explanations []struct {
			Index   int
			Matched bool
			Reason  string
		}
		Action        string
		Profile       string
		ProfileSource string `json:"profile_source"`
		Set           map[string]any
		Outcome       string
		Note          string
	}
	test := func(body obj) {
		a.into("POST", "/api/v1/config/test", body, 200, &res)
	}

	test(obj{"url": "https://cdn.example.com/p.jpg", "content_type": "image/jpeg"})
	assert.Equal(t, "extreme", res.Profile)
	assert.Equal(t, "rule", res.ProfileSource)
	assert.Equal(t, "process", res.Outcome)
	require.Len(t, res.Explanations, 4)
	assert.True(t, res.Explanations[1].Matched)
	assert.False(t, res.Explanations[0].Matched)
	assert.NotEmpty(t, res.Explanations[0].Reason)

	test(obj{"url": "https://cdn.example.com/p.jpg", "content_type": "image/jpeg", "headers": obj{"X-MidoPress-Profile": "lossless"}})
	assert.Equal(t, "lossless", res.Profile)
	assert.Equal(t, "header", res.ProfileSource, "the header wins over a rule")

	test(obj{"url": "https://cdn.example.com/p.jpg", "content_type": "image/jpeg", "device_profile": "saver", "headers": obj{"X-MidoPress-Profile": "lossless"}})
	assert.Equal(t, "saver", res.Profile)
	assert.Equal(t, "device", res.ProfileSource, "and the device wins over the header")

	test(obj{"url": "https://other.test/x.html", "content_type": "text/html", "headers": obj{"Save-Data": "on"}})
	assert.Equal(t, "saver", res.Profile)
	assert.Equal(t, "save-data", res.ProfileSource)

	test(obj{"url": "https://other.test/x.html", "content_type": "text/html"})
	assert.Equal(t, "balanced", res.Profile)
	assert.Equal(t, "default", res.ProfileSource)

	test(obj{"url": "https://media.test/film.mp4", "content_type": "video/mp4"})
	assert.Equal(t, "passthrough", res.Action)
	assert.Equal(t, "passthrough", res.Outcome)

	test(obj{"url": "https://site.test/app.js", "content_type": "text/javascript"})
	assert.Equal(t, true, res.Set["minify_js"])

	test(obj{"url": "https://site.test/app.js"})
	assert.Contains(t, res.Note, "content type", "without a response only the request side can be tried")

	a.obj("POST", "/api/v1/config/test", obj{"url": "not a url"}, 422)
	a.obj("POST", "/api/v1/config/test", obj{"url": "ftp://x/y"}, 422)
	a.obj("POST", "/api/v1/config/test", obj{"url": "https://x.test/", "src": "nope"}, 422)
}

func TestDevicesWithTokens(t *testing.T) {
	devices := filepath.Join(t.TempDir(), "devices.json")
	_, err := auth.OpenStore(devices, true)
	require.NoError(t, err)
	h := newHarness(t, withAdmin(), withConfig(func(c *config.Config) { c.Auth.Mode, c.Auth.DevicesFile = "token", devices }))
	a := h.admin(t)

	var list []map[string]any
	a.into("GET", "/api/v1/devices", nil, 200, &list)
	assert.Empty(t, list)

	nd := a.obj("POST", "/api/v1/devices", obj{"id": "phone", "profile": "extreme", "host": "proxy.example.org"}, 201)
	token := nd["token"].(string)
	assert.GreaterOrEqual(t, len(token), 32)
	url := nd["config_url"].(string)
	assert.True(t, strings.HasPrefix(url, "midopress://config?"))
	assert.Contains(t, url, "host=proxy.example.org")
	assert.Contains(t, url, "user=phone")
	assert.Contains(t, url, "token="+token)
	assert.Contains(t, nd["config_qr_svg"], "<svg")

	a.obj("POST", "/api/v1/devices", obj{"id": "phone"}, 409)
	a.obj("POST", "/api/v1/devices", obj{"id": "bad id!"}, 422)
	a.obj("POST", "/api/v1/devices", obj{"id": "x", "profile": "ghost"}, 422)

	// it works at once, no waiting for the file to be read again
	resp, _ := h.get(h.viaUser("phone", token), h.plainURL("/status/204"))
	assert.Equal(t, 204, resp.StatusCode)
	resp, _ = h.get(h.viaUser("phone", "wrong"), h.plainURL("/status/204"))
	assert.Equal(t, 407, resp.StatusCode)

	a.into("GET", "/api/v1/devices", nil, 200, &list)
	require.Len(t, list, 1)
	assert.Equal(t, "phone", list[0]["id"])
	assert.Equal(t, "extreme", list[0]["profile"])
	assert.Equal(t, true, list[0]["active"])
	assert.NotNil(t, list[0]["last_seen"])
	assert.NotContains(t, list[0], "token")

	var clients []map[string]any
	a.into("GET", "/api/v1/clients", nil, 200, &clients)
	require.NotEmpty(t, clients)
	assert.Equal(t, "phone", clients[0]["device"])
	assert.Equal(t, "extreme", clients[0]["profile"])

	d := a.obj("PATCH", "/api/v1/devices/phone", obj{"profile": "saver"}, 200)
	assert.Equal(t, "saver", d["profile"])
	a.obj("PATCH", "/api/v1/devices/ghost", obj{"profile": "saver"}, 404)
	a.obj("PATCH", "/api/v1/devices/phone", obj{"profile": "ghost"}, 422)

	resp2, _ := a.call("DELETE", "/api/v1/devices/phone", nil)
	assert.Equal(t, 204, resp2.StatusCode)
	resp, _ = h.get(h.viaUser("phone", token), h.plainURL("/status/204"))
	assert.Equal(t, 407, resp.StatusCode, "a revoked device stops at once")
	resp2, _ = a.call("DELETE", "/api/v1/devices/phone", nil)
	assert.Equal(t, 404, resp2.StatusCode)
}

func TestDevicesNeedTokenAuthentication(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)
	e := a.obj("GET", "/api/v1/devices", nil, 409)
	assert.Equal(t, "no_tokens", e["code"])
	a.obj("POST", "/api/v1/devices", obj{"id": "x"}, 409)
}

func TestCertificateDownloadsAndThePageOfTheMagicHost(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)

	info := a.obj("GET", "/api/v1/ca", nil, 200)
	assert.Equal(t, true, info["available"])
	assert.Equal(t, "midopress.it", info["magic_host"])
	assert.Equal(t, "http://midopress.it/", info["page_url"])
	assert.Len(t, info["sha256"], 64)

	resp, pemBytes := a.call("GET", "/api/v1/ca.pem", nil)
	require.Equal(t, 200, resp.StatusCode)
	block, _ := pem.Decode(pemBytes)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.True(t, cert.IsCA)
	_, der := a.call("GET", "/api/v1/ca.der", nil)
	assert.Equal(t, block.Bytes, der)
	_, p12 := a.call("GET", "/api/v1/ca.p12", nil)
	certs, err := pkcs12.DecodeTrustStore(p12, "midopress")
	require.NoError(t, err)
	assert.Len(t, certs, 1)
	assert.False(t, bytes.Contains(pemBytes, []byte("PRIVATE KEY")), "the key never leaves")

	c := h.client()
	t.Run("the page, through the proxy and never forwarded", func(t *testing.T) {
		before := h.srv.Proxy().Stats().Requests
		resp, body := h.get(c, "http://midopress.it/")
		require.Equal(t, 200, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
		assert.Contains(t, strings.ToLower(string(body)), "midopress")
		assert.Contains(t, string(body), strings.ToUpper(info["sha256"].(string))[:2]+":"+strings.ToUpper(info["sha256"].(string))[2:4])
		assert.Equal(t, before, h.srv.Proxy().Stats().Requests, "it is answered by the proxy, not forwarded")
		assert.Zero(t, h.dials.Load(), "nothing went out to the Internet")

		resp, got := h.get(c, "http://midopress.it/ca.crt")
		require.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, pemBytes, got)
		resp, got = h.get(c, "http://MIDOPRESS.IT:80/ca.der")
		require.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, der, got)
		resp, got = h.get(c, "http://midopress.it/api/v1/me")
		require.Equal(t, 200, resp.StatusCode)
		assert.Contains(t, string(got), `"profile":"balanced"`)
	})

	t.Run("the trust check works over HTTPS because the proxy decrypts it", func(t *testing.T) {
		resp, body := h.get(c, "https://midopress.it/check")
		require.Equal(t, 200, resp.StatusCode)
		assert.JSONEq(t, `{"ok":true}`, string(body))
		assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	})

	t.Run("and a name that only looks like it is forwarded", func(t *testing.T) {
		resp, _ := h.get(c, "http://midopress.it.example.com/ca.crt")
		assert.Equal(t, 404, resp.StatusCode, "the test origin does not know that path")
		assert.NotZero(t, h.dials.Load())
	})
}

func TestNoCertificateWithoutInterception(t *testing.T) {
	h := newHarness(t, withAdmin(), withoutMITM())
	a := h.admin(t)
	info := a.obj("GET", "/api/v1/ca", nil, 200)
	assert.Equal(t, false, info["available"])
	a.obj("GET", "/api/v1/ca.pem", nil, 404)
}

func TestLogLevelChangesWhileRunning(t *testing.T) {
	level := new(slog.LevelVar)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}))
	h := newHarness(t, withAdmin(), withLogger(logger), withServerOption(server.WithLogLevel(level)))
	a := h.admin(t)
	var st struct {
		Level string
		Lines []map[string]any
	}
	a.into("GET", "/api/v1/log", nil, 200, &st)
	assert.Equal(t, "info", st.Level)

	a.into("PUT", "/api/v1/log", obj{"level": "debug"}, 200, &st)
	assert.Equal(t, "debug", st.Level)
	h.srv.Logger().Debug("a line for the test", "n", 1)
	a.into("GET", "/api/v1/log", nil, 200, &st)
	found := false
	for _, l := range st.Lines {
		if l["message"] == "a line for the test" {
			found = true
			assert.Equal(t, "DEBUG", l["level"])
			assert.Equal(t, "1", l["attrs"].(map[string]any)["n"])
		}
	}
	assert.True(t, found, "debug lines show once the level is debug")

	a.into("PUT", "/api/v1/log", obj{"level": "error"}, 200, &st)
	h.srv.Logger().Info("hidden line")
	a.into("GET", "/api/v1/log", nil, 200, &st)
	for _, l := range st.Lines {
		assert.NotEqual(t, "hidden line", l["message"], "below the level nothing is kept")
	}
	a.obj("PUT", "/api/v1/log", obj{"level": "loud"}, 422)
}

func TestTheEventStream(t *testing.T) {
	h := newHarness(t, withAdmin(), withLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	a := h.admin(t)
	req, _ := http.NewRequest("GET", a.base+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := (&http.Client{}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	events := make(chan [2]string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				events <- [2]string{name, strings.TrimPrefix(line, "data: ")}
			}
		}
		close(events)
	}()
	next := func(want string) string {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case e, ok := <-events:
				require.True(t, ok, "the stream ended")
				if e[0] == want {
					return e[1]
				}
			case <-deadline:
				t.Fatalf("no %q event", want)
			}
		}
	}

	var st struct {
		Series  struct{ Seconds []any }
		Version string
	}
	require.NoError(t, json.Unmarshal([]byte(next("stats")), &st))
	assert.Len(t, st.Series.Seconds, 120, "a stats event right away, the same shape as /stats")

	a.call("PUT", "/api/v1/settings", obj{"flows": true})
	var set struct{ Flows bool }
	require.NoError(t, json.Unmarshal([]byte(next("settings")), &set))
	assert.True(t, set.Flows)

	h.get(h.client(), h.plainURL("/html"), "Accept-Encoding", "gzip")
	var cl struct{ Key string }
	require.NoError(t, json.Unmarshal([]byte(next("client")), &cl))
	assert.NotEmpty(t, cl.Key)
	var f struct{ Host, Path string }
	require.NoError(t, json.Unmarshal([]byte(next("flow")), &f))
	assert.Equal(t, "example.com", f.Host)

	h.srv.Logger().Warn("something for the stream")
	var l struct{ Message string }
	require.NoError(t, json.Unmarshal([]byte(next("log")), &l))
	assert.Equal(t, "something for the stream", l.Message)
}

func TestServiceCommands(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)
	c := h.client()
	h.get(c, h.plainURL("/image/jpeg"))
	assert.Equal(t, 1, h.srv.CacheLen())

	for _, p := range []string{"/api/v1/service/reload", "/api/v1/service/cache/clear", "/api/v1/service/stats/reset"} {
		resp, _ := a.call("POST", p, nil)
		assert.Equal(t, 204, resp.StatusCode, p)
	}
	assert.Equal(t, 0, h.srv.CacheLen(), "the cache was emptied")

	var about struct {
		Version, Backend, AuthMode string
		Backends                   []string
		Modules                    []map[string]any
		Native                     map[string]bool
		MITM                       bool
	}
	a.into("GET", "/api/v1/about", nil, 200, &about)
	assert.Equal(t, "wasm", about.Backend)
	assert.Contains(t, about.Backends, "wasm")
	assert.NotEmpty(t, about.Modules)
	assert.Contains(t, about.Native, "avif")
	assert.True(t, about.MITM)

	resp, body := a.call("GET", "/api/v1/metrics", nil)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), "midopress_responses_total")
	resp, _ = a.do("GET", "/api/v1/metrics", nil, "")
	assert.Equal(t, 401, resp.StatusCode, "the metrics here need the token")

	resp, _ = a.call("POST", "/api/v1/service/shutdown", nil)
	assert.Equal(t, 202, resp.StatusCode)
	select {
	case err := <-h.done:
		assert.NoError(t, err)
		h.cancel = nil
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
}

func TestBadBodies(t *testing.T) {
	h := newHarness(t, withAdmin())
	a := h.admin(t)
	req, _ := http.NewRequest("PUT", a.base+"/api/v1/config/rules", strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)

	big := bytes.Repeat([]byte("a"), 2<<20)
	req, _ = http.NewRequest("POST", a.base+"/api/v1/config/validate", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, 400, resp2.StatusCode, "a body over 1 MiB is refused")

	resp3, _ := a.call("POST", "/api/v1/stats", nil)
	assert.Equal(t, 405, resp3.StatusCode, "the wrong method says so")
}
