package config

import (
	"errors"
	"math"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func (c *Config) validateImageAPI() error {
	a := c.ImageAPI
	if !a.Enabled {
		return nil
	}
	host, _, err := net.SplitHostPort(a.Addr)
	if err != nil {
		return errors.New("image_api.addr must be a host:port")
	}
	ip, _ := netip.ParseAddr(host)
	loopback := ip.IsLoopback()
	if (a.TLS.Cert == "") != (a.TLS.Key == "") || (!loopback && a.TLS.Cert == "") {
		return errors.New("image_api.tls requires a certificate and key; HTTP is only allowed on a loopback listener behind TLS or for local tests")
	}
	u, err := url.Parse(a.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" ||
		strings.ContainsAny(a.PublicURL, "\r\n") || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("image_api.public_url must be an HTTP(S) origin without a path, credentials, query or fragment")
	}
	pubIP, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme == "http" && (!loopback || !pubIP.IsLoopback() && u.Hostname() != "localhost") {
		return errors.New("image_api.public_url requires HTTPS except for localhost tests")
	}
	if a.MaxBytes < 1024 || a.MaxBytes > 64<<20 || a.MaxPixels < 1 || a.MaxPixels > 16_000_000 ||
		a.MaxWidth < 64 || a.MaxWidth > 16384 || math.IsNaN(a.MaxDPR) || math.IsInf(a.MaxDPR, 0) || a.MaxDPR < 1 || a.MaxDPR > 4 ||
		a.Concurrency < 1 || a.Concurrency > 16 || a.Timeout < time.Second || a.Timeout > 2*time.Minute ||
		a.SignatureTTL < time.Second || a.SignatureTTL > time.Hour || a.CacheTTL < time.Second || a.CacheTTL > 24*time.Hour {
		return errors.New("image_api limits outside supported ranges")
	}
	if a.Addr == c.Web.Addr || a.Addr == c.Listen.Addr {
		return errors.New("image_api.addr must be separate from administration and proxy listeners")
	}
	return nil
}
