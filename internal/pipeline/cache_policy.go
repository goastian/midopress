package pipeline

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/img"
)

const cacheAlgorithmVersion = "pipeline-v4-private-integrity"

func sharedCacheAllowed(req *http.Request, response http.Header) bool {
	if req != nil {
		if req.Method != http.MethodGet || req.URL != nil && req.URL.User != nil ||
			len(req.Header.Values("Cookie")) > 0 || len(req.Header.Values("Authorization")) > 0 {
			return false
		}
		if privateCacheControl(req.Header) {
			return false
		}
	}
	if privateCacheControl(response) || len(response.Values("Set-Cookie")) > 0 || len(response.Values("WWW-Authenticate")) > 0 {
		return false
	}
	for _, line := range response.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "*", "cookie", "authorization":
				return false
			}
		}
	}
	return true
}

func privateCacheControl(h http.Header) bool {
	return httpx.HeaderHasDirective(h, "Cache-Control", "no-store") || httpx.HeaderHasDirective(h, "Cache-Control", "private") || httpx.HeaderHasDirective(h, "Cache-Control", "no-cache")
}

var executableIdentity struct {
	sync.Once
	value string
}

var processCacheNonce = sync.OnceValue(newCacheNonce)

func newCacheNonce() string {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic("cache identity unavailable")
	}
	return hex.EncodeToString(nonce[:])
}

func binaryCacheIdentity() string {
	executableIdentity.Do(func() {
		name, err := os.Executable()
		if runtime.GOOS == "linux" {
			name, err = "/proc/self/exe", nil
		}
		if err == nil {
			var file *os.File
			file, err = os.Open(name)
			if err == nil {
				defer func() { _ = file.Close() }()
				h := sha256.New()
				if _, err = io.Copy(h, file); err == nil {
					executableIdentity.value = hex.EncodeToString(h.Sum(nil))
					return
				}
			}
		}
		executableIdentity.value = processCacheNonce()
	})
	return executableIdentity.value
}

func cacheBackendIdentity(backend img.Backend, isolated bool) string {
	inventory := img.RuntimeInventory(backend)
	unverified := ""
	for _, component := range inventory {
		if component.Execution == "native" && (component.Version == "" || component.Version == "unreported") {
			unverified = processCacheNonce()
		}
	}
	data, _ := json.Marshal([]any{img.RuntimeInfo(backend), inventory, unverified})
	return fmt.Sprintf("%T|%s|%s|%s|%t", backend, backend.Name(), data, img.JPEGRecompressionExecutionPath(), isolated)
}

func (p *Pipeline) variantKey(kind string, options any, raw []byte) string {
	h := sha256.New()
	if err := json.NewEncoder(h).Encode([]any{cacheAlgorithmVersion, binaryCacheIdentity(), p.cacheBackend, p.cfg.MinGain, p.cfg.Image, kind, options}); err != nil {
		return newCacheNonce()
	}
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func (p *Pipeline) imageCacheKey(raw []byte, profile *img.Profile, caps img.Caps, hints img.Hints, fixed int) string {
	return p.variantKey("image", []any{profile, caps, hints, fixed, p.engine.Metric}, raw)
}

func (p *Pipeline) textCacheKey(fl *flow, target string, plain []byte) string {
	return p.variantKey("text", []any{fl.prof, fl.lv.levels[fl.prof.Name], fl.opts.minify, fl.opts.noTransform, target, fl.lv.allowIntegrityChanges}, plain)
}
