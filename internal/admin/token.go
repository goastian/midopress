package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LoadOrCreateToken reads the admin token from path, or makes a new one of 256
// random bits, writes it with permissions 600 and says so. A token that is
// there but too short to be safe is an error, not something to replace: it may
// have been put there on purpose.
func LoadOrCreateToken(path string) (token string, created bool, err error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		token = strings.TrimSpace(string(data))
		if len(token) < 32 {
			return "", false, fmt.Errorf("the admin token in %s is shorter than 32 characters; remove the file to make a new one", path)
		}
		return token, false, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", false, err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, err
	}
	token = hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	// O_EXCL: two processes starting together must not each write a token
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateToken(path)
	}
	if err != nil {
		return "", false, err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", false, err
	}
	return token, true, f.Close()
}

// guard checks the token of every request and slows down guessing: after too
// many wrong tokens from one address it answers 429 for a while, whatever token
// comes next.
type guard struct {
	hash [sha256.Size]byte
	now  func() time.Time

	mu       sync.Mutex
	failures map[string]*failure
}

type failure struct {
	count int
	until time.Time // end of the window, and of the block once count is over the limit
}

const (
	maxFailures   = 8
	failureWindow = time.Minute
)

func newGuard(token string) *guard {
	return &guard{hash: sha256.Sum256([]byte(token)), now: time.Now, failures: map[string]*failure{}}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// check returns 0 when the request carries the token, else the status to send.
func (g *guard) check(r *http.Request) (status int, retry time.Duration) {
	ip := clientIP(r)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if f, ok := g.failures[ip]; ok {
		if now.After(f.until) {
			delete(g.failures, ip)
		} else if f.count >= maxFailures {
			return http.StatusTooManyRequests, f.until.Sub(now)
		}
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	h := sha256.Sum256([]byte(got))
	if got != "" && subtle.ConstantTimeCompare(h[:], g.hash[:]) == 1 {
		delete(g.failures, ip)
		return 0, 0
	}
	if got != "" { // a request with no token at all is not a guess
		f := g.failures[ip]
		if f == nil {
			f = &failure{}
			g.failures[ip] = f
		}
		f.count++
		f.until = now.Add(failureWindow)
		if len(g.failures) > 4096 {
			for k, v := range g.failures {
				if now.After(v.until) {
					delete(g.failures, k)
				}
			}
		}
	}
	return http.StatusUnauthorized, 0
}
