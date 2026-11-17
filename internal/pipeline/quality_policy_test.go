package pipeline

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/quality"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sizing or fixed-quality request must still meet the profile's full-size metric.
func assertImagePolicy(t *testing.T, p *Pipeline, original []byte, rec *httptest.ResponseRecorder, out Outcome) {
	t.Helper()
	if out.Reason != Transformed {
		assert.Contains(t, []Reason{PassBudget, PassNotSmaller, PassType}, out.Reason, out.Cause)
		assert.Equal(t, original, rec.Body.Bytes())
		return
	}
	be := img.NewWASM()
	a, err := img.DecodeDisplay(context.Background(), be, img.Sniff(original), original)
	require.NoError(t, err)
	b, err := img.DecodeDisplay(context.Background(), be, img.Sniff(rec.Body.Bytes()), rec.Body.Bytes())
	require.NoError(t, err)
	if a.Bounds().Size() != b.Bounds().Size() {
		b = img.Resize(b, a.Bounds().Dx(), a.Bounds().Dy())
	}
	prof := p.live.Load().profiles[out.Profile]
	require.NotNil(t, prof)
	opts := quality.Options{MaxPixels: a.Bounds().Dx() * a.Bounds().Dy()}
	if p.engine.Metric == img.MetricDSSIM {
		ref, err := quality.NewDSSIMReference(a, opts)
		require.NoError(t, err)
		d, err := ref.Distance(b)
		require.NoError(t, err)
		assert.LessOrEqual(t, d, prof.TargetDSSIM)
	} else {
		ref, err := quality.NewReference(a, opts)
		require.NoError(t, err)
		s, err := ref.Similarity(b)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, s, prof.TargetSSIM)
	}
}
