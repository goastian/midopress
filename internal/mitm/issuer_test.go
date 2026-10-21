package mitm

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCA(t testing.TB) *CA {
	t.Helper()
	ca, err := GenerateCA("Test CA", 24*time.Hour)
	require.NoError(t, err)
	return ca
}

func poolOf(ca *CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

func TestIssueDNSLeaf(t *testing.T) {
	ca := newTestCA(t)
	iss := NewIssuer(ca, IssuerOptions{})
	cert, err := iss.Certificate("WWW.Example.COM.")
	require.NoError(t, err)

	require.Len(t, cert.Certificate, 1)
	leaf := cert.Leaf
	require.NotNil(t, leaf)
	assert.Equal(t, cert.Certificate[0], leaf.Raw)
	assert.Equal(t, "www.example.com", leaf.Subject.CommonName)
	assert.Equal(t, []string{"www.example.com"}, leaf.DNSNames)
	assert.Empty(t, leaf.IPAddresses)
	assert.False(t, leaf.IsCA)
	assert.True(t, leaf.BasicConstraintsValid)
	assert.Equal(t, x509.KeyUsageDigitalSignature, leaf.KeyUsage)
	assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
	assert.NotEmpty(t, leaf.SubjectKeyId)
	assert.Equal(t, ca.Cert.SubjectKeyId, leaf.AuthorityKeyId)
	assert.Empty(t, leaf.OCSPServer)
	assert.Empty(t, leaf.CRLDistributionPoints)
	assert.Empty(t, leaf.PolicyIdentifiers)
	assert.WithinDuration(t, time.Now().Add(-24*time.Hour), leaf.NotBefore, time.Minute)
	assert.WithinDuration(t, time.Now().Add(48*time.Hour), leaf.NotAfter, time.Minute)

	_, err = leaf.Verify(x509.VerifyOptions{Roots: poolOf(ca), DNSName: "www.example.com"})
	assert.NoError(t, err)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: poolOf(ca), DNSName: "other.example.com"})
	assert.Error(t, err)

	assert.False(t, ca.Key.(*ecdsa.PrivateKey).PublicKey.Equal(leaf.PublicKey))
}

func TestIssueIPLeaf(t *testing.T) {
	ca := newTestCA(t)
	iss := NewIssuer(ca, IssuerOptions{})
	for _, ip := range []string{"192.0.2.10", "2001:db8::1"} {
		cert, err := iss.Certificate(ip)
		require.NoError(t, err)
		assert.Empty(t, cert.Leaf.DNSNames)
		require.Len(t, cert.Leaf.IPAddresses, 1)
		assert.True(t, cert.Leaf.IPAddresses[0].Equal(net.ParseIP(ip)))
		_, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: poolOf(ca), DNSName: ip})
		assert.NoError(t, err)
	}
}

func TestLongNameTruncatedCN(t *testing.T) {
	iss := NewIssuer(newTestCA(t), IssuerOptions{})
	name := ""
	for len(name) < 150 {
		name += "abcdefghij."
	}
	name += "com"
	cert, err := iss.Certificate(name)
	require.NoError(t, err)
	assert.Len(t, cert.Leaf.Subject.CommonName, 64)
	assert.Equal(t, []string{name}, cert.Leaf.DNSNames)
}

func TestInvalidNames(t *testing.T) {
	iss := NewIssuer(newTestCA(t), IssuerOptions{})
	for _, n := range []string{"", ".", "a b.com", "a..com", ".a.com", "exa/mple.com", "*.example.com", "a\x00b.com", "ex\tample.com", "fe80::1%eth0", "a:b", "é.com"} {
		_, err := iss.Certificate(n)
		assert.Error(t, err, "name %q", n)
	}
	_, _, issued := iss.Stats()
	assert.Zero(t, issued)
}

func TestSerialUniquenessAndDistinctKeys(t *testing.T) {
	iss := NewIssuer(newTestCA(t), IssuerOptions{CacheSize: 1 << 12})
	serials := map[string]struct{}{}
	keys := map[string]struct{}{}
	for i := range 300 {
		cert, err := iss.Certificate(fmt.Sprintf("h%d.example.com", i))
		require.NoError(t, err)
		serials[cert.Leaf.SerialNumber.String()] = struct{}{}
		keys[string(cert.Leaf.SubjectKeyId)] = struct{}{}
		assert.LessOrEqual(t, cert.Leaf.SerialNumber.BitLen(), 128)
	}
	assert.Len(t, serials, 300)
	assert.Len(t, keys, 300)
}

