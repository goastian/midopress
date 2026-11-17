package imageapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strconv"
	"time"
)

const Version = "img-v1"

var errRequest = errors.New("invalid image request")

type Request struct {
	URL     string  `json:"url"`
	Version string  `json:"version"`
	Profile string  `json:"profile"`
	Format  string  `json:"format"`
	Width   int     `json:"width"`
	DPR     float64 `json:"dpr"`
	Mode    string  `json:"mode"`
}

func (r Request) values() url.Values {
	return url.Values{
		"url": {r.URL}, "v": {r.Version}, "profile": {r.Profile}, "format": {r.Format},
		"w": {strconv.Itoa(r.Width)}, "dpr": {strconv.FormatFloat(r.DPR, 'f', -1, 64)}, "mode": {r.Mode},
	}
}

func (s *Service) normalize(r Request) (Request, error) {
	if r.Version == "" {
		r.Version = "1"
	}
	if r.Profile == "" {
		r.Profile = "balanced"
	}
	if r.Format == "" {
		r.Format = "auto"
	}
	if r.DPR == 0 {
		r.DPR = 1
	}
	if r.Mode == "" {
		r.Mode = "optimized"
	}
	if len(r.URL) > 8192 || len(r.Version) > 128 || r.Version == "" ||
		r.Width < 0 || r.Width > s.o.MaxWidth || math.IsNaN(r.DPR) || math.IsInf(r.DPR, 0) ||
		r.DPR < 1 || r.DPR > s.o.MaxDPR || float64(r.Width)*r.DPR > float64(s.o.MaxWidth) ||
		(r.Mode != "optimized" && r.Mode != "original") {
		return r, errRequest
	}
	if _, ok := s.o.Profiles()[r.Profile]; !ok {
		return r, errRequest
	}
	switch r.Format {
	case "auto", "jpeg", "png", "webp", "avif", "jxl":
	default:
		return r, errRequest
	}
	u, err := url.Parse(r.URL)
	if err != nil || validateURL(u) != nil {
		return r, errRequest
	}
	return r, nil
}

func signature(key []byte, q url.Values) string {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("GET\n/img\n" + q.Encode()))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) Sign(r Request) (string, time.Time, error) {
	r, err := s.normalize(r)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().Add(s.o.SignatureTTL).Truncate(time.Second)
	q := r.values()
	q.Set("exp", strconv.FormatInt(expires.Unix(), 10))
	q.Set("sig", signature(s.o.Key, q))
	return s.o.PublicURL + "/img?" + q.Encode(), expires, nil
}

func (s *Service) authenticate(raw string) (Request, error) {
	q, err := url.ParseQuery(raw)
	if err != nil || len(q) != 9 {
		return Request{}, errRequest
	}
	for k, v := range q {
		if len(v) != 1 {
			return Request{}, errRequest
		}
		switch k {
		case "url", "v", "profile", "format", "w", "dpr", "mode", "exp", "sig":
		default:
			return Request{}, errRequest
		}
	}
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || exp <= s.now().Unix() || exp > s.now().Add(s.o.SignatureTTL).Unix() {
		return Request{}, errRequest
	}
	got, err := hex.DecodeString(q.Get("sig"))
	if err != nil {
		return Request{}, errRequest
	}
	q.Del("sig")
	want, _ := hex.DecodeString(signature(s.o.Key, q))
	if !hmac.Equal(got, want) {
		return Request{}, errRequest
	}
	w, err := strconv.Atoi(q.Get("w"))
	if err != nil {
		return Request{}, errRequest
	}
	dpr, err := strconv.ParseFloat(q.Get("dpr"), 64)
	if err != nil {
		return Request{}, errRequest
	}
	return s.normalize(Request{URL: q.Get("url"), Version: q.Get("v"), Profile: q.Get("profile"),
		Format: q.Get("format"), Width: w, DPR: dpr, Mode: q.Get("mode")})
}
