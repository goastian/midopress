// Package mitm provides the certificate authority, leaf certificate issuance
// and interception policy used by the TLS man-in-the-middle proxy.
package mitm

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 5280 key identifier, not a security use
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CA is a certificate authority able to sign leaf certificates.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer

	certDER []byte
	certPEM []byte
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func keyID(pub *ecdsa.PublicKey) []byte {
	raw, err := pub.Bytes()
	if err != nil {
		return nil
	}
	sum := sha1.Sum(raw) //nolint:gosec // see import
	return sum[:]
}

// GenerateCA creates a self-signed ECDSA P-256 CA valid for validity.
func GenerateCA(commonName string, validity time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, fmt.Errorf("generate CA serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          keyID(&key.PublicKey),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}
	return newCA(der, key)
}

func newCA(der []byte, key crypto.Signer) (*CA, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	return &CA{
		Cert:    cert,
		Key:     key,
		certDER: der,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

// LoadCA reads a PEM certificate and PEM private key (PKCS#8, PKCS#1 or SEC1).
func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}

	var der []byte
	for rest := certPEM; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			der = block.Bytes
			break
		}
	}
	if der == nil {
		return nil, fmt.Errorf("no PEM certificate found in %s", certPath)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !cert.BasicConstraintsValid {
		return nil, errors.New("certificate has no BasicConstraints extension, it is not a CA")
	}
	if !cert.IsCA {
		return nil, errors.New("certificate is not a CA (BasicConstraints CA:FALSE)")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("CA certificate key usage does not allow certificate signing")
	}

	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse CA key from %s: %w", keyPath, err)
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(cert.PublicKey) {
		return nil, errors.New("CA private key does not match the certificate")
	}
	return newCA(der, key)
}

func parseKey(data []byte) (crypto.Signer, error) {
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, errors.New("no PEM private key found")
		}
		switch block.Type {
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			s, ok := k.(crypto.Signer)
			if !ok {
				return nil, errors.New("unsupported private key type")
			}
			return s, nil
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(block.Bytes)
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(block.Bytes)
		}
	}
}

// WriteFiles stores the certificate (0644) and the key as PKCS#8 PEM (0600),
// creating parent directories with mode 0700. It never overwrites files.
func (ca *CA) WriteFiles(certPath, keyPath string) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(ca.Key)
	if err != nil {
		return fmt.Errorf("marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	for _, p := range []string{certPath, keyPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
	}
	if err := writeExclusive(certPath, ca.certPEM, 0o644); err != nil {
		return err
	}
	if err := writeExclusive(keyPath, keyPEM, 0o600); err != nil {
		_ = os.Remove(certPath)
		return err
	}
	return nil
}

func writeExclusive(path string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// CertPEM returns the PEM encoded CA certificate.
func (ca *CA) CertPEM() []byte { return ca.certPEM }

// CertDER returns the DER encoded CA certificate.
func (ca *CA) CertDER() []byte { return ca.certDER }
