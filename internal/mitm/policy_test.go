package mitm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPatternsCompile(t *testing.T) {
	_, err := NewPolicy(PolicyOptions{UseDefaults: true})
	require.NoError(t, err)
}

func TestDefaultPatterns(t *testing.T) {
	p, err := NewPolicy(PolicyOptions{UseDefaults: true})
	require.NoError(t, err)

	for _, h := range []string{
		"www.bankofamerica.com", "my-bank.example", "bank.example", "online.banco.example", "www.sparkasse-koeln.de",
		"agency.gov", "irs.gov", "service.gov.uk", "army.mil", "example.gov:443",
		"my.health.example", "clinic.example.org", "hospital-central.example", "salud.example",
		"paypal.com", "www.paypal.com", "vault.1password.com", "vault.bitwarden.com", "lastpass.com.lastpass.com",
		"accounts.mozilla.org", "a.b.mozilla.com", "x.mozilla.net", "sync.firefox.com", "ACCOUNTS.Mozilla.ORG:443",
	} {
		assert.True(t, p.Passthrough(h), h)
	}
	for _, h := range []string{
		"example.com", "www.example.com", "news.ycombinator.com", "mozilla.org", "evilpaypal.com",
		"github.com", "gov.example.com", "wealthy.example", "192.0.2.1", "mybank.example",
	} {
		assert.False(t, p.Passthrough(h), h)
	}
}

func TestStaticPatterns(t *testing.T) {
	p, err := NewPolicy(PolicyOptions{Patterns: []string{"private.test", "*.corp.test"}})
	require.NoError(t, err)
	assert.True(t, p.Passthrough("private.test:8443"))
	assert.True(t, p.Passthrough("a.corp.test"))
	assert.False(t, p.Passthrough("corp.test"))
	assert.False(t, p.Passthrough("www.paypal.com"))
}

func TestInvalidPolicyPattern(t *testing.T) {
	_, err := NewPolicy(PolicyOptions{Patterns: []string{"~d ("}})
	assert.Error(t, err)
}

func TestAutoMarkAndExpiry(t *testing.T) {
	p, err := NewPolicy(PolicyOptions{AutoTTL: 50 * time.Millisecond})
	require.NoError(t, err)
	assert.False(t, p.Passthrough("pinned.example.com"))
	p.MarkFailed("Pinned.Example.com:443")
	assert.True(t, p.Passthrough("pinned.example.com"))
	assert.True(t, p.Passthrough("PINNED.example.com.:8443"))
	assert.False(t, p.Passthrough("other.example.com"))
	assert.Equal(t, 1, p.AutoCount())

	assert.Eventually(t, func() bool { return !p.Passthrough("pinned.example.com") }, 2*time.Second, 10*time.Millisecond)
	assert.Zero(t, p.AutoCount())
}

func TestAutoMaxEntries(t *testing.T) {
	p, err := NewPolicy(PolicyOptions{AutoMaxEntries: 32})
	require.NoError(t, err)
	for i := range 1000 {
		p.MarkFailed(fmt.Sprintf("h%d.example.com", i))
	}
	assert.LessOrEqual(t, p.AutoCount(), 32)
}

func TestPassthroughDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes sync.Pool allocate")
	}
	p, err := NewPolicy(PolicyOptions{UseDefaults: true})
	require.NoError(t, err)
	allocs := testing.AllocsPerRun(100, func() {
		p.Passthrough("www.example.com")
		p.Passthrough("www.example.com:443")
	})
	assert.Zero(t, allocs)
}

func handshakeServerError(t *testing.T, serverCA *CA, clientCfg *tls.Config) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	iss := NewIssuer(serverCA, IssuerOptions{})
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		done <- tls.Server(c, iss.TLSConfig(nil)).Handshake()
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	require.Error(t, tls.Client(c, clientCfg).Handshake())
	return <-done
}

func TestIsUntrustedCertAlertRealHandshake(t *testing.T) {
	for _, v := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		ca := newTestCA(t)
		serverErr := handshakeServerError(t, ca, &tls.Config{
			ServerName: "example.com",
			RootCAs:    x509.NewCertPool(),
			MinVersion: v,
			MaxVersion: v,
		})
		require.Error(t, serverErr)
		assert.True(t, IsUntrustedCertAlert(serverErr), "version %x: %v", v, serverErr)
		assert.True(t, IsUntrustedCertAlert(fmt.Errorf("wrapped: %w", serverErr)))
	}
}

func TestIsUntrustedCertAlertAlertKinds(t *testing.T) {
	for _, text := range []string{
		"tls: bad certificate", "tls: unsupported certificate", "tls: revoked certificate",
		"tls: expired certificate", "tls: unknown certificate", "tls: unknown certificate authority",
	} {
		err := &net.OpError{Op: "remote error", Err: errors.New(text)}
		assert.True(t, IsUntrustedCertAlert(err), text)
	}
	for _, text := range []string{"tls: handshake failure", "tls: internal error", "tls: certificate required", "tls: protocol version not supported"} {
		err := &net.OpError{Op: "remote error", Err: errors.New(text)}
		assert.False(t, IsUntrustedCertAlert(err), text)
	}
	assert.False(t, IsUntrustedCertAlert(&net.OpError{Op: "read", Err: errors.New("tls: bad certificate")}))
}

func TestIsUntrustedCertAlertNegatives(t *testing.T) {
	assert.False(t, IsUntrustedCertAlert(nil))
	assert.False(t, IsUntrustedCertAlert(io.EOF))
	assert.False(t, IsUntrustedCertAlert(io.ErrUnexpectedEOF))
	assert.False(t, IsUntrustedCertAlert(context.DeadlineExceeded))
	assert.False(t, IsUntrustedCertAlert(os.ErrDeadlineExceeded))
	assert.False(t, IsUntrustedCertAlert(errors.New("tls: bad certificate")))
	assert.False(t, IsUntrustedCertAlert(&net.OpError{Op: "read", Err: io.EOF}))
	assert.False(t, IsUntrustedCertAlert(&net.OpError{Op: "remote error", Err: nil}))

	cli, srv := net.Pipe()
	defer func() { _ = srv.Close() }()
	require.NoError(t, cli.SetDeadline(time.Now().Add(50*time.Millisecond)))
	timeoutErr := tls.Client(cli, &tls.Config{ServerName: "x"}).Handshake()
	require.Error(t, timeoutErr)
	assert.False(t, IsUntrustedCertAlert(timeoutErr))

	cli2, srv2 := net.Pipe()
	_ = srv2.Close()
	eofErr := tls.Client(cli2, &tls.Config{ServerName: "x"}).Handshake()
	require.Error(t, eofErr)
	assert.False(t, IsUntrustedCertAlert(eofErr))
}
