package pipeline

import (
	"net/http"
	"time"

	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/metrics"
)

type instruments struct {
	outcomes   *metrics.CounterVec
	bytesIn    *metrics.CounterVec
	bytesOut   *metrics.CounterVec
	seconds    *metrics.HistogramVec
	encode     *metrics.HistogramVec
	bgJobs     *metrics.CounterVec
	imageSaved *metrics.CounterVec

	reasonResponses *metrics.CounterVec
	reasonBytes     *metrics.CounterVec
	causeResponses  *metrics.CounterVec
	causeBytes      *metrics.CounterVec
	noTransform     *metrics.CounterVec
	memoryWait      *metrics.HistogramVec
}

// Instrument registers the pipeline's metrics. It is meant to be called once.
func (p *Pipeline) Instrument(reg *metrics.Registry) {
	in := &instruments{
		outcomes: reg.NewCounterVec("midopress_responses_total", "Responses by what the pipeline did: transformed or the reason it was left alone.", "reason"),
		bytesIn:  reg.NewCounterVec("midopress_pipeline_bytes_in_total", "Body bytes read from origins, by kind of content.", "kind"),
		bytesOut: reg.NewCounterVec("midopress_pipeline_bytes_out_total", "Body bytes sent to clients, by kind of content.", "kind"),
		seconds: reg.NewHistogramVec("midopress_pipeline_seconds", "Time the pipeline took to handle a response, by kind of content.",
			[]float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}, "kind"),
		encode: reg.NewHistogramVec("midopress_image_attempt_seconds", "Time of one attempt of the quality search (encode, decode, measure), by format.",
			[]float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}, "format"),
		bgJobs:     reg.NewCounterVec("midopress_background_jobs_total", "Images finished in the background, by result.", "result"),
		imageSaved: reg.NewCounterVec("midopress_image_saved_bytes_total", "Bytes saved on images, by the media type sent.", "format"),

		reasonResponses: reg.NewCounterVec("midopress_pipeline_reason_responses_total", "Responses by what the pipeline did and the kind of content.", "reason", "kind"),
		reasonBytes:     reg.NewCounterVec("midopress_pipeline_reason_bytes_total", "Body bytes read from origins, by what the pipeline did and the kind of content. The ones whose reason is not transformed are the bytes that were not saved.", "reason", "kind"),
		causeResponses:  reg.NewCounterVec("midopress_pipeline_cause_responses_total", "Responses left alone, by reason and its cause: why the pipeline was busy, the media type that is not handled, the status.", "reason", "cause"),
		memoryWait: reg.NewHistogramVec("midopress_pipeline_memory_wait_seconds", "Time a response waited for room in the memory budget, by whether it got it.",
			[]float64{.001, .005, .01, .025, .05, .1, .25, .5, 1}, "result"),
		noTransform: reg.NewCounterVec("midopress_pipeline_no_transform_overridden_total", "Responses that said Cache-Control: no-transform and were transformed anyway because the profile or the configuration allows it, by kind of content.", "kind"),
		causeBytes:  reg.NewCounterVec("midopress_pipeline_cause_bytes_total", "Body bytes read from origins for the responses of midopress_pipeline_cause_responses_total.", "reason", "cause"),
	}
	p.inst.Store(in)

	reg.NewCounterFunc("midopress_cache_hits_total", "Cache lookups answered from the cache.", func() float64 { return float64(p.cache.Stats().Hits()) })
	reg.NewCounterFunc("midopress_cache_memory_hits_total", "Cache lookups answered from memory.", func() float64 { return float64(p.cache.Stats().MemoryHits) })
	reg.NewCounterFunc("midopress_cache_disk_hits_total", "Cache lookups answered from disk.", func() float64 { return float64(p.cache.Stats().DiskHits) })
	reg.NewCounterFunc("midopress_cache_misses_total", "Cache lookups that found nothing.", func() float64 { return float64(p.cache.Stats().Misses) })
	reg.NewCounterFunc("midopress_cache_evictions_total", "Entries dropped to make room.", func() float64 { return float64(p.cache.Stats().Evictions) })
	reg.NewCounterFunc("midopress_cache_expired_total", "Entries dropped because their time was up.", func() float64 { return float64(p.cache.Stats().Expired) })
	reg.NewCounterFunc("midopress_cache_disk_writes_total", "Files written to the disk tier.", func() float64 { return float64(p.cache.Stats().DiskWrites) })
	reg.NewCounterFunc("midopress_cache_disk_errors_total", "Failed or dropped disk writes and unreadable files.", func() float64 { return float64(p.cache.Stats().DiskErrors) })
	reg.NewGaugeFunc("midopress_cache_hit_ratio", "Share of cache lookups answered from the cache since start.", func() float64 { return p.cache.Stats().HitRate() })
	reg.NewGaugeFunc("midopress_cache_memory_bytes", "Bytes held in the memory tier.", func() float64 { return float64(p.cache.Stats().MemoryBytes) })
	reg.NewGaugeFunc("midopress_cache_memory_entries", "Entries in the memory tier.", func() float64 { return float64(p.cache.Stats().MemoryItems) })
	reg.NewGaugeFunc("midopress_cache_disk_bytes", "Bytes held in the disk tier.", func() float64 { return float64(p.cache.Stats().DiskBytes) })
	reg.NewGaugeFunc("midopress_cache_disk_entries", "Entries in the disk tier.", func() float64 { return float64(p.cache.Stats().DiskItems) })
	reg.NewGaugeFunc("midopress_pipeline_memory_budget_bytes", "Memory the pipeline may hold in buffers and pictures at once.", func() float64 { return float64(p.cfg.MemoryBudget) })
	reg.NewGaugeFunc("midopress_pipeline_memory_reserved_bytes", "Memory the pipeline holds now.", func() float64 { return float64(p.memUsed.Load()) })
	reg.NewGaugeFunc("midopress_cpu_slots_busy", "Slots of the CPU limiter in use.", func() float64 { return float64(len(p.sem)) })
	reg.NewGaugeFunc("midopress_cpu_slots", "Slots of the CPU limiter.", func() float64 { return float64(cap(p.sem)) })
	reg.NewGaugeFunc("midopress_background_queue", "Image jobs waiting for the background workers.", func() float64 { return float64(len(p.bg.queue)) })
}

