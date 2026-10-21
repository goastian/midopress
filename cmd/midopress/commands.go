package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/server"
)

func cmdConfig(args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 || args[0] != "check" {
		_, _ = fmt.Fprintln(stderr, "usage: midopress config check [-config file] [-print] [flags]")
		return 2
	}
	f := newServeFlags("midopress config check", stderr)
	print := f.fs.Bool("print", false, "print the effective configuration")
	if err := f.fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	cfg, err := f.load(environ)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := server.Check(cfg); err != nil {
		_, _ = fmt.Fprintln(stderr, "configuration is not valid:")
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if *print {
		out, err := cfg.Redacted().YAML()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = stdout.Write(out)
		return 0
	}
	_, _ = fmt.Fprintln(stdout, "configuration ok")
	return 0
}

func defaultCADir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".midopress")
	}
	return ".midopress"
}

func cmdCA(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: midopress ca init|export [flags]")
		return 2
	}
	switch args[0] {
	case "init":
		return caInit(args[1:], stdout, stderr)
	case "export":
		return caExport(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "midopress ca: unknown command %q\n", args[0])
	return 2
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func caInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midopress ca init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", defaultCADir(), "directory for ca.crt and ca.key")
	name := fs.String("name", "MidoPress CA", "common name of the certificate")
	days := fs.Int("days", 3650, "validity in days")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	ca, err := mitm.GenerateCA(*name, time.Duration(*days)*24*time.Hour)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress ca init:", err)
		return 1
	}
	certPath, keyPath := filepath.Join(*dir, "ca.crt"), filepath.Join(*dir, "ca.key")
	if err := ca.WriteFiles(certPath, keyPath); err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress ca init:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, `created a new certificate authority

  certificate: %s
  private key: %s
  SHA-256:     %s

Point the configuration at them:

  mitm:
    ca_cert: %s
    ca_key: %s

Then install the certificate as a trusted authority in the clients. Whoever
has the private key can intercept the traffic of those clients: keep it safe.
`, certPath, keyPath, fingerprint(ca.CertDER()), certPath, keyPath)
	return 0
}

func caExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midopress ca export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	certPath := fs.String("ca", filepath.Join(defaultCADir(), "ca.crt"), "path of the CA certificate")
	format := fs.String("format", "pem", "output format: pem or der")
	out := fs.String("out", "", "output file, stdout when empty")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	data, err := os.ReadFile(*certPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress ca export:", err)
		return 1
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		_, _ = fmt.Fprintln(stderr, "midopress ca export: no certificate found in", *certPath)
		return 1
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		_, _ = fmt.Fprintln(stderr, "midopress ca export:", *certPath, "is not a CA certificate")
		return 1
	}
	var payload []byte
	switch *format {
	case "pem":
		payload = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})
	case "der":
		payload = block.Bytes
	default:
		_, _ = fmt.Fprintf(stderr, "midopress ca export: unknown format %q\n", *format)
		return 2
	}
	if *out == "" {
		_, _ = stdout.Write(payload)
		return 0
	}
	if err := os.WriteFile(*out, payload, 0o644); err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress ca export:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s (SHA-256 %s)\n", *out, fingerprint(block.Bytes))
	return 0
}

func cmdDevice(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: midopress device add|list|revoke -file devices.json [flags]")
		return 2
	}
	fs := flag.NewFlagSet("midopress device "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "path of the devices file")
	id := fs.String("id", "", "device id")
	profile := fs.String("profile", "", "compression profile of the device (add)")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *file == "" {
		_, _ = fmt.Fprintln(stderr, "midopress device: -file is required")
		return 2
	}

	switch args[0] {
	case "add":
		if *id == "" {
			_, _ = fmt.Fprintln(stderr, "midopress device add: -id is required")
			return 2
		}
		st, err := auth.OpenStore(*file, true)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "midopress device add:", err)
			return 1
		}
		token, err := st.Add(*id, *profile)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "midopress device add:", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "device %q created\ntoken (shown only now): %s\n\nUse the id as the proxy user name and the token as the password.\n", *id, token)
	case "list":
		st, err := auth.OpenStore(*file, false)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "midopress device list:", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tPROFILE\tCREATED")
		for _, d := range st.List() {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", d.ID, d.Profile, d.Created.Format(time.RFC3339))
		}
		_ = tw.Flush()
	case "revoke":
		if *id == "" {
			_, _ = fmt.Fprintln(stderr, "midopress device revoke: -id is required")
			return 2
		}
		st, err := auth.OpenStore(*file, false)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "midopress device revoke:", err)
			return 1
		}
		if err := st.Revoke(*id); err != nil {
			_, _ = fmt.Fprintln(stderr, "midopress device revoke:", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "device %q revoked\n", *id)
	default:
		_, _ = fmt.Fprintf(stderr, "midopress device: unknown command %q\n", args[0])
		return 2
	}
	return 0
}
