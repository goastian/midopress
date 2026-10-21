package mitm

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateCA(t *testing.T) {
	ca, err := GenerateCA("MidoPress Test CA", 365*24*time.Hour)
	require.NoError(t, err)

	c := ca.Cert
	assert.Equal(t, "MidoPress Test CA", c.Subject.CommonName)
	assert.True(t, c.IsCA)
	assert.True(t, c.BasicConstraintsValid)
	assert.True(t, c.MaxPathLenZero)
	assert.Equal(t, 0, c.MaxPathLen)
	assert.Equal(t, x509.KeyUsageCertSign|x509.KeyUsageCRLSign|x509.KeyUsageDigitalSignature, c.KeyUsage)
	assert.NotEmpty(t, c.SubjectKeyId)
	assert.IsType(t, &ecdsa.PublicKey{}, c.PublicKey)
	assert.Equal(t, "P-256", c.PublicKey.(*ecdsa.PublicKey).Curve.Params().Name)
	assert.WithinDuration(t, time.Now().Add(-time.Hour), c.NotBefore, time.Minute)
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), c.NotAfter, time.Minute)
	assert.LessOrEqual(t, c.SerialNumber.BitLen(), 128)
	assert.Equal(t, ca.CertDER(), c.Raw)
	assert.NoError(t, c.CheckSignatureFrom(c))
	assert.Contains(t, string(ca.CertPEM()), "BEGIN CERTIFICATE")

	other, err := GenerateCA("x", time.Hour)
	require.NoError(t, err)
	assert.NotEqual(t, 0, c.SerialNumber.Cmp(other.Cert.SerialNumber))
}

func TestWriteFilesAndLoadRoundTrip(t *testing.T) {
	ca, err := GenerateCA("rt", time.Hour)
	require.NoError(t, err)

	dir := filepath.Join(t.TempDir(), "a", "b")
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")
	require.NoError(t, ca.WriteFiles(certPath, keyPath))

	ci, err := os.Stat(certPath)
	require.NoError(t, err)
	ki, err := os.Stat(keyPath)
	require.NoError(t, err)
	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o644), ci.Mode().Perm())
	assert.Equal(t, fs.FileMode(0o600), ki.Mode().Perm())
	assert.Equal(t, fs.FileMode(0o700), di.Mode().Perm())

	loaded, err := LoadCA(certPath, keyPath)
	require.NoError(t, err)
	assert.Equal(t, ca.CertDER(), loaded.CertDER())
	assert.True(t, ca.Key.(*ecdsa.PrivateKey).Equal(loaded.Key))

	err = ca.WriteFiles(certPath, keyPath)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrExist))

	err = ca.WriteFiles(filepath.Join(dir, "new.pem"), keyPath)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrExist))
	_, statErr := os.Stat(filepath.Join(dir, "new.pem"))
	assert.True(t, errors.Is(statErr, fs.ErrNotExist))
}

func opensslCA(t *testing.T, args ...string) (certPath, keyPath string) {
	t.Helper()
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not available")
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	full := append([]string{"req", "-x509", "-nodes", "-days", "2", "-subj", "/CN=openssl test",
		"-keyout", keyPath, "-out", certPath}, args...)
	out, err := exec.Command(bin, full...).CombinedOutput()
	require.NoError(t, err, string(out))
	return certPath, keyPath
}

func TestLoadCAOpenSSLRSA(t *testing.T) {
	certPath, keyPath := opensslCA(t, "-newkey", "rsa:2048", "-addext", "basicConstraints=critical,CA:TRUE")
	ca, err := LoadCA(certPath, keyPath)
	require.NoError(t, err)
	assert.IsType(t, &rsa.PrivateKey{}, ca.Key)

	iss := NewIssuer(ca, IssuerOptions{})
	cert, err := iss.Certificate("rsa.example.com")
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	_, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "rsa.example.com"})
	assert.NoError(t, err)
}

func TestLoadCAOpenSSLRSAPKCS1(t *testing.T) {
	certPath, keyPath := opensslCA(t, "-newkey", "rsa:2048", "-addext", "basicConstraints=critical,CA:TRUE")
	if out, err := exec.Command("openssl", "rsa", "-traditional", "-in", keyPath, "-out", keyPath+".1").CombinedOutput(); err != nil {
		t.Skipf("cannot convert key to PKCS#1: %s", out)
	}
	data, err := os.ReadFile(keyPath + ".1")
	require.NoError(t, err)
	assert.Contains(t, string(data), "RSA PRIVATE KEY")
	_, err = LoadCA(certPath, keyPath+".1")
	assert.NoError(t, err)
}

func TestLoadCAOpenSSLECSEC1(t *testing.T) {
	certPath, keyPath := opensslCA(t, "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
		"-addext", "basicConstraints=critical,CA:TRUE")
	if out, err := exec.Command("openssl", "ec", "-in", keyPath, "-out", keyPath+".ec").CombinedOutput(); err != nil {
		t.Skipf("cannot convert key to SEC1: %s", out)
	}
	data, err := os.ReadFile(keyPath + ".ec")
	require.NoError(t, err)
	assert.Contains(t, string(data), "EC PRIVATE KEY")
	ca, err := LoadCA(certPath, keyPath+".ec")
	require.NoError(t, err)
	assert.IsType(t, &ecdsa.PrivateKey{}, ca.Key)
}

func TestLoadCARejectsNonCA(t *testing.T) {
	certPath, keyPath := opensslCA(t, "-newkey", "rsa:2048", "-addext", "basicConstraints=critical,CA:FALSE")
	_, err := LoadCA(certPath, keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a CA")
}

func TestLoadCARejectsMissingBasicConstraints(t *testing.T) {
	certPath, keyPath := opensslCA(t, "-newkey", "rsa:2048", "-config", writeNoBCConfig(t))
	_, err := LoadCA(certPath, keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BasicConstraints")
}

func writeNoBCConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openssl.cnf")
	cfg := "[req]\ndistinguished_name=dn\nprompt=no\nx509_extensions=ext\n[dn]\nCN=nobc\n[ext]\nsubjectKeyIdentifier=hash\n"
	require.NoError(t, os.WriteFile(p, []byte(cfg), 0o600))
	return p
}

func TestLoadCAMismatchedKey(t *testing.T) {
	a, err := GenerateCA("a", time.Hour)
	require.NoError(t, err)
	b, err := GenerateCA("b", time.Hour)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, a.WriteFiles(filepath.Join(dir, "a.pem"), filepath.Join(dir, "a.key")))
	require.NoError(t, b.WriteFiles(filepath.Join(dir, "b.pem"), filepath.Join(dir, "b.key")))

	_, err = LoadCA(filepath.Join(dir, "a.pem"), filepath.Join(dir, "b.key"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestLoadCAErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadCA(filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing.key"))
	assert.True(t, errors.Is(err, fs.ErrNotExist))

	junk := filepath.Join(dir, "junk")
	require.NoError(t, os.WriteFile(junk, []byte("not pem"), 0o600))
	_, err = LoadCA(junk, junk)
	assert.Error(t, err)
}
