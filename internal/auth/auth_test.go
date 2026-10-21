package auth

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

type fakeClock struct{ t atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.t.Store(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.t.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration) { c.t.Add(int64(d)) }

func newBasic(t *testing.T, mutate func(*Options)) *Authenticator {
	t.Helper()
	opts := Options{Mode: ModeBasic, User: "alice", Pass: "s3cret"}
	if mutate != nil {
		mutate(&opts)
	}
	a, err := New(opts)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	return a
}

func newTokenSetup(t *testing.T, mutate func(*Options)) (*Authenticator, *Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := OpenStore(path, true)
	require.NoError(t, err)
	opts := Options{Mode: ModeToken, DevicesFile: path, ReloadInterval: -1}
	if mutate != nil {
		mutate(&opts)
	}
	a, err := New(opts)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	return a, store, path
}

func TestNewValidation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.json")
	tests := []struct {
		name string
		opts Options
		ok   bool
	}{
		{"none", Options{Mode: ModeNone}, true},
		{"basic ok", Options{Mode: ModeBasic, User: "u", Pass: "p"}, true},
		{"basic no user", Options{Mode: ModeBasic, Pass: "p"}, false},
		{"basic no pass", Options{Mode: ModeBasic, User: "u"}, false},
		{"token no file option", Options{Mode: ModeToken}, false},
		{"token missing file", Options{Mode: ModeToken, DevicesFile: missing}, false},
		{"unknown", Options{Mode: "weird"}, false},
		{"empty mode", Options{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(tt.opts)
			if tt.ok {
				require.NoError(t, err)
				a.Close()
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestModeNone(t *testing.T) {
	a, err := New(Options{Mode: ModeNone})
	require.NoError(t, err)
	d := a.Check("1.2.3.4:5", "")
	assert.True(t, d.Allowed)
	assert.Zero(t, d.Status)
	assert.True(t, a.Check("1.2.3.4:5", "garbage").Allowed)
	assert.True(t, a.AllowClient("8.8.8.8:1"))
}

func TestBasicCheck(t *testing.T) {
	a := newBasic(t, func(o *Options) { o.Realm = `My "Realm"` })
	huge := "Basic " + strings.Repeat("A", 100000)
	tests := []struct {
		name   string
		header string
		ok     bool
	}{
		{"valid", basicHeader("alice", "s3cret"), true},
		{"scheme lower", "basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")), true},
		{"scheme upper", "BASIC " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")), true},
		{"password with colon", basicHeader("alice", "s3cret:x"), false},
		{"wrong pass", basicHeader("alice", "nope"), false},
		{"wrong user", basicHeader("bob", "s3cret"), false},
		{"bad base64", "Basic !!!notbase64", false},
		{"no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("alice")), false},
		{"wrong scheme", "Digest abc", false},
		{"bearer in basic mode", "Bearer s3cret", false},
		{"no space", "Basic", false},
		{"huge", huge, false},
		{"empty creds", "Basic ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := a.Check("10.0.0.1:1234", tt.header)
			if tt.ok {
				assert.True(t, d.Allowed)
				assert.Zero(t, d.Status)
				assert.Equal(t, "alice", d.Device)
				return
			}
			assert.False(t, d.Allowed)
			assert.Equal(t, 407, d.Status)
			assert.Equal(t, `Basic realm="My \"Realm\""`, d.Challenge)
		})
	}
}

func TestMissingHeaderNotCounted(t *testing.T) {
	a := newBasic(t, func(o *Options) { o.MaxFailures = 2 })
	for range 10 {
		d := a.Check("10.0.0.1:1", "")
		assert.Equal(t, 407, d.Status)
		assert.Equal(t, `Basic realm="MidoPress"`, d.Challenge)
	}
	assert.True(t, a.Check("10.0.0.1:1", basicHeader("alice", "s3cret")).Allowed)
}

func TestRateLimit(t *testing.T) {
	clock := newFakeClock()
	a := newBasic(t, func(o *Options) {
		o.MaxFailures = 3
		o.FailureWindow = 30 * time.Second
		o.Now = clock.Now
	})
	bad := basicHeader("alice", "wrong")
	good := basicHeader("alice", "s3cret")

	for range 3 {
		assert.Equal(t, 407, a.Check("[::ffff:10.0.0.9]:1", bad).Status)
	}
	d := a.Check("10.0.0.9:9999", good)
	assert.Equal(t, 429, d.Status)
	assert.False(t, d.Allowed)
	assert.Equal(t, 30*time.Second, d.RetryAfter)

	assert.Equal(t, 429, a.Check("10.0.0.9:1", "").Status)
	assert.True(t, a.Check("10.0.0.10:1", good).Allowed)

	clock.Advance(10 * time.Second)
	assert.Equal(t, 20*time.Second, a.Check("10.0.0.9:1", good).RetryAfter)

	clock.Advance(20*time.Second - 500*time.Millisecond)
	assert.Equal(t, time.Second, a.Check("10.0.0.9:1", good).RetryAfter)

	clock.Advance(time.Second)
	assert.True(t, a.Check("10.0.0.9:1", good).Allowed)
	assert.Equal(t, 407, a.Check("10.0.0.9:1", bad).Status)
}

func TestRateLimitSuccessDoesNotReset(t *testing.T) {
	clock := newFakeClock()
	a := newBasic(t, func(o *Options) {
		o.MaxFailures = 2
		o.Now = clock.Now
	})
	bad := basicHeader("alice", "wrong")
	good := basicHeader("alice", "s3cret")
	a.Check("10.0.0.1:1", bad)
	assert.True(t, a.Check("10.0.0.1:1", good).Allowed)
	a.Check("10.0.0.1:1", bad)
	assert.Equal(t, 429, a.Check("10.0.0.1:1", good).Status)
}

func TestRateLimitWindowExpiryResetsCount(t *testing.T) {
	clock := newFakeClock()
	a := newBasic(t, func(o *Options) {
		o.MaxFailures = 2
		o.FailureWindow = time.Minute
		o.Now = clock.Now
	})
	bad := basicHeader("alice", "wrong")
	a.Check("10.0.0.1:1", bad)
	clock.Advance(2 * time.Minute)
	a.Check("10.0.0.1:1", bad)
	assert.Equal(t, 407, a.Check("10.0.0.1:1", bad).Status)
	assert.Equal(t, 429, a.Check("10.0.0.1:1", bad).Status)
}

func TestRateLimitShortRealWindow(t *testing.T) {
	a := newBasic(t, func(o *Options) {
		o.MaxFailures = 1
		o.FailureWindow = 50 * time.Millisecond
	})
	assert.Equal(t, 407, a.Check("10.0.0.1:1", "Basic x").Status)
	assert.Equal(t, 429, a.Check("10.0.0.1:1", "Basic x").Status)
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, 407, a.Check("10.0.0.1:1", "Basic x").Status)
}

func TestRateLimitUnparsableRemoteSharedKey(t *testing.T) {
	a := newBasic(t, func(o *Options) { o.MaxFailures = 2 })
	bad := basicHeader("alice", "wrong")
	a.Check("garbage", bad)
	a.Check("", bad)
	assert.Equal(t, 429, a.Check("other-garbage", bad).Status)
	assert.True(t, a.AllowClient("garbage"))
}

func TestAllowClient(t *testing.T) {
	blocking := newBasic(t, func(o *Options) { o.BlockGlobal = true })
	open := newBasic(t, nil)
	tests := []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:80", true},
		{"[::1]:80", true},
		{"::1", true},
		{"10.1.2.3:1", true},
		{"172.16.0.1:1", true},
		{"172.31.255.255", true},
		{"172.32.0.1:1", false},
		{"192.168.1.1:1", true},
		{"169.254.1.1:1", true},
		{"[fe80::1]:1", true},
		{"[fe80::1%eth0]:1", true},
		{"[fd00::1]:1", true},
		{"[fc00::1]:1", true},
		{"100.64.0.1:1", true},
		{"100.127.255.255", true},
		{"100.128.0.1:1", false},
		{"100.63.255.255:1", false},
		{"[::ffff:192.168.1.5]:1", true},
		{"::ffff:8.8.8.8", false},
		{"8.8.8.8:53", false},
		{"[2001:4860:4860::8888]:53", false},
		{"0.0.0.0:1", false},
		{"[::]:1", false},
		{"224.0.0.1:1", false},
		{"[ff02::1]:1", false},
		{"garbage", false},
		{"", false},
		{"1.2.3.4:notaport", false},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			assert.Equal(t, tt.want, blocking.AllowClient(tt.remote))
			assert.True(t, open.AllowClient(tt.remote))
		})
	}
	assert.Equal(t, 403, blocking.Check("8.8.8.8:1", basicHeader("alice", "s3cret")).Status)
	assert.True(t, blocking.Check("10.0.0.1:1", basicHeader("alice", "s3cret")).Allowed)
}

