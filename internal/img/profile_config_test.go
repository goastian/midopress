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

func TestTheNewFieldsOfAProfile(t *testing.T) {
	defaults := Profiles()
	assert.Equal(t, 0, defaults[ProfileLossless].AVIFEffort)
	assert.Equal(t, 4, defaults[ProfileBalanced].AVIFEffort)
	assert.Equal(t, 5, defaults[ProfileSaver].AVIFEffort)
	assert.Equal(t, 5, defaults[ProfileExtreme].AVIFEffort)
	for name, want := range map[string]string{ProfileLossless: "", ProfileBalanced: "", ProfileSaver: NoTransformImages, ProfileExtreme: NoTransformImages} {
		assert.Equal(t, want, defaults[name].NoTransform, name)
	}
	for name, want := range map[string]bool{ProfileLossless: false, ProfileBalanced: false, ProfileSaver: true, ProfileExtreme: true} {
		assert.Equal(t, want, defaults[name].MinifyJS, name)
	}
	// every profile can wait for its slow brotli level, longer the more it saves
	assert.Less(t, defaults[ProfileBalanced].Text.StaticWaitMS, defaults[ProfileSaver].Text.StaticWaitMS)
	assert.Less(t, defaults[ProfileSaver].Text.StaticWaitMS, defaults[ProfileExtreme].Text.StaticWaitMS)

	ps, err := BuildProfiles(map[string]ProfileOverride{
		"saver": {AVIFEffort: ptr(7), NoTransform: ptr(NoTransformAll), MinifyJS: ptr(false), Text: &TextOverride{StaticWait: ptr(50)}},
	})
	require.NoError(t, err)
	s := ps[ProfileSaver]
	assert.Equal(t, 7, s.AVIFEffort)
	assert.Equal(t, NoTransformAll, s.NoTransform)
	assert.False(t, s.MinifyJS)
	assert.Equal(t, 50, s.Text.StaticWaitMS)

	for name, o := range map[string]ProfileOverride{
		"avif_effort high": {AVIFEffort: ptr(11)},
		"avif_effort low":  {AVIFEffort: ptr(-1)},
		"no_transform":     {NoTransform: ptr("sometimes")},
		"static_wait":      {Text: &TextOverride{StaticWait: ptr(-5)}},
	} {
		_, err := BuildProfiles(map[string]ProfileOverride{"balanced": o})
		assert.Error(t, err, name)
	}
}

type fakeNative struct {
	Backend
	native bool
}

func (f fakeNative) Native(Format) bool { return f.native }

func TestAVIFEffortFollowsTheProfileWhereItIsAffordable(t *testing.T) {
	saver := Profiles()[ProfileSaver]
	none := &Profile{Name: "none"}
	native := NewEngine(fakeNative{NewWASM(), true})
	wasm := NewEngine(fakeNative{NewWASM(), false})

	assert.Equal(t, 5, native.effort(AVIF, 1<<20, saver), "the native library affords what the profile asks")
	assert.Equal(t, 2, wasm.effort(AVIF, 1<<20, saver), "WebAssembly never goes above its own default")
	assert.Equal(t, 3, native.effort(AVIF, 1<<20, none), "no preference: the default of the engine")
	assert.Equal(t, 2, wasm.effort(AVIF, 1<<20, none))
	low := &Profile{Name: "low", AVIFEffort: 1}
	assert.Equal(t, 1, wasm.effort(AVIF, 1<<20, low), "a lower one is honoured everywhere")

	for _, e := range []*Engine{native, wasm} {
		assert.Equal(t, 7, e.effort(WebP, 1<<20, saver), "WebP and JPEG XL keep the defaults measured for them")
		assert.Equal(t, 3, e.effort(JXL, 1<<20, saver))
	}
}
