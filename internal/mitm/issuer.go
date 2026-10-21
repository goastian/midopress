package mitm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/goastian/midopress/internal/lru"
)

const (
	defaultLeafTTL   = 48 * time.Hour
	defaultCacheSize = 10000
	leafBackdate     = 24 * time.Hour
	maxNameLen       = 253
	maxCommonName    = 64
)

// IssuerOptions configures an Issuer. Zero values select the defaults.
type IssuerOptions struct {
	LeafTTL   time.Duration
	CacheSize int
	Now       func() time.Time
}

type cachedCert struct {
	cert      *tls.Certificate
	refreshAt time.Time
}

// Issuer mints and caches leaf certificates signed by a CA. It is safe for
// concurrent use.
type Issuer struct {
	ca     *CA
	ttl    time.Duration
	now    func() time.Time
	cache  *lru.Cache[string, *cachedCert]
	flight singleflight.Group
	hits   atomic.Uint64
	misses atomic.Uint64
	issued atomic.Uint64
}

// NewIssuer returns an Issuer signing with ca.
func NewIssuer(ca *CA, opts IssuerOptions) *Issuer {
	if opts.LeafTTL <= 0 {
		opts.LeafTTL = defaultLeafTTL
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = defaultCacheSize
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Issuer{
		ca:    ca,
		ttl:   opts.LeafTTL,
		now:   opts.Now,
		cache: lru.New[string, *cachedCert](opts.CacheSize, opts.LeafTTL/2),
	}
}

// Stats returns cache hits, cache misses and the number of certificates
// actually generated.
func (i *Issuer) Stats() (hits, misses, issued uint64) {
	return i.hits.Load(), i.misses.Load(), i.issued.Load()
}

// Certificate returns a leaf certificate for serverName (a DNS name or an IP
// literal), generating it when it is not cached or has less than half of its
// lifetime left.
func (i *Issuer) Certificate(serverName string) (*tls.Certificate, error) {
	name, err := normalizeName(serverName)
	if err != nil {
		return nil, err
	}
	if c := i.lookup(name); c != nil {
		i.hits.Add(1)
		return c, nil
	}
	i.misses.Add(1)

	v, err, _ := i.flight.Do(name, func() (any, error) {
		if c := i.lookup(name); c != nil {
			return c, nil
		}
		c, err := i.issue(name)
		if err != nil {
			return nil, err
		}
		i.cache.Add(name, &cachedCert{cert: c, refreshAt: i.now().Add(i.ttl / 2)})
		return c, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tls.Certificate), nil
}

func (i *Issuer) lookup(name string) *tls.Certificate {
	e, ok := i.cache.Get(name)
	if !ok {
		return nil
	}
	if !i.now().Before(e.refreshAt) {
		i.cache.Remove(name)
		return nil
	}
	return e.cert
}

func (i *Issuer) issue(name string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, fmt.Errorf("generate leaf serial: %w", err)
	}
	cn := name
	if len(cn) > maxCommonName {
		cn = cn[:maxCommonName]
	}
	now := i.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-leafBackdate),
		NotAfter:              now.Add(i.ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		SubjectKeyId:          keyID(&key.PublicKey),
		AuthorityKeyId:        i.ca.Cert.SubjectKeyId,
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip.AsSlice())
	} else {
		tmpl.DNSNames = []string{name}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, i.ca.Cert, &key.PublicKey, i.ca.Key)
	if err != nil {
		return nil, fmt.Errorf("sign leaf for %q: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse leaf for %q: %w", name, err)
	}
	i.issued.Add(1)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// TLSConfig returns a tls.Config shared by all connections that serves
// certificates minted by the issuer. defaultHost is consulted when the client
// sends no SNI.
func (i *Issuer) TLSConfig(defaultHost func(*tls.ClientHelloInfo) string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" && defaultHost != nil {
				name = defaultHost(hello)
			}
			if name == "" {
				return nil, errors.New("no server name")
			}
			return i.Certificate(name)
		},
	}
}

// normalizeName lowercases name and strips a trailing dot, rejecting
// anything that is neither a plausible DNS name nor an IP literal.
func normalizeName(name string) (string, error) {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return "", errors.New("empty server name")
	}
	if len(name) > maxNameLen {
		return "", errors.New("server name too long")
	}
	upper := false
	for j := 0; j < len(name); j++ {
		if c := name[j]; c >= 'A' && c <= 'Z' {
			upper = true
			break
		}
	}
	if upper {
		name = strings.ToLower(name)
	}
	if strings.IndexByte(name, ':') >= 0 {
		if _, err := netip.ParseAddr(name); err != nil || strings.IndexByte(name, '%') >= 0 {
			return "", fmt.Errorf("invalid server name %q", name)
		}
		return name, nil
	}
	labelStart := 0
	for j := 0; j <= len(name); j++ {
		if j == len(name) || name[j] == '.' {
			if j == labelStart || j-labelStart > 63 {
				return "", fmt.Errorf("invalid server name %q", name)
			}
			labelStart = j + 1
			continue
		}
		c := name[j]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return "", fmt.Errorf("invalid character %q in server name", c)
		}
	}
	return name, nil
}
