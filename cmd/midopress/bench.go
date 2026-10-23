package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	stdjpeg "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/imgmeta"
	"github.com/goastian/midopress/internal/quality"
)

// benchCaps are the client capability sets the benchmark simulates.
var benchCaps = map[string]img.Caps{
	"legacy": {},
	"webp":   {WebP: true},
	"modern": {WebP: true, AVIF: true},
	"all":    {WebP: true, AVIF: true, JXL: true},
}

type benchImage struct {
	name     string
	category string
	raw      []byte
	format   img.Format
}

type benchRow struct {
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Caps     string  `json:"caps"`
	Profile  string  `json:"profile"`
	Original int     `json:"original_bytes"`
	Output   int     `json:"output_bytes"`
	Format   string  `json:"format"`
	Lossless bool    `json:"lossless"`
	Metric   string  `json:"metric,omitempty"`
	Measure  float64 `json:"measure"`
	DSSIM    float64 `json:"verified_dssim"` // measured again after decoding the output; -1 when not measured
	Millis   float64 `json:"ms"`
	Changed  bool    `json:"changed"`
	Note     string  `json:"note,omitempty"`
	Error    string  `json:"error,omitempty"`
}

func cmdBench(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midopress bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profiles := fs.String("profiles", "lossless,balanced,saver,extreme", "profiles to run, comma separated")
	capsList := fs.String("caps", "legacy,webp,modern,all", "client capabilities to simulate: legacy, webp, modern (webp+avif), all (webp+avif+jxl)")
	jobs := fs.Int("jobs", runtime.NumCPU(), "images processed at the same time")
	budget := fs.Duration("budget", 20*time.Second, "CPU time allowed per image, as in the proxy")
	limit := fs.Int("limit", 0, "use only the first N images (0 for all)")
	derive := fs.String("derive-jpeg", "", "also make JPEGs at these qualities from the PNG photographs (kodak-*), comma separated, e.g. 75,90")
	viewport := fs.String("viewport", "", "viewport hint as WIDTHxDPR, e.g. 400x2, used by profiles that resize")
	verify := fs.Bool("verify", false, "measure each result again, decoding it independently of the engine")
	tryAll := fs.Bool("try-all", false, "try every allowed format and keep the smallest, instead of stopping at the first that does well")
	minGain := fs.Float64("min-gain", 0.02, "a result must be this fraction smaller than the original to count")
	trace := fs.Bool("trace", false, "print every encode the quality search makes, to stderr")
	metricName := fs.String("metric", "dssim", "metric that decides the quality: dssim, ssim or ssimulacra2")
	backendName := fs.String("backend", "wasm", "image backend: wasm or vips (needs the vips build tag)")
	dssimLimit := fs.Float64("dssim-limit", 0.002, "count the results whose measured dssim is at most this (needs -verify)")
	effort := fs.Int("effort", 0, "how hard AVIF is tried, 1 to 10, to compare what the extra CPU buys; 0 is what the profile says")
	passes := fs.Int("passes", 0, "instead of the engine, run the whole pipeline with its cache this many times over the images and report hit rate and latency")
	verbose := fs.Bool("v", false, "print one line per image")
	asJSON := fs.Bool("json", false, "print every row as JSON instead of tables")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: midopress bench [flags] <directory with images>\n(fetch a corpus with: go run ./tools/fetch-images -dir DIR)")
		return 2
	}

	images, err := loadBenchImages(fs.Arg(0), *derive, *limit)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress bench:", err)
		return 1
	}
	if len(images) == 0 {
		_, _ = fmt.Fprintln(stderr, "midopress bench: no images found")
		return 1
	}
	var hints img.Hints
	if *viewport != "" {
		w, dpr, _ := strings.Cut(*viewport, "x")
		hints.ViewportWidth, _ = strconv.Atoi(w)
		hints.DPR, _ = strconv.ParseFloat(dpr, 64)
	}

	be0, err := img.NewBackend(*backendName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress bench:", err)
		return 2
	}
	metric, err := img.ParseMetric(*metricName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "midopress bench:", err)
		return 2
	}
	if *passes > 0 {
		return benchPipeline(images, *passes, *profiles, *capsList, hints, *metricName, be0, *jobs, stdout, stderr)
	}
	engine := img.NewEngine(be0)
	engine.Metric = metric
	if *trace {
		engine.Trace = func(f string, a ...any) { _, _ = fmt.Fprintf(stderr, f+"\n", a...) }
	}
	be := engine.Backend()
	_, _ = fmt.Fprintf(stderr, "%d images, backend %s (webp native: %v, avif native: %v, jxl native: %v), %d jobs\n",
		len(images), be.Name(), be.Native(img.WebP), be.Native(img.AVIF), be.Native(img.JXL), *jobs)

	var rows []benchRow
	for _, capName := range splitList(*capsList) {
		caps, ok := benchCaps[capName]
		if !ok {
			_, _ = fmt.Fprintf(stderr, "midopress bench: unknown caps %q\n", capName)
			return 2
		}
		for _, profName := range splitList(*profiles) {
			prof, err := img.Lookup(profName)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, "midopress bench:", err)
				return 2
			}
			if *effort > 0 {
				cp := *prof
				cp.AVIFEffort = *effort
				prof = &cp
			}
			_, _ = fmt.Fprintf(stderr, "running %s / %s ...\n", capName, profName)
			rows = append(rows, runBench(engine, images, capName, caps, prof, hints, *budget, *jobs, *verify, *tryAll, *minGain)...)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(rows)
		return 0
	}
	printBench(stdout, rows, *verbose, *dssimLimit)
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadBenchImages(dir, derive string, limit int) ([]benchImage, error) {
	var out []benchImage
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		default:
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		out = append(out, benchImage{name: name, category: category(name, img.Sniff(raw)), raw: raw, format: img.Sniff(raw)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })

	for _, q := range splitList(derive) {
		quality, err := strconv.Atoi(q)
		if err != nil || quality < 1 || quality > 100 {
			return nil, fmt.Errorf("bad -derive-jpeg quality %q", q)
		}
		for _, b := range out {
			if !strings.HasPrefix(b.name, "kodak-") || b.format != img.PNG {
				continue
			}
			m, err := png.Decode(bytes.NewReader(b.raw))
			if err != nil {
				continue
			}
			var buf bytes.Buffer
			if err := stdjpeg.Encode(&buf, m, &stdjpeg.Options{Quality: quality}); err != nil {
				continue
			}
			out = append(out, benchImage{
				name: fmt.Sprintf("%s.q%d.jpg", strings.TrimSuffix(b.name, ".png"), quality), category: "photo-jpeg-derived",
				raw: buf.Bytes(), format: img.JPEG,
			})
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// category groups images so that a good result on one kind does not hide a
// poor one on another.
func category(name string, f img.Format) string {
	switch {
	case strings.HasPrefix(name, "photo-") || strings.HasPrefix(name, "gallery-") && f == img.JPEG:
		return "photo-jpeg"
	case strings.HasPrefix(name, "kodak-"), strings.HasPrefix(name, "gallery3-"):
		return "photo-png"
	case strings.HasPrefix(name, "graphic-"):
		return "graphic-png"
	case strings.HasPrefix(name, "anim-"):
		return "animation-gif"
	}
	return f.String()
}

func runBench(engine *img.Engine, images []benchImage, capName string, caps img.Caps, prof *img.Profile, hints img.Hints, budget time.Duration, jobs int, verify, tryAll bool, minGain float64) []benchRow {
	rows := make([]benchRow, len(images))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(jobs, 1))
	for i, b := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i] = benchOne(engine, b, capName, caps, prof, hints, budget, verify, tryAll, minGain)
		}()
	}
	wg.Wait()
	return rows
}

