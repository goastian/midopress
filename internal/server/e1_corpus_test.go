package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/imgmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func e1WriteReport(t *testing.T, name string, report any) {
	t.Helper()
	if dir := os.Getenv("MIDOPRESS_E1_REPORT_DIR"); dir != "" {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		body, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), append(body, '\n'), 0o644))
	}
}

func e1Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func e1RasterCanvas(t *testing.T, raw []byte) image.Point {
	t.Helper()
	format := img.Sniff(raw)
	if format == img.GIF {
		animation, err := gif.DecodeAll(bytes.NewReader(raw))
		require.NoError(t, err)
		require.NotEmpty(t, animation.Image)
		return image.Pt(animation.Config.Width, animation.Config.Height)
	}
	var pixels image.Image
	var err error
	switch format {
	case img.PNG:
		pixels, err = png.Decode(bytes.NewReader(raw))
	case img.JPEG:
		pixels, err = jpeg.Decode(bytes.NewReader(raw))
	default:
		pixels, err = img.NewWASM().Decode(format, raw)
	}
	require.NoError(t, err, "raster decode %s (%d bytes)", format, len(raw))
	size := pixels.Bounds().Size()
	orientation := 1
	switch format {
	case img.JPEG:
		meta, err := imgmeta.ParseJPEG(raw)
		require.NoError(t, err)
		orientation = meta.Orientation
	case img.PNG:
		meta, err := imgmeta.ParsePNG(raw)
		require.NoError(t, err)
		orientation = meta.Orientation
	case img.WebP:
		meta, err := imgmeta.ParseWebP(raw)
		require.NoError(t, err)
		orientation = meta.Orientation
	case img.AVIF:
		transforms, err := imgmeta.AVIFPresentation(raw)
		require.NoError(t, err)
		for _, transform := range transforms {
			switch transform.Kind {
			case "irot":
				if transform.Value%2 == 1 {
					size = image.Pt(size.Y, size.X)
				}
			case "clap":
				p := transform.Aperture
				require.Positive(t, p[1])
				require.Positive(t, p[3])
				require.Zero(t, p[0]%p[1])
				require.Zero(t, p[2]%p[3])
				size = image.Pt(int(p[0]/p[1]), int(p[2]/p[3]))
			}
		}
	}
	if orientation >= 5 {
		size = image.Pt(size.Y, size.X)
	}
	return size
}

func e1Metric(body, name string) float64 {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			var v float64
			_, _ = fmt.Sscan(fields[1], &v)
			return v
		}
	}
	return 0
}

func e1Metrics(t *testing.T, h *harness) string {
	t.Helper()
	resp, err := http.Get("http://" + h.proxyAddr + "/metrics")
	require.NoError(t, err)
	body, err := readAll(resp)
	require.NoError(t, err)
	return string(body)
}

