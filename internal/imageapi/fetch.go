package imageapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/goastian/midopress/internal/upstream"
)

var (
	errDestination = errors.New("destination refused")
	errSession     = errors.New("resource requires original client session")
	errSize        = errors.New("image exceeds byte limit")
	errOrigin      = errors.New("origin did not return a public image")
)

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var ipv6Global = netip.MustParsePrefix("2000::/3")

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !ipv6Global.Contains(ip) {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func validateURL(u *url.URL) error {
	if u == nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || strings.Contains(u.Host, "%") {
		return errDestination
	}
	if port := u.Port(); port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		return errDestination
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || !strings.Contains(host, ".") && !strings.Contains(host, ":") ||
		strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errDestination
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip) {
		return errDestination
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errDestination
	}
	for k := range q {
		k = strings.ToLower(k)
		if strings.Contains(k, "token") || strings.Contains(k, "session") || strings.Contains(k, "signature") ||
			strings.HasPrefix(k, "x-amz-") || strings.HasPrefix(k, "x-goog-") || k == "auth" || k == "key" || k == "sig" {
			return errSession
		}
	}
	return nil
}

func privateResponse(h http.Header) bool {
	if len(h.Values("Set-Cookie")) > 0 || h.Get("WWW-Authenticate") != "" {
		return true
	}
	for _, line := range h.Values("Cache-Control") {
		for _, part := range strings.Split(line, ",") {
			name, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(part)), "=")
			switch name {
			case "private", "no-store", "no-cache", "no-transform":
				return true
			}
		}
	}
	for _, line := range h.Values("Vary") {
		for _, part := range strings.Split(line, ",") {
			switch strings.ToLower(strings.TrimSpace(part)) {
			case "*", "cookie", "authorization":
				return true
			}
		}
	}
	return false
}

func (s *Service) fetch(ctx context.Context, resource, accept string) ([]byte, error) {
	u, err := url.Parse(resource)
	if err != nil {
		return nil, errDestination
	}
	for redirects := 0; redirects <= 5; redirects++ {
		if err := validateURL(u); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errDestination
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("User-Agent", "MidoPress-Image/1")
		resp, err := s.transport.RoundTrip(req)
		if err != nil {
			if errors.Is(err, upstream.ErrBlockedAddress) {
				return nil, errDestination
			}
			return nil, errOrigin
		}
		if privateResponse(resp.Header) || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_ = resp.Body.Close()
			return nil, errSession
		}
		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			next, err := resp.Location()
			_ = resp.Body.Close()
			if err != nil || redirects == 5 {
				return nil, errDestination
			}
			u = next
			continue
		}
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" ||
			(resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
			_ = resp.Body.Close()
			return nil, errOrigin
		}
		if resp.ContentLength > s.o.MaxBytes {
			_ = resp.Body.Close()
			return nil, errSize
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, s.o.MaxBytes+1))
		_ = resp.Body.Close()
		if int64(len(data)) > s.o.MaxBytes {
			return nil, errSize
		}
		if err != nil {
			return nil, errOrigin
		}
		return data, nil
	}
	return nil, errDestination
}
