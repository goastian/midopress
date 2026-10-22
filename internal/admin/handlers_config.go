package admin

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/rules"
)

var profileName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ruleJSON is a rule as the API says it.
type ruleJSON struct {
	Match   string         `json:"match"`
	Action  string         `json:"action,omitempty"`
	Profile string         `json:"profile,omitempty"`
	Set     map[string]any `json:"set,omitempty"`
}

func toRules(in []ruleJSON) []rules.Rule {
	out := make([]rules.Rule, len(in))
	for i, r := range in {
		set := map[string]any(nil)
		if len(r.Set) > 0 {
			set = make(map[string]any, len(r.Set))
			for k, v := range r.Set {
				if f, ok := v.(float64); ok && f == float64(int(f)) {
					v = int(f) // JSON has one kind of number, the settings want integers
				}
				set[k] = v
			}
		}
		out[i] = rules.Rule{Match: r.Match, Action: rules.Action(r.Action), Profile: r.Profile, Set: set}
	}
	return out
}

func fromRules(in []rules.Rule) []ruleJSON {
	out := make([]ruleJSON, len(in))
	for i, r := range in {
		out[i] = ruleJSON{Match: r.Match, Action: string(r.Action), Profile: r.Profile, Set: r.Set}
	}
	return out
}

func (s *Server) profileNames() []string {
	profiles, _ := s.d.Pipeline.Profiles()
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Server) view() (configView, error) {
	cfg := s.d.Config()
	ov, err := s.d.Overrides()
	if err != nil {
		return configView{}, err
	}
	generic := map[string]any{}
	if raw, err := yaml.Marshal(cfg.Redacted()); err == nil {
		_ = yaml.Unmarshal(raw, &generic)
	}
	profiles, def := s.d.Pipeline.Profiles()
	builtin := img.Profiles()
	infos := make([]profileInfo, 0, len(profiles))
	for _, name := range s.profileNames() {
		p := profiles[name]
		_, isBuiltin := builtin[name]
		override, modified := cfg.Profiles[name]
		formats := make([]string, len(p.Formats))
		for i, f := range p.Formats {
			formats[i] = f.String()
		}
		infos = append(infos, profileInfo{
			Name: name, Builtin: isBuiltin, Modified: isBuiltin && modified, Lossless: p.Lossless,
			TargetDSSIM: p.TargetDSSIM, TargetSSIM: p.TargetSSIM, TargetSSIMULACRA2: p.TargetSSIMULACRA2,
			Attempts: p.Attempts, Formats: formats, ResizeToViewport: p.ResizeToViewport, PhotoColors: p.PhotoColors,
			QuantizePNG: p.QuantizePNG, AnimatedToStill: p.AnimatedToStill, AnimatedQuality: p.AnimatedQuality,
			Text:     textLevels{Brotli: p.Text.Brotli, Gzip: p.Text.Gzip, Zstd: p.Text.Zstd, BrotliStatic: p.Text.BrotliStatic},
			Override: override,
		})
	}
	return configView{
		Config: generic, DefaultProfile: def, Profiles: infos, Rules: fromRules(cfg.Rules),
		StateFile: cfg.Web.UIPath(),
		ManagedByUI: managedBy{
			DefaultProfile: ov != nil && ov.DefaultProfile != nil,
			Profiles:       ov != nil && ov.Profiles != nil,
			Rules:          ov != nil && ov.Rules != nil,
		},
	}, nil
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	v, err := s.view()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// issuesOf turns what the configuration code reports into positions the
// interface can point at.
func issuesOf(err error, fallbackField string) []Issue {
	var out []Issue
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, x := range j.Unwrap() {
				walk(x)
			}
			return
		}
		var re *rules.RuleError
		if errors.As(e, &re) {
			iss := Issue{Index: re.Index, Field: re.Field, Message: re.Err.Error()}
			var pe *rules.ParseError
			if errors.As(re.Err, &pe) {
				iss.Column, iss.Message = pe.Column, pe.Msg
			}
			out = append(out, iss)
			return
		}
		for line := range strings.SplitSeq(e.Error(), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, Issue{Index: -1, Field: fallbackField, Message: line})
			}
		}
	}
	walk(err)
	return out
}

func cloneOverrides(o *config.Overrides) *config.Overrides {
	c := &config.Overrides{}
	if o == nil {
		return c
	}
	if o.DefaultProfile != nil {
		v := *o.DefaultProfile
		c.DefaultProfile = &v
	}
	if o.Profiles != nil {
		m := maps.Clone(*o.Profiles)
		c.Profiles = &m
	}
	if o.Rules != nil {
		r := slices.Clone(*o.Rules)
		c.Rules = &r
	}
	return c
}

// change applies fn to a copy of what is saved, saves it and answers with the
// new state. fn can refuse with the status it wants.
func (s *Server) change(w http.ResponseWriter, fn func(o *config.Overrides) (status int, code string, issues []Issue, err error)) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	cur, err := s.d.Overrides()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	o := cloneOverrides(cur)
	if status, code, issues, err := fn(o); err != nil {
		if status == http.StatusUnprocessableEntity {
			writeInvalid(w, err.Error(), issues)
		} else {
			writeError(w, status, code, err.Error())
		}
		return
	}
	if err := s.d.Save(o); err != nil {
		writeInvalid(w, err.Error(), issuesOf(err, ""))
		return
	}
	v, err := s.view()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) checkRules(list []rules.Rule, names []string) []Issue {
	var issues []Issue
	if _, err := rules.Compile(list, rules.WithProfiles(names...)); err != nil {
		issues = append(issues, issuesOf(err, "")...)
	}
	for i, r := range list {
		if err := config.ValidateSet(r.Set); err != nil {
			issues = append(issues, Issue{Index: i, Field: "set", Message: err.Error()})
		}
	}
	return issues
}