func TestCacheHitReturnsSameCertificate(t *testing.T) {
	iss := NewIssuer(newTestCA(t), IssuerOptions{})
	a, err := iss.Certificate("example.com")
	require.NoError(t, err)
	b, err := iss.Certificate("EXAMPLE.com.")
	require.NoError(t, err)
	assert.Same(t, a, b)
	hits, misses, issued := iss.Stats()
	assert.Equal(t, uint64(1), hits)
	assert.Equal(t, uint64(1), misses)
	assert.Equal(t, uint64(1), issued)
}

func TestSingleflight(t *testing.T) {
	iss := NewIssuer(newTestCA(t), IssuerOptions{})
	const n = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	certs := make([]*tls.Certificate, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := iss.Certificate("burst.example.com")
			assert.NoError(t, err)
			certs[i] = c
		}()
	}
	close(start)
	wg.Wait()
	_, _, issued := iss.Stats()
	assert.Equal(t, uint64(1), issued)
	for _, c := range certs {
		assert.Same(t, certs[0], c)
	}
}

func TestRefreshAfterHalfLifetime(t *testing.T) {
	var offset atomic.Int64
	base := time.Now()
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	iss := NewIssuer(newTestCA(t), IssuerOptions{LeafTTL: 10 * time.Hour, Now: now})

	first, err := iss.Certificate("example.com")
	require.NoError(t, err)

	offset.Store(int64(4 * time.Hour))
	again, err := iss.Certificate("example.com")
	require.NoError(t, err)
	assert.Same(t, first, again)

	offset.Store(int64(5*time.Hour + time.Second))
	renewed, err := iss.Certificate("example.com")
	require.NoError(t, err)
	assert.NotSame(t, first, renewed)
	assert.NotEqual(t, 0, first.Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber))
	assert.True(t, renewed.Leaf.NotAfter.Sub(now()) >= 9*time.Hour+59*time.Minute)

	_, _, issued := iss.Stats()
	assert.Equal(t, uint64(2), issued)
}

func serveTLS(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.(*tls.Conn).Handshake()
			}()
		}
	}()
	return ln.Addr().String()
}

func TestEndToEndHandshake(t *testing.T) {
	ca := newTestCA(t)
	iss := NewIssuer(ca, IssuerOptions{})
	addr := serveTLS(t, iss.TLSConfig(func(*tls.ClientHelloInfo) string { return "fallback.example.com" }))

	dial := func(cfg *tls.Config) *tls.Conn {
		t.Helper()
		cfg.RootCAs = poolOf(ca)
		cfg.NextProtos = []string{"h2", "http/1.1"}
		c, err := tls.Dial("tcp", addr, cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	c := dial(&tls.Config{ServerName: "sni.example.com"})
	assert.Equal(t, "h2", c.ConnectionState().NegotiatedProtocol)
	assert.Equal(t, []string{"sni.example.com"}, c.ConnectionState().PeerCertificates[0].DNSNames)
	assert.Len(t, c.ConnectionState().PeerCertificates, 1)

	c = dial(&tls.Config{ServerName: "fallback.example.com"})
	assert.Equal(t, []string{"fallback.example.com"}, c.ConnectionState().PeerCertificates[0].DNSNames)

}

func TestEmptySNIUsesDefaultHost(t *testing.T) {
	ca := newTestCA(t)
	iss := NewIssuer(ca, IssuerOptions{})
	addr := serveTLS(t, iss.TLSConfig(func(*tls.ClientHelloInfo) string { return "127.0.0.1" }))

	c, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: poolOf(ca)})
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	require.Len(t, c.ConnectionState().PeerCertificates[0].IPAddresses, 1)
	assert.True(t, c.ConnectionState().PeerCertificates[0].IPAddresses[0].Equal(net.ParseIP("127.0.0.1")))
}

func TestNoServerNameWithoutDefault(t *testing.T) {
	iss := NewIssuer(newTestCA(t), IssuerOptions{})
	cfg := iss.TLSConfig(nil)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	assert.Equal(t, []string{"h2", "http/1.1"}, cfg.NextProtos)
	_, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no server name")
}

func BenchmarkCertificateCached(b *testing.B) {
	iss := NewIssuer(newTestCA(b), IssuerOptions{})
	_, err := iss.Certificate("bench.example.com")
	require.NoError(b, err)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := iss.Certificate("bench.example.com"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkCertificateIssue(b *testing.B) {
	iss := NewIssuer(newTestCA(b), IssuerOptions{CacheSize: 16})
	names := make([]string, 1024)
	for i := range names {
		names[i] = fmt.Sprintf("h%d.example.com", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if _, err := iss.Certificate(names[i%len(names)]); err != nil {
			b.Fatal(err)
		}
		iss.cache.Remove(names[i%len(names)])
	}
}
