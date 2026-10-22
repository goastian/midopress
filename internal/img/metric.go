package img

import (
	"fmt"
	"image"
	"math"

	"github.com/goastian/midopress/internal/quality"
)

// Metric is the measure that decides when a lossy result is close enough to its
// source.
type Metric string

const (
	// MetricDSSIM is Kornel Lesinski's dssim, a multi-scale SSIM in Lab space
	// where 0 is identical and 0.001 can not be told apart by eye.
	MetricDSSIM Metric = "dssim"
	// MetricSSIM is a single scale structural similarity, 1 is identical.
	MetricSSIM Metric = "ssim"
	// MetricSSIMULACRA2 is Cloudinary's metric, 100 is identical and 90 is
	// visually lossless. It costs about four times what dssim does.
	MetricSSIMULACRA2 Metric = "ssimulacra2"
)

// ParseMetric validates a metric name; the empty string means DSSIM.
func ParseMetric(s string) (Metric, error) {
	switch Metric(s) {
	case "", MetricDSSIM:
		return MetricDSSIM, nil
	case MetricSSIM, MetricSSIMULACRA2:
		return Metric(s), nil
	}
	return "", fmt.Errorf("img: unknown metric %q, use dssim, ssim or ssimulacra2", s)
}

// scorer measures candidates against one source. Internally every metric is
// turned into a score where higher is better and the relation with the encoder
// quality is close to a straight line, so that the same search works for all:
// minus the natural logarithm of the distance for dssim, the plain value for
// the others.
type scorer struct {
	metric Metric
	eval   func(image.Image) (float64, error)
	target float64 // score a result must reach
	margin float64 // a result this far above the target is good enough
	slope  float64 // typical change of the score per quality point
}

const dssimFloor = 1e-5

// natural converts a score to the units of the metric: the dssim distance, the
// ssim similarity or the ssimulacra2 score.
func (s *scorer) natural(score float64) float64 {
	if s.metric == MetricDSSIM {
		return math.Exp(-score)
	}
	return score
}

// newScorer prepares the measurement of pic for a profile. Small images, which
// are usually icons and logos where every pixel is seen, get a stricter target.
func newScorer(m Metric, pic image.Image, p *Profile) (*scorer, error) {
	pixels := pic.Bounds().Dx() * pic.Bounds().Dy()
	icon := pixels > 0 && pixels <= iconPixels
	switch m {
	case MetricSSIM:
		ref, err := quality.NewReference(pic)
		if err != nil {
			return nil, err
		}
		target := p.TargetSSIM
		if icon {
			target = 1 - (1-target)/2
		}
		return &scorer{metric: m, eval: ref.Similarity, target: target, margin: 0.004, slope: 0.0016}, nil
	case MetricSSIMULACRA2:
		ref, err := quality.NewSSIMULACRA2Reference(pic)
		if err != nil {
			return nil, err
		}
		target := p.TargetSSIMULACRA2
		if icon {
			target += 5
		}
		return &scorer{metric: m, eval: ref.Score, target: target, margin: 0.8, slope: 0.4}, nil
	}
	ref, err := quality.NewDSSIMReference(pic)
	if err != nil {
		return nil, err
	}
	target := p.TargetDSSIM
	if icon {
		target /= 2
	}
	return &scorer{
		metric: MetricDSSIM,
		eval: func(c image.Image) (float64, error) {
			d, err := ref.Distance(c)
			return -math.Log(math.Max(d, dssimFloor)), err
		},
		target: -math.Log(target),
		margin: 0.12,
		slope:  0.075,
	}, nil
}

// baseline is the score at which balancedQuality is calibrated: the balanced
// target of the metric. The first guess of the search moves away from it in
// proportion to how far the profile's target is.
func (s *scorer) baseline() float64 {
	switch s.metric {
	case MetricSSIM:
		return 0.975
	case MetricSSIMULACRA2:
		return 85
	}
	return -math.Log(0.002)
}

// iconPixels is the area, 128 by 128, up to which an image counts as an icon.
const iconPixels = 128 * 128
