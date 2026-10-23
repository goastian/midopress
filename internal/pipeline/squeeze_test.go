package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
)

// jsLike makes a script of about n bytes with the repetition of real code: a
// vocabulary, nested calls, numbers and strings.
func jsLike(n int) []byte {
	rng := rand.New(rand.NewPCG(11, 12))
	words := strings.Fields("function return var let const if else for while this window document element value index length push apply call bind prototype constructor Object Array String Number Promise resolve reject then catch async await new typeof null undefined true false class extends super import export default switch case break continue throw try finally")
	var b strings.Builder
	for b.Len() < n {
		w := func() string { return words[rng.IntN(len(words))] }
		fmt.Fprintf(&b, "%s %s_%d(%s,%s){%s(%s.%s[%d],\"%s-%d\");return %s?%s:%d}\n", w(), w(), rng.IntN(500), w(), w(), w(), w(), w(), rng.IntN(64), w(), rng.IntN(9999), w(), w(), rng.IntN(100000))
	}
	return []byte(b.String())
}

func brotliAt(t *testing.T, level int, data []byte) []byte {
	t.Helper()
	c := codec.NewText(codec.Levels{Brotli: level, Gzip: 6, Zstd: 3})
	var out bytes.Buffer
	require.NoError(t, c.Compress(codec.Brotli, &out, data))
	return out.Bytes()
}

const static = "public, max-age=31536000, immutable"

func TestOriginsThatAlreadySendBrotliAreSqueezed(t *testing.T) {
	js := jsLike(120 << 10)
	origin := brotliAt(t, 5, js)
	slow := brotliAt(t, 11, js)
	require.Less(t, len(slow), len(origin), "the test needs a gap to find")

	serve := func(p *Pipeline, hdr ...string) (*writerRec, Outcome) {
		rec, out := run(p, newReq("Accept-Encoding", "gzip, br"), newResp("text/plain", origin,
			append([]respOpt{withHeader("Content-Encoding", "br"), withHeader("Cache-Control", static)}, headers(hdr)...)...))
		return &writerRec{rec.Body.Bytes(), rec.Header().Get("Content-Encoding")}, out
	}

	t.Run("saver recompresses it at the slow level, and the bytes are the same script", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver })
		got, out := serve(p)
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "br", got.enc)
		assert.Less(t, len(got.body), len(origin))
		assert.InDelta(t, len(slow), len(got.body), float64(len(slow))*0.02)
		assert.Equal(t, js, decode(t, codec.Brotli, got.body))

		again, out2 := serve(p)
		require.Equal(t, Transformed, out2.Reason)
		assert.Equal(t, got.body, again.body, "kept by content, not made again")
	})

	t.Run("lossless leaves it as it came", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileLossless })
		got, out := serve(p)
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
		assert.Equal(t, origin, got.body)
	})

	t.Run("what the origin says is not cacheable is not worth a slow level", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver })
		rec, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", origin,
			withHeader("Content-Encoding", "br"), withHeader("Cache-Control", "private, no-store")))
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
		assert.Equal(t, origin, rec.Body.Bytes())
	})

	t.Run("it can be switched off with recompress", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver; c.Text.Recompress = false })
		got, out := serve(p)
		assert.Equal(t, PassAlreadyCompressed, out.Reason)
		assert.Equal(t, origin, got.body)
	})
}

func TestBigBodiesAreSqueezedInTheBackground(t *testing.T) {
	// a profile that waits 50 ms for the slow level: what takes longer, which at
	// 0.2 MB/s is anything over 10 KB, is made in the background
	profiles := img.Profiles()
	impatient := *profiles[img.ProfileExtreme]
	impatient.Name = "impatient"
	impatient.Text.StaticWaitMS = 50
	profiles[impatient.Name] = &impatient

	js := jsLike(80 << 10)
	origin := brotliAt(t, 5, js)
	require.Greater(t, len(js), staticInlineMax(11, 50))

	p := newPipeline(func(c *Config) { c.Profiles = profiles; c.Profile = impatient.Name })
	t0 := time.Now()
	rec, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", origin,
		withHeader("Content-Encoding", "br"), withHeader("Cache-Control", static)))
	first := time.Since(t0)
	assert.Equal(t, PassAlreadyCompressed, out.Reason, "the first request does not wait for the slow level")
	assert.Equal(t, origin, rec.Body.Bytes())
	assert.Less(t, first, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.True(t, p.WaitBackground(ctx), "the background job finishes")

	rec, out = run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", origin,
		withHeader("Content-Encoding", "br"), withHeader("Cache-Control", static)))
	require.Equal(t, Transformed, out.Reason, "the next one finds it")
	assert.Less(t, rec.Body.Len(), len(origin))
	assert.Equal(t, js, decode(t, codec.Brotli, rec.Body.Bytes()))
}

func TestStaticLevelInlineLimits(t *testing.T) {
	assert.Greater(t, staticInlineMax(9, 300), staticInlineMax(10, 300))
	assert.Greater(t, staticInlineMax(10, 300), staticInlineMax(11, 300))
	assert.Greater(t, staticInlineMax(6, 300), 1<<20)
	assert.Equal(t, 2*staticInlineMax(11, 500), staticInlineMax(11, 1000), "a longer wait takes a bigger body")
	assert.Zero(t, staticInlineMax(11, 0), "no wait, everything in the background")
}

func TestEveryProfileSqueezesWhatItCanAffordToWaitFor(t *testing.T) {
	origin := brotliAt(t, 5, jsLike(60<<10))
	for _, name := range []string{img.ProfileBalanced, img.ProfileSaver, img.ProfileExtreme} {
		p := newPipeline(func(c *Config) { c.Profile = name })
		rec, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", origin,
			withHeader("Content-Encoding", "br"), withHeader("Cache-Control", static)))
		require.Equal(t, Transformed, out.Reason, name)
		assert.Less(t, rec.Body.Len(), len(origin), name)
	}
	// the one that keeps everything as it came does not
	p := newPipeline(func(c *Config) { c.Profile = img.ProfileLossless })
	_, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", origin,
		withHeader("Content-Encoding", "br"), withHeader("Cache-Control", static)))
	assert.Equal(t, PassAlreadyCompressed, out.Reason)
}

type writerRec struct {
	body []byte
	enc  string
}

func headers(kv []string) []respOpt {
	var out []respOpt
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, withHeader(kv[i], kv[i+1]))
	}
	return out
}