func (s *Server) handlePutRules(w http.ResponseWriter, r *http.Request) {
	var in []ruleJSON
	if !readJSON(w, r, &in) {
		return
	}
	list := toRules(in)
	if issues := s.checkRules(list, s.profileNames()); len(issues) > 0 {
		writeInvalid(w, fmt.Sprintf("%d problem(s) in the rules", len(issues)), issues)
		return
	}
	s.change(w, func(o *config.Overrides) (int, string, []Issue, error) {
		o.Rules = &list
		return 0, "", nil, nil
	})
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rules []ruleJSON `json:"rules"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	issues := s.checkRules(toRules(body.Rules), s.profileNames())
	if issues == nil {
		issues = []Issue{}
	}
	writeJSON(w, http.StatusOK, validateResult{OK: len(issues) == 0, Issues: issues})
}

func (s *Server) handlePutProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body img.ProfileOverride
	if !readJSON(w, r, &body) {
		return
	}
	if !profileName.MatchString(name) {
		writeInvalid(w, "the name of a profile is lower case letters, digits, - and _, starting with a letter", []Issue{{Index: -1, Field: "name", Message: "lower case letters, digits, - and _"}})
		return
	}
	s.change(w, func(o *config.Overrides) (int, string, []Issue, error) {
		merged := maps.Clone(s.d.Config().Profiles)
		if merged == nil {
			merged = map[string]img.ProfileOverride{}
		}
		merged[name] = body
		if _, err := img.BuildProfiles(merged); err != nil {
			return http.StatusUnprocessableEntity, "invalid", issuesOf(err, name), err
		}
		o.Profiles = &merged
		return 0, "", nil, nil
	})
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.d.Config()
	_, isBuiltin := img.Profiles()[name]
	if _, exists := s.liveProfiles()[name]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "there is no profile called "+name)
		return
	}
	if !isBuiltin {
		if _, def := s.d.Pipeline.Profiles(); def == name {
			writeError(w, http.StatusConflict, "conflict", "it is the default profile: choose another default first")
			return
		}
		for i, rl := range cfg.Rules {
			if rl.Profile == name {
				writeError(w, http.StatusConflict, "conflict", fmt.Sprintf("rule %d uses it", i+1))
				return
			}
		}
	}
	s.change(w, func(o *config.Overrides) (int, string, []Issue, error) {
		merged := maps.Clone(cfg.Profiles)
		if merged == nil {
			merged = map[string]img.ProfileOverride{}
		}
		delete(merged, name)
		o.Profiles = &merged
		return 0, "", nil, nil
	})
}

func (s *Server) liveProfiles() map[string]*img.Profile {
	p, _ := s.d.Pipeline.Profiles()
	return p
}

func (s *Server) handlePutDefaultProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile string `json:"profile"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, ok := s.liveProfiles()[body.Profile]; !ok {
		writeInvalid(w, "there is no profile called "+body.Profile, []Issue{{Index: -1, Field: "profile", Message: "unknown profile"}})
		return
	}
	s.change(w, func(o *config.Overrides) (int, string, []Issue, error) {
		o.DefaultProfile = &body.Profile
		return 0, "", nil, nil
	})
}

func (s *Server) handleForgetUI(w http.ResponseWriter, _ *http.Request) {
	s.change(w, func(o *config.Overrides) (int, string, []Issue, error) {
		*o = config.Overrides{}
		return 0, "", nil, nil
	})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var body testRequest
	if !readJSON(w, r, &body) {
		return
	}
	u, err := url.Parse(strings.TrimSpace(body.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		writeInvalid(w, "give a full address, such as https://example.com/photo.jpg", []Issue{{Index: -1, Field: "url", Message: "a full http or https address"}})
		return
	}
	method := strings.ToUpper(body.Method)
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, u.String(), nil)
	req.URL = u
	for k, v := range body.Headers {
		req.Header.Set(k, v)
	}
	if body.Src != "" {
		a, err := netip.ParseAddr(body.Src)
		if err != nil {
			writeInvalid(w, "src is not an address", []Issue{{Index: -1, Field: "src", Message: "an IP address"}})
			return
		}
		req.RemoteAddr = netip.AddrPortFrom(a, 40000).String()
	}
	req = req.WithContext(pipeline.WithClient(req.Context(), pipeline.Client{Profile: body.DeviceProfile}))

	var resp *http.Response
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(body.ContentType, ";", 2)[0]))
	if mt != "" {
		status := body.Status
		if status == 0 {
			status = http.StatusOK
		}
		resp = &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {body.ContentType}}}
	}
	d := s.d.Pipeline.Decide(req, resp, mt)

	res := testResult{Explanations: []ruleExplanation{}, Action: string(d.Action), Profile: d.Profile.Name, ProfileSource: d.ProfileSource, Set: d.Set}
	if res.Set == nil {
		res.Set = map[string]any{}
	}
	for _, e := range d.Explanations {
		res.Explanations = append(res.Explanations, ruleExplanation{Index: e.Index, Match: e.Match, Matched: e.Matched, Reason: e.Reason, Action: string(e.Action), Profile: e.Profile})
	}
	switch {
	case d.Action != rules.ActionNone:
		res.Outcome, res.Note = "passthrough", "A rule leaves this alone: the response is sent as it comes."
	case mt == "":
		res.Outcome, res.Note = "process", "Only the conditions on the request were tried. Give a content type to try the rules that look at the response."
	default:
		res.Outcome, res.Note = "process", "The pipeline handles it with the profile above, unless the content turns out not to be worth it (the original is sent when the result is not smaller)."
	}
	writeJSON(w, http.StatusOK, res)
}