func benchOne(engine *img.Engine, b benchImage, capName string, caps img.Caps, prof *img.Profile, hints img.Hints, budget time.Duration, verify, tryAll bool, minGain float64) benchRow {
	row := benchRow{DSSIM: -1, Name: b.name, Category: b.category, Caps: capName, Profile: prof.Name, Original: len(b.raw), Output: len(b.raw), Format: b.format.String()}
	ctx, cancel := context.WithTimeout(context.Background(), budget*2)
	defer cancel()
	start := time.Now()
	res, err := engine.Optimize(ctx, img.Input{Raw: b.raw, Format: b.format, Caps: caps, Profile: prof, Hints: hints, Budget: budget, TryAll: tryAll})
	row.Millis = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Note, row.Lossless, row.Measure, row.Metric = res.Note, res.Lossless, res.Measure, string(res.Metric)
	if float64(len(res.Data)) <= float64(len(b.raw))*(1-minGain) {
		row.Output, row.Format, row.Changed = len(res.Data), res.Format.String(), true
	} else {
		row.Lossless, row.Note = true, "kept the original"
	}
	if verify && row.Changed && !res.Lossless && res.Format != img.GIF {
		row.DSSIM = verifyResult(engine, b, res, prof, hints)
	}
	return row
}

// verifyResult measures a result the way the engine did not: it decodes the
// output with the backend and compares it with the original decoded by the
// standard library, whole and with the dssim implementation. It returns -1 when
// the result can not be compared (it was resized, or could not be decoded).
func verifyResult(engine *img.Engine, b benchImage, res *img.Result, prof *img.Profile, hints img.Hints) float64 {
	var src image.Image
	var err error
	switch b.format {
	case img.JPEG:
		src, err = stdjpeg.Decode(bytes.NewReader(b.raw))
		if m, merr := imgmeta.ParseJPEG(b.raw); err == nil && merr == nil {
			src = img.Orient(src, m.Orientation)
		}
	case img.PNG:
		src, err = png.Decode(bytes.NewReader(b.raw))
	case img.WebP, img.AVIF:
		src, err = engine.Backend().Decode(b.format, b.raw)
	}
	if err != nil || src == nil || res.Resized {
		return -1
	}
	got, err := engine.Backend().Decode(res.Format, res.Data)
	if err != nil || got.Bounds().Size() != src.Bounds().Size() {
		return -1
	}
	d, err := quality.DSSIM(src, got)
	if err != nil {
		return -1
	}
	return d
}

