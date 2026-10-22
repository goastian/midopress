package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/textmin"
)

// live is what can change while the proxy runs: the profiles, the rules and
// what is minified. A request takes one and keeps it, so that a reload in the
// middle of a response changes nothing for it.
type live struct {
	profile  string
	profiles map[string]*img.Profile
	// texts holds two codecs per profile: for responses that change on every
	// request, and for those that look cacheable and can afford a slow level.
	texts  map[string][2]*codec.Text
	minify textmin.Config
	min    *textmin.Minifier
	rules  *rules.RuleSet
	ttl    TTLConfig

	mins sync.Map // textmin.Config -> *textmin.Minifier, for rules that change it
}

func newLive(cfg *Config) (*live, error) {
	profiles := cfg.Profiles
	if profiles == nil {
		profiles = img.Profiles()
	}
	if _, ok := profiles[cfg.Profile]; !ok {
		return nil, fmt.Errorf("pipeline: unknown profile %q", cfg.Profile)
	}
	lv := &live{
		profile:  cfg.Profile,
		profiles: profiles,
		texts:    make(map[string][2]*codec.Text, len(profiles)),
		minify:   cfg.Text.Minify,
		min:      textmin.New(cfg.Text.Minify),
		rules:    cfg.Rules,
		ttl:      cfg.TTL,
	}
	for name, prof := range profiles {
		l := codec.Levels{Brotli: prof.Text.Brotli, Gzip: prof.Text.Gzip, Zstd: prof.Text.Zstd}
		if cfg.Levels.Brotli > 0 {
			l.Brotli = cfg.Levels.Brotli
		}
		if cfg.Levels.Gzip > 0 {
			l.Gzip = cfg.Levels.Gzip
		}
		if cfg.Levels.Zstd > 0 {
			l.Zstd = cfg.Levels.Zstd
		}
		static := l.WithBrotli(max(l.Brotli, prof.Text.BrotliStatic))
		lv.texts[name] = [2]*codec.Text{codec.NewText(l), codec.NewText(static)}
	}
	return lv, nil
}

func (lv *live) minifier(c textmin.Config) *textmin.Minifier {
	if c == lv.minify {
		return lv.min
	}
	if m, ok := lv.mins.Load(c); ok {
		return m.(*textmin.Minifier)
	}
	m, _ := lv.mins.LoadOrStore(c, textmin.New(c))
	return m.(*textmin.Minifier)
}

// Client says who made a request. The proxy sets it in the context of the
// request once it has authenticated the client.
type Client struct {
	Device string // id of the device, empty without token authentication
	// Profile is the profile assigned to the device, empty for none.
	Profile string
	Addr    netip.Addr
}

type clientKey struct{}

// WithClient returns a context that carries c.
func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// ClientFrom returns the client stored by WithClient.
func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientKey{}).(Client)
	return c
}

// Profile precedence, as in the architecture notes: the profile of the device
// token, then the X-MidoPress-Profile header, then a rule, then Save-Data
// raising the default to at least saver, then the default.
func (lv *live) pick(c Client, h http.Header, rule string) *img.Profile {
	if p, ok := lv.profiles[c.Profile]; ok && c.Profile != "" {
		return p
	}
	if v := strings.ToLower(strings.TrimSpace(h.Get("X-MidoPress-Profile"))); v != "" {
		if p, ok := lv.profiles[v]; ok {
			return p
		}
	}
	if p, ok := lv.profiles[rule]; ok && rule != "" {
		return p
	}
	prof := lv.profiles[lv.profile]
	if strings.EqualFold(strings.TrimSpace(h.Get("Save-Data")), "on") && profileRank(prof.Name) < profileRank(img.ProfileSaver) {
		if p, ok := lv.profiles[img.ProfileSaver]; ok {
			return p
		}
	}
	return prof
}

// profileRank orders the built-in profiles from the most to the least careful;
// other profiles count as the least careful.
func profileRank(name string) int {
	switch name {
	case img.ProfileLossless:
		return 0
	case img.ProfileBalanced:
		return 1
	case img.ProfileSaver:
		return 2
	}
	return 3
}

// flow is what is decided about one response before it is transformed.
type flow struct {
	lv     *live
	client Client
	prof   *img.Profile
	opts   options
}

// options are the settings a rule can change for one response.
type options struct {
	minify  textmin.Config
	images  bool
	text    bool
	quality int // 0 leaves it to the request header and the configuration
}

func (lv *live) newFlow(req *http.Request, dec rules.Decision) (*flow, error) {
	cl := ClientFrom(req.Context())
	f := &flow{lv: lv, client: cl, prof: lv.pick(cl, req.Header, dec.Profile)}
	f.opts = options{minify: lv.minify, images: true, text: true}
	var errs []string
	for k, v := range dec.Set {
		mode := func() (textmin.Mode, bool) {
			switch x := v.(type) {
			case bool:
				if x {
					return textmin.Safe, true
				}
				return textmin.Off, true
			case string:
				switch m := textmin.Mode(x); m {
				case textmin.Off, textmin.Safe, textmin.Aggressive:
					return m, true
				}
			}
			return "", false
		}
		switch k {
		case "minify_html", "minify_css", "minify_js", "minify_svg", "minify_json":
			m, ok := mode()
			if !ok {
				errs = append(errs, k)
				continue
			}
			switch k {
			case "minify_html":
				f.opts.minify.HTML = m
			case "minify_css":
				f.opts.minify.CSS = m
			case "minify_js":
				f.opts.minify.JS = m
			case "minify_svg":
				f.opts.minify.SVG = m
			case "minify_json":
				f.opts.minify.JSON = m
			}
		case "images":
			b, ok := v.(bool)
			if !ok {
				errs = append(errs, k)
				continue
			}
			f.opts.images = b
		case "text":
			b, ok := v.(bool)
			if !ok {
				errs = append(errs, k)
				continue
			}
			f.opts.text = b
		case "quality":
			n, ok := v.(int)
			if !ok || n < 1 || n > 100 {
				errs = append(errs, k)
				continue
			}
			f.opts.quality = n
		default:
			errs = append(errs, k)
		}
	}
	if len(errs) > 0 {
		return f, fmt.Errorf("pipeline: rule set has invalid keys or values: %v", errs)
	}
	return f, nil
}

func rulesContext(req *http.Request, resp *http.Response, mt string) *rules.Context {
	c := &rules.Context{
		Method:      req.Method,
		URL:         req.URL.String(),
		Host:        req.URL.Hostname(),
		ReqHeader:   req.Header,
		HasResponse: resp != nil,
	}
	if ap, err := netip.ParseAddrPort(req.RemoteAddr); err == nil {
		c.Src = ap.Addr()
	}
	if resp != nil {
		c.Status = resp.StatusCode
		c.ContentType = mt
		c.RespHeader = resp.Header
	}
	return c
}
