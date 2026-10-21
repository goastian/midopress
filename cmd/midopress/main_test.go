package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb, nil)
	return code, out.String(), errb.String()
}

func TestVersionAndHelp(t *testing.T) {
	code, out, _ := exec(t, "version")
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(out, "midopress "))

	code, out, _ = exec(t, "-version")
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(out, "midopress "))

	code, out, _ = exec(t, "help")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "usage")

	code, _, errOut := exec(t, "bogus")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "unknown command")
}

func TestConfigCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}

	t.Run("defaults are fine", func(t *testing.T) {
		code, out, _ := exec(t, "config", "check")
		assert.Equal(t, 0, code)
		assert.Contains(t, out, "configuration ok")
	})

	t.Run("a valid file", func(t *testing.T) {
		p := write("ok.yaml", "listen:\n  addr: \"127.0.0.1:7000\"\npipeline:\n  text: {brotli: 9}\n")
		code, out, _ := exec(t, "config", "check", "-config", p)
		assert.Equal(t, 0, code)
		assert.Contains(t, out, "configuration ok")
	})

	t.Run("problems are all reported at once", func(t *testing.T) {
		p := write("bad.yaml", "log: {level: loud}\npipeline:\n  text: {brotli: 99}\nmitm:\n  ca_cert: /nonexistent/ca.crt\n  ca_key: /nonexistent/ca.key\n")
		code, _, errOut := exec(t, "config", "check", "-config", p)
		assert.Equal(t, 1, code)
		assert.Contains(t, errOut, "log.level")
		assert.Contains(t, errOut, "brotli")
		assert.Contains(t, errOut, "mitm")
	})

	t.Run("a typo in a key is an error", func(t *testing.T) {
		p := write("typo.yaml", "pipeline:\n  text:\n    brotlli: 5\n")
		code, _, errOut := exec(t, "config", "check", "-config", p)
		assert.Equal(t, 1, code)
		assert.Contains(t, errOut, "brotlli")
	})

	t.Run("a missing file", func(t *testing.T) {
		code, _, _ := exec(t, "config", "check", "-config", filepath.Join(dir, "missing.yaml"))
		assert.Equal(t, 1, code)
	})

	t.Run("flags override the file and an open proxy is refused", func(t *testing.T) {
		code, _, errOut := exec(t, "config", "check", "-block-global=false")
		assert.Equal(t, 1, code)
		assert.Contains(t, errOut, "open proxy")

		code, _, _ = exec(t, "config", "check", "-block-global=false", "-user", "u", "-pass", "p")
		assert.Equal(t, 0, code, "with credentials it is fine")
	})

	t.Run("print shows the effective configuration without secrets", func(t *testing.T) {
		code, out, _ := exec(t, "config", "check", "-print", "-user", "u", "-pass", "hunter2", "-brotli", "11")
		assert.Equal(t, 0, code)
		assert.Contains(t, out, "brotli: 11")
		assert.Contains(t, out, "mode: basic")
		assert.NotContains(t, out, "hunter2")
	})

	t.Run("the environment is read", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"config", "check", "-print"}, &out, &errb, []string{"MIDOPRESS_PIPELINE_TEXT_BROTLI=10"})
		assert.Equal(t, 0, code)
		assert.Contains(t, out.String(), "brotli: 10")
	})
}

func TestCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")

	code, out, errOut := exec(t, "ca", "init", "-dir", dir, "-name", "Test CA", "-days", "30")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "SHA-256")
	assert.Regexp(t, regexp.MustCompile(`([0-9A-F]{2}:){31}[0-9A-F]{2}`), out)

	fi, err := os.Stat(filepath.Join(dir, "ca.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "the private key must not be readable by others")

	t.Run("it never overwrites an existing authority", func(t *testing.T) {
		before, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
		code, _, errOut := exec(t, "ca", "init", "-dir", dir)
		assert.Equal(t, 1, code)
		assert.Contains(t, errOut, "exist")
		after, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
		assert.Equal(t, before, after)
	})

	t.Run("export as PEM and DER", func(t *testing.T) {
		code, pemOut, _ := exec(t, "ca", "export", "-ca", filepath.Join(dir, "ca.crt"))
		require.Equal(t, 0, code)
		block, _ := pem.Decode([]byte(pemOut))
		require.NotNil(t, block)
		cert, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		assert.Equal(t, "Test CA", cert.Subject.CommonName)
		assert.True(t, cert.IsCA)
		assert.NotContains(t, pemOut, "PRIVATE")

		derFile := filepath.Join(t.TempDir(), "ca.der")
		code, out, _ := exec(t, "ca", "export", "-ca", filepath.Join(dir, "ca.crt"), "-format", "der", "-out", derFile)
		require.Equal(t, 0, code)
		assert.Contains(t, out, "SHA-256")
		der, err := os.ReadFile(derFile)
		require.NoError(t, err)
		assert.Equal(t, block.Bytes, der)
	})

	t.Run("export refuses what is not a CA", func(t *testing.T) {
		notCA := filepath.Join(t.TempDir(), "x.crt")
		require.NoError(t, os.WriteFile(notCA, []byte("not a certificate"), 0o600))
		code, _, _ := exec(t, "ca", "export", "-ca", notCA)
		assert.Equal(t, 1, code)
		code, _, _ = exec(t, "ca", "export", "-ca", filepath.Join(dir, "missing.crt"))
		assert.Equal(t, 1, code)
	})

	t.Run("bad usage", func(t *testing.T) {
		code, _, _ := exec(t, "ca")
		assert.Equal(t, 2, code)
		code, _, _ = exec(t, "ca", "revoke")
		assert.Equal(t, 2, code)
		code, _, _ = exec(t, "ca", "export", "-format", "p12", "-ca", filepath.Join(dir, "ca.crt"))
		assert.Equal(t, 2, code)
	})
}

func TestDeviceCommands(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sub", "devices.json")

	code, out, errOut := exec(t, "device", "add", "-file", file, "-id", "phone", "-profile", "saver")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "token (shown only now):")

	code, _, errOut = exec(t, "device", "add", "-file", file, "-id", "phone")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "phone")

	code, _, _ = exec(t, "device", "add", "-file", file, "-id", "bad id!")
	assert.Equal(t, 1, code)

	code, out, _ = exec(t, "device", "list", "-file", file)
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "phone")
	assert.Contains(t, out, "saver")

	code, _, _ = exec(t, "device", "revoke", "-file", file, "-id", "phone")
	assert.Equal(t, 0, code)
	code, out, _ = exec(t, "device", "list", "-file", file)
	assert.Equal(t, 0, code)
	assert.NotContains(t, out, "phone")

	code, _, _ = exec(t, "device", "revoke", "-file", file, "-id", "phone")
	assert.Equal(t, 1, code)
	code, _, _ = exec(t, "device", "list", "-file", filepath.Join(t.TempDir(), "none.json"))
	assert.Equal(t, 1, code, "listing does not create a file")
	code, _, _ = exec(t, "device", "add", "-id", "x")
	assert.Equal(t, 2, code)
	code, _, _ = exec(t, "device")
	assert.Equal(t, 2, code)
}

func TestTokensAreNotStoredInTheClear(t *testing.T) {
	file := filepath.Join(t.TempDir(), "devices.json")
	code, out, _ := exec(t, "device", "add", "-file", file, "-id", "phone")
	require.Equal(t, 0, code)
	m := regexp.MustCompile(`token \(shown only now\): (\S+)`).FindStringSubmatch(out)
	require.Len(t, m, 2)
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), m[1])
}

func TestServeRefusesBadConfiguration(t *testing.T) {
	code, _, errOut := exec(t, "-host", "127.0.0.1:0", "-cert", "only-a-cert.pem")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "invalid configuration")

	code, _, errOut = exec(t, "-host", "not a host")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "listen.addr")

	code, _, _ = exec(t, "-no-such-flag")
	assert.Equal(t, 2, code)
}
