package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/server"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = ""

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

const usage = `usage: midopress [flags]
       midopress config check [flags]
       midopress ca init|export [flags]
       midopress device add|list|revoke -file devices.json [flags]
       midopress admin token [-config file]
       midopress bench [flags] <directory with images>
       midopress version

With no subcommand it runs the proxy. Run a subcommand with -h for its flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

func run(args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		switch args[0] {
		case "serve":
			return cmdServe(args[1:], stdout, stderr, environ)
		case "config":
			return cmdConfig(args[1:], stdout, stderr, environ)
		case "ca":
			return cmdCA(args[1:], stdout, stderr)
		case "device":
			return cmdDevice(args[1:], stdout, stderr)
		case "admin":
			return cmdAdmin(args[1:], stdout, stderr, environ)
		case "bench":
			return cmdBench(args[1:], stdout, stderr)
		case "version":
			_, _ = fmt.Fprintln(stdout, "midopress", buildVersion())
			return 0
		case "help":
			_, _ = fmt.Fprint(stdout, usage)
			return 0
		default:
			_, _ = fmt.Fprintf(stderr, "midopress: unknown command %q\n\n%s", args[0], usage)
			return 2
		}
	}
	return cmdServe(args, stdout, stderr, environ)
}

// serveFlags holds the flags of the proxy. The first group is what compy
// had; they keep working and override the configuration file.
type serveFlags struct {
	fs *flag.FlagSet

	configPath string
	host       string
	cert, key  string
	ca, caKey  string
	user, pass string
	brotli     int
	jpeg       int
	profile    string
	gif        bool
	gzip       int
	png        bool
	minify     bool

	logLevel    string
	logFormat   string
	logURLs     bool
	blockGlobal bool
	version     bool
}

func newServeFlags(name string, stderr io.Writer) *serveFlags {
	f := &serveFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	f.fs.SetOutput(stderr)
	fs := f.fs
	fs.StringVar(&f.configPath, "config", "", "path of the YAML configuration file")
	fs.StringVar(&f.host, "host", ":9999", "<host:port> to listen on")
	fs.StringVar(&f.cert, "cert", "", "proxy cert path")
	fs.StringVar(&f.key, "key", "", "proxy cert key path")
	fs.StringVar(&f.ca, "ca", "", "CA path")
	fs.StringVar(&f.caKey, "cakey", "", "CA key path")
	fs.StringVar(&f.user, "user", "", "proxy user name")
	fs.StringVar(&f.pass, "pass", "", "proxy password")
	fs.IntVar(&f.brotli, "brotli", 0, "Brotli compression level (1-11), 0 uses the one of the profile")
	fs.IntVar(&f.jpeg, "jpeg", 0, "fixed quality of recoded images (1-100) instead of finding it for each image; 0 turns jpeg recoding off")
	fs.StringVar(&f.profile, "profile", "balanced", "how far images are pushed: lossless, balanced, saver or extreme")
	fs.BoolVar(&f.gif, "gif", true, "recode static gifs, animated ones are never touched")
	fs.IntVar(&f.gzip, "gzip", 0, "gzip compression level (1-9), 0 uses the one of the profile")
	fs.BoolVar(&f.png, "png", true, "recode png")
	fs.BoolVar(&f.minify, "minify", false, "minify html, css and javascript (safe mode); breaks Subresource Integrity hashes, see the config file for finer control")
	fs.StringVar(&f.logLevel, "log-level", "info", "log level: debug, info, warn or error")
	fs.StringVar(&f.logFormat, "log-format", "text", "log format: text or json")
	fs.BoolVar(&f.logURLs, "log-urls", false, "log request URLs (off by default for privacy)")
	fs.BoolVar(&f.blockGlobal, "block-global", true, "only accept clients on private or loopback addresses")
	fs.BoolVar(&f.version, "version", false, "print version and exit")
	return f
}

// load builds the configuration: defaults, file, environment, then the flags
// that were given explicitly.
func (f *serveFlags) load(environ []string) (*config.Config, error) {
	cfg, err := config.Load(f.configPath, environ)
	if err != nil {
		return nil, err
	}
	f.fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "host":
			cfg.Listen.Addr = f.host
		case "cert":
			cfg.Listen.TLS.Cert = f.cert
		case "key":
			cfg.Listen.TLS.Key = f.key
		case "ca":
			cfg.MITM.CACert = f.ca
		case "cakey":
			cfg.MITM.CAKey = f.caKey
		case "user":
			cfg.Auth.User = f.user
			if cfg.Auth.Mode == "none" {
				cfg.Auth.Mode = "basic"
			}
		case "pass":
			cfg.Auth.Pass = f.pass
			if cfg.Auth.Mode == "none" {
				cfg.Auth.Mode = "basic"
			}
		case "brotli":
			cfg.Pipeline.Text.Brotli = f.brotli
		case "gzip":
			cfg.Pipeline.Text.Gzip = f.gzip
		case "jpeg":
			cfg.Pipeline.Image.JPEG = f.jpeg != 0
			cfg.Pipeline.Image.JPEGQuality = f.jpeg
		case "profile":
			cfg.Pipeline.Profile = f.profile
		case "gif":
			cfg.Pipeline.Image.GIF = f.gif
		case "png":
			cfg.Pipeline.Image.PNG = f.png
		case "minify":
			mode := "off"
			if f.minify {
				mode = "safe"
			}
			cfg.Pipeline.Text.Minify.HTML, cfg.Pipeline.Text.Minify.CSS, cfg.Pipeline.Text.Minify.JS = mode, mode, mode
		case "log-level":
			cfg.Log.Level = f.logLevel
		case "log-format":
			cfg.Log.Format = f.logFormat
		case "log-urls":
			cfg.Log.URLs = f.logURLs
		case "block-global":
			cfg.Auth.BlockGlobal = f.blockGlobal
		}
	})
	return cfg, nil
}

func newLogger(cfg config.Log, w io.Writer) (*slog.Logger, *slog.LevelVar) {
	level := new(slog.LevelVar)
	var l slog.Level
	_ = l.UnmarshalText([]byte(cfg.Level))
	level.Set(l)
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts)), level
	}
	return slog.New(slog.NewTextHandler(w, opts)), level
}

func cmdServe(args []string, stdout, stderr io.Writer, environ []string) int {
	f := newServeFlags("midopress", stderr)
	if err := f.fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.version {
		_, _ = fmt.Fprintln(stdout, "midopress", buildVersion())
		return 0
	}
	cfg, err := f.load(environ)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress:", err)
		return 1
	}
	if err := server.Check(cfg); err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress: invalid configuration:")
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	log, level := newLogger(cfg.Log, stderr)

	srv, err := server.New(cfg, log,
		server.WithVersion(buildVersion()),
		server.WithLogLevel(level),
		server.WithConfigLoader(func() (*config.Config, error) { return f.load(environ) }),
	)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	log = srv.Logger()
	if err := srv.Listen(); err != nil {
		log.Error("cannot listen", "err", err)
		return 1
	}
	if addr := srv.AdminAddr(); addr != nil {
		token, created := srv.AdminToken()
		log.Info("administration interface", "url", "http://"+addr.String()+"/", "token_file", cfg.Web.TokenPath())
		if created {
			// not through the logger: the token must not end up in a log
			_, _ = fmt.Fprintf(stderr, "\nThe administration token was made. Open\n  http://%s/#/login?token=%s\nIt is kept in %s and `midopress admin token` shows it again.\n\n", addr, token, cfg.Web.TokenPath())
		}
		if host, _, err := net.SplitHostPort(addr.String()); err == nil {
			if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
				log.Warn("the administration interface listens beyond this machine: only the token protects it, put it behind TLS or keep web.addr on the loopback")
			}
		}
	}

	if cfg.Auth.Mode != "none" && cfg.Listen.TLS.Cert == "" {
		log.Warn("authentication is on but the proxy does not use TLS: credentials travel unencrypted, set listen.tls")
	}
	if !cfg.Auth.BlockGlobal {
		log.Warn("auth.block_global is off: clients on public addresses can connect")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	watchReload(ctx, srv, log)
	if f.configPath != "" {
		watchConfigFile(ctx, f.configPath, srv.Reload, log)
	}

	log.Info("midopress listening",
		"version", buildVersion(),
		"addr", srv.Addr().String(),
		"tls", cfg.Listen.TLS.Cert != "",
		"mitm", cfg.MITM.Enabled(),
		"auth", cfg.Auth.Mode,
		"block_global", cfg.Auth.BlockGlobal,
	)
	if err := srv.Run(ctx); err != nil {
		log.Error("server stopped", "err", err)
		return 1
	}

	s := srv.Proxy().Stats()
	ratio := 0.0
	if s.BytesIn > 0 {
		ratio = float64(s.BytesOut) / float64(s.BytesIn) * 100
	}
	log.Info("midopress exiting", "requests", s.Requests, "bytes_in", s.BytesIn, "bytes_out", s.BytesOut, "ratio_percent", fmt.Sprintf("%.1f", ratio))
	return 0
}
