package pipeline

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTTLFor(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	c := TTLConfig{Min: time.Minute, Max: 24 * time.Hour, Default: time.Hour}
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Add(kv[i], kv[i+1])
		}
		return out
	}
	tests := []struct {
		name string
		h    http.Header
		want time.Duration
	}{
		{"nothing said", h(), time.Hour},
		{"max-age", h("Cache-Control", "public, max-age=600"), 10 * time.Minute},
		{"max-age above the maximum", h("Cache-Control", "max-age=31536000"), 24 * time.Hour},
		{"max-age below the minimum", h("Cache-Control", "max-age=5"), time.Minute},
		{"s-maxage wins", h("Cache-Control", "max-age=60, s-maxage=7200"), 2 * time.Hour},
		{"age is subtracted", h("Cache-Control", "max-age=3600", "Age", "1800"), 30 * time.Minute},
		{"immutable", h("Cache-Control", "public, max-age=60, immutable"), 24 * time.Hour},
		{"no-store", h("Cache-Control", "no-store, max-age=3600"), time.Minute},
		{"no-cache", h("Cache-Control", "no-cache"), time.Minute},
		{"private", h("Cache-Control", "private, max-age=3600"), time.Minute},
		{"expires with date", h("Expires", "Wed, 21 Oct 2026 14:00:00 GMT", "Date", "Wed, 21 Oct 2026 12:30:00 GMT"), 90 * time.Minute},
		{"expires without date", h("Expires", "Wed, 21 Oct 2026 12:30:00 GMT"), 30 * time.Minute},
		{"expires in the past", h("Expires", "Wed, 21 Oct 2026 11:00:00 GMT"), time.Minute},
		{"expires zero", h("Expires", "0"), time.Minute},
		{"max-age beats expires", h("Cache-Control", "max-age=300", "Expires", "Thu, 22 Oct 2026 12:00:00 GMT"), 5 * time.Minute},
		{"garbage max-age falls to the default", h("Cache-Control", "max-age=abc"), time.Hour},
		{"negative max-age", h("Cache-Control", "max-age=-5"), time.Hour},
		{"quoted max-age", h("Cache-Control", `max-age="900"`), 15 * time.Minute},
		{"max-age zero", h("Cache-Control", "max-age=0"), time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ttlFor(tt.h, now, c))
		})
	}
}