func e1Settle(t *testing.T, h *harness) bool {
	t.Helper()
	end, quiet := time.Now().Add(90*time.Second), 0
	for time.Now().Before(end) {
		m := e1Metrics(t, h)
		if e1Metric(m, "midopress_background_queue")+e1Metric(m, "midopress_cpu_slots_busy") == 0 {
			quiet++
		} else {
			quiet = 0
		}
		if quiet == 3 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

type e1CorpusRow struct {
	Resource    string  `json:"resource"`
	ID          string  `json:"id"`
	Status      int     `json:"status"`
	SourceType  string  `json:"source_type"`
	ResultType  string  `json:"result_type"`
	Encoding    string  `json:"encoding"`
	Protocol    string  `json:"protocol"`
	OriginBytes int     `json:"origin_bytes"`
	ClientBytes int     `json:"client_bytes"`
	Saved       int64   `json:"saved"`
	TTFBMS      float64 `json:"ttfb_ms"`
	TotalMS     float64 `json:"total_ms"`
	Validation  string  `json:"validation"`
	Reason      string  `json:"reason"`
	CachePolicy string  `json:"cache_policy"`
}

type e1CorpusPass struct {
	Rows        []e1CorpusRow  `json:"rows"`
	OriginBytes int            `json:"origin_bytes"`
	ClientBytes int            `json:"client_bytes"`
	Saved       int64          `json:"saved"`
	CacheHits   float64        `json:"cache_hits"`
	Seconds     float64        `json:"seconds"`
	P50MS       float64        `json:"p50_ms"`
	P95MS       float64        `json:"p95_ms"`
	Reasons     map[string]int `json:"reasons"`
}

func e1CorpusPassRun(t *testing.T, h *harness, origin *replayOrigin, entries []corpusEntry) e1CorpusPass {
	t.Helper()
	start := time.Now()
	beforeMetric := e1Metrics(t, h)
	pass := e1CorpusPass{Reasons: map[string]int{}}
	client := h.client()
	text := codec.NewText(codec.Levels{Brotli: 5, Gzip: 6, Zstd: 3})
	for _, entry := range entries {
		before := h.srv.Stats().Snapshot(0)
		req, err := http.NewRequest(http.MethodGet, entry.URL, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "text/html,application/javascript,text/css,image/avif,image/webp,*/*;q=0.8")
		req.Header.Set("Accept-Encoding", "gzip, br, zstd")
		at := time.Now()
		resp, err := client.Do(req)
		require.NoError(t, err, entry.URL)
		ttfb := float64(time.Since(at).Microseconds()) / 1000
		wire, err := readAll(resp)
		require.NoError(t, err, entry.URL)
		total := float64(time.Since(at).Microseconds()) / 1000
		require.Eventually(t, func() bool { return h.srv.Stats().Snapshot(0).Totals.Requests >= before.Totals.Requests+1 }, time.Second, time.Millisecond)
		after := h.srv.Stats().Snapshot(0)
		var source, result bytes.Buffer
		coding, _ := codec.ParseEncoding(entry.Headers.Get("Content-Encoding"))
		require.NoError(t, text.Decode(coding, entry.Body, &source, 64<<20))
		coding, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
		require.True(t, ok)
		require.NoError(t, text.Decode(coding, wire, &result, 64<<20))
		validation := "entity_identical"
		if !bytes.Equal(source.Bytes(), result.Bytes()) {
			require.True(t, strings.HasPrefix(mediaType(entry.Headers), "image/"), "text must remain byte-identical after decoding")
			f := img.Sniff(result.Bytes())
			require.NotEqual(t, img.Unknown, f)
			assert.Equal(t, e1RasterCanvas(t, source.Bytes()), e1RasterCanvas(t, result.Bytes()))
			validation = "raster_decodes_same_canvas; engine quality policy tested separately"
		}
		require.Equal(t, entry.Status, resp.StatusCode)
		origin.mu.Lock()
		received := origin.sent[entry.Key]
		origin.mu.Unlock()
		assert.LessOrEqual(t, len(wire), received, "accepted coding must not grow transfer")
		u, err := url.Parse(entry.URL)
		require.NoError(t, err)
		row := e1CorpusRow{Resource: u.Hostname() + u.EscapedPath(), ID: e1Digest([]byte(entry.URL))[:16], Status: resp.StatusCode,
			SourceType: mediaType(entry.Headers), ResultType: mediaType(resp.Header), Encoding: resp.Header.Get("Content-Encoding"), Protocol: resp.Proto,
			OriginBytes: received, ClientBytes: len(wire), Saved: after.Totals.Saved - before.Totals.Saved, TTFBMS: ttfb, TotalMS: total, Validation: validation, CachePolicy: entry.Headers.Get("Cache-Control")}
		for _, reason := range after.ByReason {
			var previous uint64
			for _, b := range before.ByReason {
				if b.Reason == reason.Reason && b.Kind == reason.Kind {
					previous = b.Requests
				}
			}
			if reason.Requests > previous {
				row.Reason = reason.Reason
				break
			}
		}
		assert.Equal(t, int64(received-len(wire)), row.Saved, "credited saving must describe a completed body")
		pass.Rows = append(pass.Rows, row)
		pass.OriginBytes += received
		pass.ClientBytes += len(wire)
		pass.Saved += row.Saved
		pass.Reasons[row.Reason]++
	}
	pass.Seconds = time.Since(start).Seconds()
	pass.CacheHits = e1Metric(e1Metrics(t, h), "midopress_cache_hits_total") - e1Metric(beforeMetric, "midopress_cache_hits_total")
	latencies := make([]float64, len(pass.Rows))
	for i, r := range pass.Rows {
		latencies[i] = r.TotalMS
	}
	sort.Float64s(latencies)
	pass.P50MS = latencies[(len(latencies)*50+99)/100-1]
	pass.P95MS = latencies[(len(latencies)*95+99)/100-1]
	return pass
}

func TestE1FixedCorpusColdWarm(t *testing.T) {
	if os.Getenv("MIDOPRESS_E1") != "1" {
		t.Skip("explicit corpus baseline: MIDOPRESS_E1=1; scripts/e1-validate.py")
	}
	entries := loadCorpus(t)
	har, err := os.ReadFile("../../testdata/corpus/corpus.har")
	require.NoError(t, err)
	build, _ := debug.ReadBuildInfo()
	report := map[string]any{"schema": 1, "generated": time.Now().UTC(), "corpus_sha256": e1Digest(har), "entries": len(entries),
		"go": runtime.Version(), "gomaxprocs": runtime.GOMAXPROCS(0), "build": build, "runtime": img.RuntimeInfo(img.NewWASM()),
		"components": img.RuntimeInventory(img.NewWASM()),
		"scope":      "fixed HTTPS HAR replay on isolated origins and proxies; body bytes, local TTFB/total; not Internet bandwidth or browser LCP"}
	var runs []map[string]any
	defer func() { report["runs"] = runs; e1WriteReport(t, "corpus", report) }()
	for _, name := range []string{img.ProfileBalanced, img.ProfileSaver, img.ProfileExtreme, img.ProfileLossless} {
		if selected := os.Getenv("MIDOPRESS_E1_PROFILE"); selected != "" && selected != name {
			continue
		}
		t.Run(name, func(t *testing.T) {
			origin, srv, pool := startReplayOrigin(t, entries)
			h := newHarness(t, withOrigin(srv, pool), withConfig(func(c *configT) { c.Pipeline.Profile = name; c.MITM.DefaultPassthrough = false }))
			assert.Zero(t, h.srv.CacheLen(), "each profile has a fresh conversion cache")
			cold := e1CorpusPassRun(t, h, origin, entries)
			settled := e1Settle(t, h)
			assert.True(t, settled, "background must finish before warm pass")
			warm := e1CorpusPassRun(t, h, origin, entries)
			profile, _ := img.Lookup(name)
			profileBytes, _ := json.Marshal(profile)
			runs = append(runs, map[string]any{"profile": name, "profile_sha256": e1Digest(profileBytes), "profile_config": profile, "pipeline_config": h.cfg.Pipeline, "cold": cold, "warm": warm, "settled": settled, "cache_entries": h.srv.CacheLen(), "stats": h.srv.Stats().Snapshot(0)})
			e1WriteReport(t, "corpus", map[string]any{"corpus_sha256": e1Digest(har), "entries": len(entries), "runs": runs})
			t.Logf("%s cold %d->%d; warm %d->%d; warm cache hits %.0f; body completion validated", name, cold.OriginBytes, cold.ClientBytes, warm.OriginBytes, warm.ClientBytes, warm.CacheHits)
		})
	}
}
