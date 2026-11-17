package codecworker

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/textmin"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "codec-worker" {
		if err := Run(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func workerContext(t *testing.T) (context.Context, string) {
	t.Helper()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("permission policy unavailable on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	exe, err := os.Executable()
	require.NoError(t, err)
	return ctx, exe
}

func TestRealWorkerEnforcesPermissionsAndTransformsText(t *testing.T) {
	ctx, exe := workerContext(t)
	status, err := Inspect(ctx, exe)
	require.NoError(t, err)
	assert.True(t, status.Enforced)
	assert.Equal(t, "all-existing-and-future", status.Threads)
	assert.Equal(t, "denied", status.Network)
	assert.Greater(t, status.AddressSpaceBytes, uint64(0))
	plain := bytes.Repeat([]byte("codec text with exact lossless bytes\n"), 500)
	for _, enc := range []codec.Encoding{codec.Gzip, codec.Brotli, codec.Zstd} {
		encoded, err := Compress(ctx, exe, enc, plain, codec.Levels{Brotli: 6, Gzip: 6, Zstd: 3})
		require.NoError(t, err)
		require.Less(t, len(encoded), len(plain))
		decoded, err := Decode(ctx, exe, enc, encoded, int64(len(plain)))
		require.NoError(t, err)
		assert.Equal(t, plain, decoded)
		_, err = Decode(ctx, exe, enc, encoded, 32)
		assert.Error(t, err)
	}
	minified, err := Minify(ctx, exe, "application/json", []byte("{  \"a\" : [ 1, 2 ] }"), textmin.DefaultConfig())
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":[1,2]}`, string(minified))
	base, err := url.Parse("https://example.test/index")
	require.NoError(t, err)
	hints, err := PageHints(ctx, exe, base, []byte(`<img src="/photo.jpg" width="320"><script src="/app.js" integrity="sha384-test"></script>`))
	require.NoError(t, err)
	assert.Equal(t, 320, hints.Images["https://example.test/photo.jpg"])
	assert.Contains(t, hints.Integrity, "https://example.test/app.js")
}

func TestImageAndFontParsingRunInsideRealWorker(t *testing.T) {
	ctx, exe := workerContext(t)
	pic := image.NewRGBA(image.Rect(0, 0, 192, 128))
	for y := range 128 {
		for x := range 192 {
			pic.SetRGBA(x, y, color.RGBA{byte(x*13 + y), byte(y*7 + x), byte(x*x + y*3), 255})
		}
	}
	var raw bytes.Buffer
	require.NoError(t, jpeg.Encode(&raw, pic, &jpeg.Options{Quality: 95}))
	profile, _ := img.Lookup(img.ProfileExtreme)
	for _, format := range []img.Format{img.WebP, img.AVIF} {
		ctx, exe = workerContext(t)
		p := *profile
		p.Formats = []img.Format{format}
		res, err := Optimize(ctx, exe, "wasm", img.Input{Raw: raw.Bytes(), Format: img.JPEG, Profile: &p, Caps: img.Caps{AVIF: format == img.AVIF, WebP: format == img.WebP}, VerifyQuality: true}, 192*128)
		require.NoError(t, err, format.String())
		assert.Equal(t, format, res.Format)
		assert.Less(t, len(res.Data), raw.Len())
	}
	_, err := Optimize(ctx, exe, "wasm", img.Input{Raw: raw.Bytes(), Format: img.JPEG, Profile: profile}, 100)
	assert.Error(t, err)
	font, err := os.ReadFile(filepath.Join("..", "fonts", "testdata", "AnonymousPro-Regular.ttf"))
	require.NoError(t, err)
	ctx, exe = workerContext(t)
	converted, err := Font(ctx, exe, font)
	require.NoError(t, err)
	assert.Equal(t, "wOF2", string(converted[:4]))
	_, err = Font(ctx, exe, []byte("invalid font"))
	assert.Error(t, err)
}

func TestParentKillsRealWorkerAtDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exe, err := os.Executable()
	require.NoError(t, err)
	_, err = Call(ctx, exe, Request{Operation: "probe"})
	assert.ErrorIs(t, err, context.Canceled)
	active, waiting, _ := Workers()
	assert.Zero(t, active)
	assert.Zero(t, waiting)
}

func TestParentRejectsMissingEnforcementAndKillsCodecInProgress(t *testing.T) {
	_, exe := workerContext(t)
	fake := filepath.Join(t.TempDir(), "unsandboxed")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s' '{\"Sandbox\":{\"enforced\":false}}'\n"), 0700))
	_, err := Call(context.Background(), fake, Request{Operation: "probe"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pic := image.NewRGBA(image.Rect(0, 0, 512, 512))
	var raw bytes.Buffer
	require.NoError(t, png.Encode(&raw, pic))
	profile, _ := img.Lookup(img.ProfileLossless)
	p := *profile
	p.Formats = []img.Format{img.JXL}
	_, err = Optimize(ctx, exe, "wasm", img.Input{Raw: raw.Bytes(), Format: img.PNG, Profile: &p, Caps: img.Caps{JXL: true}, OutputFormat: img.JXL}, 512*512)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected worker cancellation, got %v", err)
	}
	a, q, _ := Workers()
	assert.Zero(t, a)
	assert.Zero(t, q)
}