func TestTokenAuth(t *testing.T) {
	a, store, _ := newTokenSetup(t, nil)
	tok, err := store.Add("phone", "p")
	require.NoError(t, err)
	require.NoError(t, a.Reload())

	tests := []struct {
		name   string
		header string
		ok     bool
	}{
		{"bearer", "Bearer " + tok, true},
		{"bearer lower", "bearer " + tok, true},
		{"bearer wrong", "Bearer " + tok + "x", false},
		{"basic", basicHeader("phone", tok), true},
		{"basic wrong token", basicHeader("phone", "nope"), false},
		{"basic unknown id", basicHeader("ghost", tok), false},
		{"basic other id with valid token", basicHeader("tablet", tok), false},
		{"bad base64", "Basic ***", false},
		{"no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("phone")), false},
		{"wrong scheme", "Token " + tok, false},
		{"huge", "Bearer " + strings.Repeat("a", 100000), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := a.Check("10.0.0.1:1", tt.header)
			if tt.ok {
				assert.True(t, d.Allowed)
				assert.Equal(t, "phone", d.Device)
			} else {
				assert.Equal(t, 407, d.Status)
			}
		})
	}
}

func TestTokenReloadAfterFileReplaced(t *testing.T) {
	a, store, _ := newTokenSetup(t, nil)
	tok, err := store.Add("one", "")
	require.NoError(t, err)
	assert.Equal(t, 407, a.Check("10.0.0.1:1", "Bearer "+tok).Status)
	require.NoError(t, a.Reload())
	assert.True(t, a.Check("10.0.0.1:2", "Bearer "+tok).Allowed)

	require.NoError(t, store.Revoke("one"))
	require.NoError(t, a.Reload())
	assert.Equal(t, 407, a.Check("10.0.0.1:3", "Bearer "+tok).Status)
}

