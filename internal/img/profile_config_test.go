package img

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func TestBuildProfilesWithoutOverrides(t *testing.T) {
	ps, err := BuildProfiles(nil)
	require.NoError(t, err)
	assert.Len(t, ps, 4)
	assert.Equal(t, 0.002, ps[ProfileBalanced].TargetDSSIM)
}

func TestBuildProfilesOverridesAndDefinesNew(t *testing.T) {
	ps, err := BuildProfiles(map[string]ProfileOverride{
		"balanced": {TargetDSSIM: ptr(0.0015), Formats: []string{"webp"}, Text: &TextOverride{Brotli: ptr(8)}},
		"mobile":   {Base: "saver", ResizeToViewport: ptr(false), AnimatedToStill: ptr(true)},
		"night":    {Base: "mobile", TargetDSSIM: ptr(0.02)},
	})
	require.NoError(t, err)

	b := ps[ProfileBalanced]
	assert.Equal(t, 0.0015, b.TargetDSSIM)
	assert.Equal(t, []Format{WebP}, b.Formats)
	assert.Equal(t, 8, b.Text.Brotli)
	assert.Equal(t, 6, b.Text.Gzip, "untouched fields keep the built-in value")
	assert.Equal(t, 0.002, Profiles()[ProfileBalanced].TargetDSSIM, "the built-in table is not shared")

	m := ps["mobile"]
	assert.Equal(t, "mobile", m.Name)
	assert.Equal(t, ps[ProfileSaver].TargetDSSIM, m.TargetDSSIM)
	assert.False(t, m.ResizeToViewport)
	assert.True(t, m.AnimatedToStill)
	assert.True(t, ps[ProfileSaver].ResizeToViewport)

	n := ps["night"]
	assert.Equal(t, 0.02, n.TargetDSSIM)
	assert.True(t, n.AnimatedToStill, "inherits from mobile, which was resolved first")
}

func TestBuildProfilesReportsEveryProblem(t *testing.T) {
	_, err := BuildProfiles(map[string]ProfileOverride{
		"balanced": {TargetDSSIM: ptr(5.0), Formats: []string{"bmp"}, Attempts: ptr(0), Text: &TextOverride{Brotli: ptr(12)}},
		"x":        {Base: "nope"},
		"a":        {Base: "b"},
		"b":        {Base: "a"},
	})
	require.Error(t, err)
	for _, want := range []string{"target_dssim", "bmp", "attempts", "text.brotli", `unknown base "nope"`, "cycle"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLosslessStaysLossless(t *testing.T) {
	ps, err := BuildProfiles(map[string]ProfileOverride{"lossless": {TargetDSSIM: ptr(0.5)}})
	require.NoError(t, err)
	assert.True(t, ps[ProfileLossless].Lossless)
	assert.Equal(t, 0.0, ps[ProfileLossless].TargetDSSIM)
}

func TestParseMetric(t *testing.T) {
	for in, want := range map[string]Metric{"": MetricDSSIM, "dssim": MetricDSSIM, "ssim": MetricSSIM, "ssimulacra2": MetricSSIMULACRA2} {
		m, err := ParseMetric(in)
		require.NoError(t, err)
		assert.Equal(t, want, m)
	}
	_, err := ParseMetric("psnr")
	assert.Error(t, err)
}
