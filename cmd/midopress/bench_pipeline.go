package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/xcache"
)

var benchAccept = map[string]string{
	"legacy": "image/png,image/jpeg,*/*;q=0.8",
	"webp":   "image/webp,image/png,*/*;q=0.8",
	"modern": "image/avif,image/webp,image/png,*/*;q=0.8",
	"all":    "image/jxl,image/avif,image/webp,image/png,*/*;q=0.8",
}

// benchPipeline runs the images through the whole pipeline, with its cache and
// its background work, several times, which is what a proxy does when the same
// pages are visited again. It reports, for each pass, how many lookups the
// cache answered and how long a request took.
func benchPipeline(images []benchImage, passes int, profiles, capsList string, hints img.Hints, metric string, be img.Backend, jobs int, stdout, stderr io.Writer) int {
	profile := splitList(profiles)[0]
	capName := splitList(capsList)[0]
	accept, ok := benchAccept[capName]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "midopress bench: unknown caps %q\n", capName)
		return 2
	}
	cache := xcache.New(2 << 30)
	cfg := pipeline.DefaultConfig()
	cfg.Profile = profile
	cfg.Image.Metric = metric
	cfg.Image.MinBytes = 1
	cfg.Image.MaxBytes = 64 << 20
	cfg.Image.MaxPixels = 100_000_000
	cfg.Backend = be
	cfg.Cache = cache
	pl := pipeline.New(cfg)
	defer pl.Close()

	_, _ = fmt.Fprintf(stderr, "%d images, %d passes, profile %s, client %s\n", len(images), passes, profile, capName)
	_, _ = fmt.Fprintln(stdout, "| pass | requests | transformed | kept | cache hit rate | origin bytes | sent bytes | saved | p50 ms | p95 ms | p99 ms |")
	_, _ = fmt.Fprintln(stdout, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")

	for pass := 1; pass <= passes; pass++ {
		if pass > 1 {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			start := time.Now()
			done := pl.WaitBackground(ctx)
			cancel()
			_, _ = fmt.Fprintf(stderr, "background work finished: %v, waited %s\n", done, time.Since(start).Round(time.Millisecond))
		}
		before := cache.Stats()
		lat := make([]time.Duration, len(images))
		var in, out int64
		var transformed int
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, max(jobs, 1))
		for i, b := range images {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				req := httptest.NewRequest(http.MethodGet, "https://corpus.test/"+url.PathEscape(b.name), nil)
				req.Header.Set("Accept", accept)
				if hints.ViewportWidth > 0 {
					req.Header.Set("Sec-CH-Viewport-Width", fmt.Sprint(hints.ViewportWidth))
					req.Header.Set("Sec-CH-DPR", fmt.Sprint(hints.DPR))
				}
				resp := &http.Response{
					StatusCode: http.StatusOK, ContentLength: int64(len(b.raw)),
					Header: http.Header{"Content-Type": {b.format.MIME()}, "Cache-Control": {"public, max-age=86400"}},
					Body:   io.NopCloser(bytes.NewReader(b.raw)),
				}
				rec := httptest.NewRecorder()
				start := time.Now()
				o := pl.Handle(rec, req, resp)
				lat[i] = time.Since(start)
				mu.Lock()
				in += int64(len(b.raw))
				if o.Reason == pipeline.Transformed {
					transformed++
					out += int64(rec.Body.Len())
				} else {
					out += int64(len(b.raw))
				}
				mu.Unlock()
			}()
		}
		wg.Wait()
		after := cache.Stats()
		hits, misses := after.Hits()-before.Hits(), after.Misses-before.Misses
		rate := 0.0
		if hits+misses > 0 {
			rate = float64(hits) / float64(hits+misses) * 100
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		pct := func(p float64) float64 {
			return float64(lat[min(int(p*float64(len(lat))), len(lat)-1)].Microseconds()) / 1000
		}
		_, _ = fmt.Fprintf(stdout, "| %d | %d | %d | %d | %.1f%% | %d | %d | %.1f%% | %.1f | %.1f | %.1f |\n",
			pass, len(images), transformed, len(images)-transformed, rate, in, out,
			100-float64(out)*100/float64(in), pct(.50), pct(.95), pct(.99))
	}
	_, _ = fmt.Fprintln(stdout, strings.TrimSpace(`
Pass 1 starts with an empty cache: what does not fit in the request budget is answered with the fast result and finished in the background. Later passes wait for that work first, as a user coming back later would find it done.`))
	return 0
}
