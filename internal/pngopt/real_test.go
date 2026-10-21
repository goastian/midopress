package pngopt

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// compareReal optimizes every image at every level, verifies losslessness
// and logs sizes next to Go's BestCompression and optipng -o2. Sizes are
// those of the re-encoded file, before the "never larger than the input"
// fallback of Optimize. The second line per image repeats the experiment with
// the decoded pixels re-encoded by image/png, an unoptimized input.
func compareReal(t *testing.T, imgs map[string][]byte, bestFor, optipngFor int) {
	for i, name := range sortedKeys(imgs) {
		in := imgs[name]
		goIn := nrgbaToPNG(t, decodeStd(t, in))
		for v, data := range [][]byte{in, goIn} {
			label := []string{"asis", "goenc"}[v]
			var sizes [3]int
			var durs [3]time.Duration
			for lvl := Fast; lvl <= Best; lvl++ {
				if lvl == Best && (i >= bestFor || v == 1) {
					continue
				}
				start := time.Now()
				out, err := optimize(data, lvl)
				durs[lvl] = time.Since(start)
				require.NoError(t, err, name)
				requireSamePixels(t, in, out)
				if lvl == Default && v == 0 {
					requireSameFFmpeg(t, in, out)
				}
				sizes[lvl] = len(out)
			}
			opti := "-"
			if i < optipngFor {
				if n, ok := optipngSize(t, data, "-o2"); ok {
					opti = fmt.Sprint(n)
				}
			}
			t.Logf("%-18s %-5s in=%-8d fast=%-8d(%v) default=%-8d(%v) best=%-8d(%v) optipng-o2=%s",
				name, label, len(data), sizes[Fast], durs[Fast].Round(time.Millisecond),
				sizes[Default], durs[Default].Round(time.Millisecond), sizes[Best], durs[Best].Round(time.Millisecond), opti)
			if v == 0 {
				// Default is a heuristic and can lose by under one
				// percent on some large photos; Best must always win.
				got := sizes[Best]
				if got == 0 {
					got = sizes[Default]
				}
				require.LessOrEqual(t, got, len(goIn)+len(goIn)/100, "must be close to image/png BestCompression: "+name)
				if sizes[Best] > 0 {
					require.Less(t, sizes[Best], len(goIn), "Best must beat image/png BestCompression: "+name)
				}
			}
		}
	}
}

func TestOptimizeCorpus(t *testing.T) {
	compareReal(t, corpusPNGs(t), 100, 100)
}

func TestOptimizeKodak(t *testing.T) {
	n, o := 1, 0
	switch {
	case os.Getenv("PNGOPT_FULL") != "":
		n, o = 24, 24
	case testing.Short():
		n, o = 2, 0
	}
	imgs := kodakPNGs(t)
	subset := map[string][]byte{}
	for i, k := range sortedKeys(imgs) {
		if i < n {
			subset[k] = imgs[k]
		}
	}
	compareReal(t, subset, o, o)
}