func TestTokenReloadErrorKeepsSnapshot(t *testing.T) {
	a, store, path := newTokenSetup(t, nil)
	tok, err := store.Add("keep", "")
	require.NoError(t, err)
	require.NoError(t, a.Reload())

	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0o600))
	assert.Error(t, a.Reload())
	d := a.Check("10.0.0.1:1", "Bearer "+tok)
	assert.True(t, d.Allowed)
	assert.Equal(t, "keep", d.Device)

	require.NoError(t, os.Remove(path))
	assert.Error(t, a.Reload())
	assert.True(t, a.Check("10.0.0.1:1", "Bearer "+tok).Allowed)
}

func TestTokenPollerReloads(t *testing.T) {
	a, store, _ := newTokenSetup(t, func(o *Options) { o.ReloadInterval = 10 * time.Millisecond })
	tok, err := store.Add("polled", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return a.Check("10.0.0.7:1", "Bearer "+tok).Allowed
	}, 3*time.Second, 10*time.Millisecond)
}

func TestCloseIdempotent(t *testing.T) {
	a, _, _ := newTokenSetup(t, func(o *Options) { o.ReloadInterval = time.Millisecond })
	a.Close()
	a.Close()
	b := newBasic(t, nil)
	b.Close()
	b.Close()
}

func TestConcurrentCheck(t *testing.T) {
	a, store, _ := newTokenSetup(t, func(o *Options) {
		o.ReloadInterval = time.Millisecond
		o.MaxFailures = 1000000
	})
	tok, err := store.Add("c", "")
	require.NoError(t, err)
	require.NoError(t, a.Reload())

	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			remote := fmt.Sprintf("10.0.%d.1:1", g%4)
			for i := range 500 {
				switch i % 4 {
				case 0:
					assert.True(t, a.Check(remote, "Bearer "+tok).Allowed)
				case 1:
					assert.Equal(t, 407, a.Check(remote, "Bearer bad").Status)
				case 2:
					assert.True(t, a.Check(remote, basicHeader("c", tok)).Allowed)
				default:
					a.AllowClient(remote)
				}
				if g == 0 && i%100 == 0 {
					_ = a.Reload()
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkCheckBasic(b *testing.B) {
	a, err := New(Options{Mode: ModeBasic, User: "alice", Pass: "s3cret"})
	require.NoError(b, err)
	b.Cleanup(a.Close)
	header := basicHeader("alice", "s3cret")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !a.Check("10.0.0.1:1234", header).Allowed {
				b.Fatal("denied")
			}
		}
	})
}

func BenchmarkCheckBearer(b *testing.B) {
	path := filepath.Join(b.TempDir(), "devices.json")
	store, err := OpenStore(path, true)
	require.NoError(b, err)
	tok, err := store.Add("phone", "")
	require.NoError(b, err)
	a, err := New(Options{Mode: ModeToken, DevicesFile: path, ReloadInterval: -1})
	require.NoError(b, err)
	b.Cleanup(a.Close)
	header := "Bearer " + tok
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !a.Check("10.0.0.1:1234", header).Allowed {
				b.Fatal("denied")
			}
		}
	})
}
