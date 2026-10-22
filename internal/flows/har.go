package flows

import (
	"net/url"
	"sort"
	"strconv"
	"time"
)

// HAR is a HTTP Archive 1.2 document. The bodies are not in it, only their
// sizes: it says what went through and how big it was, which is what a
// proxy that compresses has to show.
type HAR struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Version string     `json:"version"`
	Creator harCreator `json:"creator"`
	Entries []harEntry `json:"entries"`
}

type harCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type harEntry struct {
	Started  string      `json:"startedDateTime"`
	Time     float64     `json:"time"`
	Request  harRequest  `json:"request"`
	Response harResponse `json:"response"`
	Cache    struct{}    `json:"cache"`
	Timings  harTimings  `json:"timings"`
	Comment  string      `json:"comment,omitempty"`
	// extra fields start with an underscore, as the specification asks
	Decision string  `json:"_decision"`
	Reason   string  `json:"_reason"`
	Codec    string  `json:"_codec,omitempty"`
	Profile  string  `json:"_profile,omitempty"`
	BytesIn  int64   `json:"_originalBytes"`
	Measure  float64 `json:"_dssim,omitempty"`
}

type harRequest struct {
	Method      string      `json:"method"`
	URL         string      `json:"url"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []struct{}  `json:"cookies"`
	Headers     []harHeader `json:"headers"`
	QueryString []harHeader `json:"queryString"`
	HeadersSize int         `json:"headersSize"`
	BodySize    int         `json:"bodySize"`
}

type harResponse struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []struct{}  `json:"cookies"`
	Headers     []harHeader `json:"headers"`
	Content     harContent  `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int         `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
}

type harContent struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harTimings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

func harHeaders(m map[string][]string) []harHeader {
	out := []harHeader{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range m[k] {
			out = append(out, harHeader{Name: k, Value: v})
		}
	}
	return out
}

// ToHAR describes flows, oldest first, as a HAR document.
func ToHAR(flows []Flow, version string) HAR {
	h := HAR{Log: harLog{Version: "1.2", Creator: harCreator{Name: "MidoPress", Version: version}, Entries: []harEntry{}}}
	for _, f := range flows {
		u := url.URL{Scheme: f.Scheme, Host: f.Host}
		if p, err := url.Parse(f.Path); err == nil {
			u.Path, u.RawQuery = p.Path, p.RawQuery
		}
		query := []harHeader{}
		for k, vs := range u.Query() {
			for _, v := range vs {
				query = append(query, harHeader{Name: k, Value: v})
			}
		}
		sort.Slice(query, func(i, j int) bool { return query[i].Name < query[j].Name })
		mime := f.ContentType
		if f.OutContentType != "" {
			mime = f.OutContentType
		}
		h.Log.Entries = append(h.Log.Entries, harEntry{
			Started: f.Time.UTC().Format(time.RFC3339Nano),
			Time:    f.DurationMS,
			Request: harRequest{
				Method: f.Method, URL: u.String(), HTTPVersion: "HTTP/1.1", Cookies: []struct{}{},
				Headers: harHeaders(f.RequestHeaders), QueryString: query, HeadersSize: -1, BodySize: -1,
			},
			Response: harResponse{
				Status: f.Status, StatusText: statusText(f.Status), HTTPVersion: "HTTP/1.1", Cookies: []struct{}{},
				Headers: harHeaders(f.ResponseHeader),
				Content: harContent{Size: f.BytesOut, MimeType: mime}, HeadersSize: -1, BodySize: f.BytesOut,
			},
			Timings:  harTimings{Wait: f.DurationMS},
			Decision: f.Decision, Reason: f.Reason, Codec: f.Codec, Profile: f.Profile,
			BytesIn: f.BytesIn, Measure: f.Measure,
			Comment: "original " + strconv.FormatInt(f.BytesIn, 10) + " bytes, sent " + strconv.FormatInt(f.BytesOut, 10),
		})
	}
	return h
}

func statusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 203:
		return "Non-Authoritative Information"
	case 206:
		return "Partial Content"
	case 301:
		return "Moved Permanently"
	case 302:
		return "Found"
	case 304:
		return "Not Modified"
	case 404:
		return "Not Found"
	}
	return ""
}