type agg struct {
	n, changed int
	orig, out  int
	lossy      int     // changed images that are not bit exact
	dssimSum   float64 // of the measured ones
	dssimMax   float64
	measured   int
	over       int // measured above the limit
	limit      float64
	ms         float64
	formats    map[string]int
	errors     int
}

func (a *agg) add(r benchRow) {
	if a.formats == nil {
		a.formats = map[string]int{}
	}
	a.n++
	a.orig += r.Original
	a.out += r.Output
	a.ms += r.Millis
	if r.Error != "" {
		a.errors++
		return
	}
	if !r.Changed {
		return
	}
	a.changed++
	a.formats[r.Format]++
	if r.Lossless {
		return
	}
	a.lossy++
	if r.DSSIM >= 0 {
		a.measured++
		a.dssimSum += r.DSSIM
		a.dssimMax = max(a.dssimMax, r.DSSIM)
		if r.DSSIM > a.limit {
			a.over++
		}
	}
}

func (a *agg) saved() float64 {
	if a.orig == 0 {
		return 0
	}
	return 100 - float64(a.out)*100/float64(a.orig)
}

func (a *agg) formatList() string {
	var parts []string
	for f, n := range a.formats {
		parts = append(parts, fmt.Sprintf("%s %d", f, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func printBench(w io.Writer, rows []benchRow, verbose bool, limit float64) {
	type key struct{ caps, profile string }
	var order []key
	groups := map[key][]benchRow{}
	for _, r := range rows {
		k := key{r.Caps, r.Profile}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}

	for _, k := range order {
		rs := groups[k]
		_, _ = fmt.Fprintf(w, "\n### client: %s, profile: %s\n\n", k.caps, k.profile)
		if verbose {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "image\toriginal\toutput\tsaved\tformat\tmetric\tdssim\tms\tnote")
			for _, r := range rs {
				m, d := "-", "-"
				if r.Changed && !r.Lossless {
					m = fmt.Sprintf("%s %.5f", r.Metric, r.Measure)
					if r.DSSIM >= 0 {
						d = fmt.Sprintf("%.5f", r.DSSIM)
					}
				}
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f%%\t%s\t%s\t%s\t%.0f\t%s%s\n", r.Name, r.Original, r.Output,
					100-float64(r.Output)*100/float64(r.Original), r.Format, m, d, r.Millis, r.Note, r.Error)
			}
			_ = tw.Flush()
			_, _ = fmt.Fprintln(w)
		}

		cats := map[string]*agg{}
		var names []string
		total := &agg{limit: limit}
		for _, r := range rs {
			if cats[r.Category] == nil {
				cats[r.Category] = &agg{limit: limit}
				names = append(names, r.Category)
			}
			cats[r.Category].add(r)
			total.add(r)
		}
		sort.Strings(names)

		_, _ = fmt.Fprintln(w, "| category | images | changed | original | output | saved | lossy | mean dssim | worst dssim | formats | ms/image |")
		_, _ = fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|")
		line := func(name string, a *agg) {
			mean, worst := "-", "-"
			if a.measured > 0 {
				mean, worst = fmt.Sprintf("%.5f", a.dssimSum/float64(a.measured)), fmt.Sprintf("%.5f", a.dssimMax)
			}
			_, _ = fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %.1f%% | %d | %s | %s | %s | %.0f |\n",
				name, a.n, a.changed, a.orig, a.out, a.saved(), a.lossy, mean, worst, a.formatList(), a.ms/float64(a.n))
		}
		for _, n := range names {
			line(n, cats[n])
		}
		line("**all**", total)
		if total.measured > 0 {
			_, _ = fmt.Fprintf(w, "\nmeasured again after decoding: %d lossy results, %d above dssim %g, worst %.5f\n",
				total.measured, total.over, limit, total.dssimMax)
		}
		if total.errors > 0 {
			_, _ = fmt.Fprintf(w, "\n%d images could not be processed (left unchanged)\n", total.errors)
		}
	}
}
