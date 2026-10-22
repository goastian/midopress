package img

import (
	"bytes"
	stdjpeg "image/jpeg"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/quality"
)

func dssimEngine() *Engine {
	e := NewEngine(NewWASM())
	e.Metric = MetricDSSIM
	return e
}

func TestDSSIMTargetIsMetOnTheDecodedResult(t *testing.T) {
	raw := encodeJPEG(t, photoLike(220, 160, 31), 95)
	src, err := stdjpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	for _, name := range []string{ProfileBalanced, ProfileSaver} {
		p := profile(t, name)
		res := optimizeWith(t, dssimEngine(), Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true, AVIF: true, JXL: true}, Profile: p})
		require.False(t, res.Lossless, name)
		assert.Equal(t, MetricDSSIM, res.Metric)
		dec, err := testEngine.Backend().Decode(res.Format, res.Data)
		require.NoError(t, err)
		d, err := quality.DSSIM(src, dec)
		require.NoError(t, err)
		assert.LessOrEqual(t, d, p.TargetDSSIM, "%s: measured again with the metric, outside the engine", name)
		assert.InDelta(t, res.Measure, d, 0.0005, "%s: the reported distance is the real one", name)
		assert.Less(t, len(res.Data), len(raw), name)
	}
}

func TestStricterProfilesCostMoreBytes(t *testing.T) {
	raw := encodeJPEG(t, photoLike(220, 160, 32), 95)
	e := dssimEngine()
	sizes := map[string]int{}
	for _, name := range []string{ProfileBalanced, ProfileSaver, ProfileExtreme} {
		sizes[name] = len(optimizeWith(t, e, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: profile(t, name)}).Data)
	}
	assert.Greater(t, sizes[ProfileBalanced], sizes[ProfileExtreme])
}

func TestSSIMULACRA2Metric(t *testing.T) {
	raw := encodeJPEG(t, photoLike(220, 160, 33), 95)
	e := NewEngine(NewWASM())
	e.Metric = MetricSSIMULACRA2
	p := profile(t, ProfileBalanced)
	res := optimizeWith(t, e, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: p})
	if res.Lossless {
		t.Skip("nothing smaller than the original reached the target")
	}
	assert.Equal(t, MetricSSIMULACRA2, res.Metric)
	assert.GreaterOrEqual(t, res.Measure, p.TargetSSIMULACRA2)
	src, _ := stdjpeg.Decode(bytes.NewReader(raw))
	dec, err := e.Backend().Decode(res.Format, res.Data)
	require.NoError(t, err)
	s, err := quality.SSIMULACRA2(src, dec)
	require.NoError(t, err)
	assert.InDelta(t, res.Measure, s, 0.5)
}

func TestIconsGetAStricterTarget(t *testing.T) {
	p := profile(t, ProfileBalanced)
	big, err := newScorer(MetricDSSIM, photoLike(300, 300, 1), p)
	require.NoError(t, err)
	icon, err := newScorer(MetricDSSIM, photoLike(64, 64, 1), p)
	require.NoError(t, err)
	assert.InDelta(t, big.natural(big.target)/2, icon.natural(icon.target), 1e-9, "half the distance")

	ssimBig, _ := newScorer(MetricSSIM, photoLike(300, 300, 1), p)
	ssimIcon, _ := newScorer(MetricSSIM, photoLike(64, 64, 1), p)
	assert.Greater(t, ssimIcon.target, ssimBig.target)
	s2Big, _ := newScorer(MetricSSIMULACRA2, photoLike(300, 300, 1), p)
	s2Icon, _ := newScorer(MetricSSIMULACRA2, photoLike(64, 64, 1), p)
	assert.Greater(t, s2Icon.target, s2Big.target)
}

func TestAVIFIsNotTriedOnTinyFiles(t *testing.T) {
	e := dssimEngine()
	p := profile(t, ProfileBalanced)
	small := Input{Raw: make([]byte, 5_000), Profile: p, Caps: Caps{WebP: true, AVIF: true, JXL: true}}
	assert.NotContains(t, e.formatsFor(small, false), AVIF)
	assert.Contains(t, e.formatsFor(small, false), JXL)
	big := Input{Raw: make([]byte, 50_000), Profile: p, Caps: small.Caps}
	assert.Contains(t, e.formatsFor(big, false), AVIF)
	assert.NotContains(t, e.formatsFor(big, true), JPEG, "no JPEG when there is transparency")
}

func TestAFormatThatNeverReachesTheTargetIsSkippedForAWhile(t *testing.T) {
	var u unreachable
	p := profile(t, ProfileBalanced)
	for range unreachableAfter {
		assert.False(t, u.skip(WebP, p, MetricDSSIM))
		u.record(WebP, p, MetricDSSIM, false, true)
	}
	skipped := 0
	for range unreachableRetry * 2 {
		if u.skip(WebP, p, MetricDSSIM) {
			skipped++
		}
	}
	assert.Greater(t, skipped, unreachableRetry, "skipped most of the time")
	assert.Less(t, skipped, unreachableRetry*2, "but tried again now and then")
	assert.False(t, u.skip(AVIF, p, MetricDSSIM), "other formats are not affected")
	assert.False(t, u.skip(WebP, profile(t, ProfileSaver), MetricDSSIM), "nor other profiles")

	u.record(WebP, p, MetricDSSIM, true, false)
	assert.False(t, u.skip(WebP, p, MetricDSSIM), "reaching the target clears it")
	u.record(WebP, p, MetricDSSIM, false, false)
	assert.False(t, u.skip(WebP, p, MetricDSSIM), "failing for lack of time says nothing")
}

func TestCostModelLearns(t *testing.T) {
	e := dssimEngine()
	before := e.attemptCost(AVIF, 1_000_000)
	for range 20 {
		e.costs.observe(AVIF, 1_000_000, 40*before)
	}
	assert.Greater(t, e.attemptCost(AVIF, 1_000_000), 20*before, "what was measured replaces the guess")
	e.costs.observe(AVIF, 1_000, before)
	assert.Greater(t, e.attemptCost(AVIF, 1_000_000), 20*before, "tiny images say nothing about the cost")
}

func optimizeWith(t *testing.T, e *Engine, in Input) *Result {
	t.Helper()
	old := testEngine
	testEngine = e
	defer func() { testEngine = old }()
	return optimize(t, in)
}

// With libjxl at hand, reversible recompression is one more candidate in every
// profile, and when it wins the original JPEG can be rebuilt from the result.
func TestReversibleJPEGXLCompetesInEveryProfile(t *testing.T) {
	if !JPEGRecompressAvailable() {
		t.Skip("libjxl is not installed")
	}
	raw := encodeJPEG(t, photoLike(220, 160, 34), 97)
	e := dssimEngine()
	for _, name := range []string{ProfileLossless, ProfileBalanced, ProfileExtreme} {
		res := optimizeWith(t, e, Input{Raw: raw, Format: JPEG, Caps: Caps{JXL: true}, Profile: profile(t, name)})
		assert.Equal(t, JXL, res.Format, name)
		assert.Less(t, len(res.Data), len(raw), name)
		if res.Lossless {
			back, err := ReconstructJPEG(res.Data)
			require.NoError(t, err)
			assert.Equal(t, raw, back, "%s: the original, bit for bit", name)
		}
	}
	lossless := optimizeWith(t, e, Input{Raw: raw, Format: JPEG, Caps: Caps{JXL: true}, Profile: profile(t, ProfileLossless)})
	assert.True(t, lossless.Lossless, "the lossless profile has nothing else to offer")
}