func (p *Pipeline) observeEncode(f img.Format, _ int, d time.Duration) {
	if in := p.inst.Load(); in != nil {
		in.encode.With(f.String()).Observe(d.Seconds())
	}
}

func (p *Pipeline) recordBackground(result string) {
	if in := p.inst.Load(); in != nil {
		in.bgJobs.With(result).Inc()
	}
}

func contentKind(h http.Header) string { return kindName(httpx.MediaType(h)) }

func (p *Pipeline) record(resp *http.Response, out Outcome, d time.Duration) {
	in := p.inst.Load()
	if in == nil {
		return
	}
	kind := contentKind(resp.Header)
	in.outcomes.With(out.Reason.String()).Inc()
	in.bytesIn.With(kind).Add(uint64(max(out.BytesIn, 0)))
	in.bytesOut.With(kind).Add(uint64(max(out.BytesOut, 0)))
	reason := out.Reason.String()
	in.reasonResponses.With(reason, kind).Inc()
	in.reasonBytes.With(reason, kind).Add(uint64(max(out.BytesIn, 0)))
	if out.IgnoredNoTransform {
		in.noTransform.With(kind).Inc()
	}
	if out.Cause != "" {
		in.causeResponses.With(reason, out.Cause).Inc()
		in.causeBytes.With(reason, out.Cause).Add(uint64(max(out.BytesIn, 0)))
	}
	in.seconds.With(kind).Observe(d.Seconds())
	if out.Reason == Transformed && kind == "image" && out.BytesIn > out.BytesOut {
		in.imageSaved.With(httpx.MediaType(resp.Header)).Add(uint64(out.BytesIn - out.BytesOut))
	}
}
