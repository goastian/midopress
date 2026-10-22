// Package auth implements proxy authentication, per-IP failure rate limiting
// and client network filtering for MidoPress.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/lru"
)

type Mode string

const (
	ModeNone  Mode = "none"
	ModeBasic Mode = "basic"
	ModeToken Mode = "token"
)

const (
	defaultRealm          = "MidoPress"
	defaultMaxFailures    = 10
	defaultFailureWindow  = time.Minute
	defaultReloadInterval = 5 * time.Second
	failureCacheSize      = 100000

	maxHeaderLen  = 512
	maxDecodedLen = 384
)

type Options struct {
	Mode           Mode
	User, Pass     string
	DevicesFile    string
	Realm          string
	BlockGlobal    bool
	MaxFailures    int
	FailureWindow  time.Duration
	ReloadInterval time.Duration
	Now            func() time.Time
}

type Decision struct {
	Allowed    bool
	Status     int
	Device     string
	Challenge  string
	RetryAfter time.Duration
}

type digest = [sha256.Size]byte

type deviceEntry struct {
	id      string
	hash    digest
	profile string
}

// snapshot is an immutable view of the devices file.
type snapshot struct {
	byID    map[string]*deviceEntry
	byToken map[digest]*deviceEntry
}

type failureEntry struct {
	count   atomic.Int32
	resetAt atomic.Int64
}

type fileStamp struct {
	mod  time.Time
	size int64
}

type Authenticator struct {
	mode        Mode
	user        string
	userHash    digest
	passHash    digest
	dummyHash   digest
	challenge   string
	blockGlobal bool
	maxFailures int32
	window      time.Duration
	now         func() time.Time

	failures *lru.Cache[netip.Addr, *failureEntry]
	devices  atomic.Pointer[snapshot]

	devicesFile string
	reloadMu    sync.Mutex
	stamp       fileStamp

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// New validates opts and builds an Authenticator. In token mode it starts a
// goroutine polling the devices file; call Close to stop it.
func New(opts Options) (*Authenticator, error) {
	a := &Authenticator{
		mode:        opts.Mode,
		blockGlobal: opts.BlockGlobal,
		maxFailures: int32(opts.MaxFailures),
		window:      opts.FailureWindow,
		now:         opts.Now,
		devicesFile: opts.DevicesFile,
		dummyHash:   sha256.Sum256([]byte("midopress-dummy")),
	}
	if a.maxFailures <= 0 {
		a.maxFailures = defaultMaxFailures
	}
	if a.window <= 0 {
		a.window = defaultFailureWindow
	}
	if a.now == nil {
		a.now = time.Now
	}
	realm := opts.Realm
	if realm == "" {
		realm = defaultRealm
	}
	realm = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(realm)
	a.challenge = `Basic realm="` + realm + `"`

	switch opts.Mode {
	case ModeNone:
	case ModeBasic:
		if opts.User == "" || opts.Pass == "" {
			return nil, errors.New("basic auth requires both user and password")
		}
		a.user = opts.User
		a.userHash = sha256.Sum256([]byte(opts.User))
		a.passHash = sha256.Sum256([]byte(opts.Pass))
	case ModeToken:
		if opts.DevicesFile == "" {
			return nil, errors.New("token auth requires a devices file")
		}
		if err := a.Reload(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown auth mode %q", opts.Mode)
	}
	if opts.Mode != ModeNone {
		a.failures = lru.New[netip.Addr, *failureEntry](failureCacheSize, a.window)
	}
	if opts.Mode == ModeToken && opts.ReloadInterval >= 0 {
		interval := opts.ReloadInterval
		if interval == 0 {
			interval = defaultReloadInterval
		}
		a.stop = make(chan struct{})
		a.done = make(chan struct{})
		go a.poll(interval)
	}
	return a, nil
}

// Close stops the background reload goroutine. It is idempotent.
func (a *Authenticator) Close() {
	a.closeOnce.Do(func() {
		if a.stop != nil {
			close(a.stop)
			<-a.done
		}
	})
}

func (a *Authenticator) poll(interval time.Duration) {
	defer close(a.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.reloadIfChanged()
		}
	}
}

func (a *Authenticator) reloadIfChanged() {
	fi, err := os.Stat(a.devicesFile)
	if err != nil {
		return
	}
	a.reloadMu.Lock()
	changed := fi.ModTime() != a.stamp.mod || fi.Size() != a.stamp.size
	a.reloadMu.Unlock()
	if changed {
		_ = a.Reload()
	}
}

// Reload re-reads the devices file and atomically swaps the snapshot. On error
// the previous snapshot is kept.
func (a *Authenticator) Reload() error {
	if a.mode != ModeToken {
		return nil
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	fi, statErr := os.Stat(a.devicesFile)
	if statErr == nil {
		a.stamp = fileStamp{mod: fi.ModTime(), size: fi.Size()}
	}
	devices, err := readDevices(a.devicesFile)
	if err != nil {
		return err
	}
	snap := &snapshot{
		byID:    make(map[string]*deviceEntry, len(devices)),
		byToken: make(map[digest]*deviceEntry, len(devices)),
	}
	for _, d := range devices {
		h, err := decodeDigest(d.TokenSHA256)
		if err != nil {
			return err
		}
		e := &deviceEntry{id: d.ID, hash: h, profile: d.Profile}
		snap.byID[d.ID] = e
		snap.byToken[h] = e
	}
	a.devices.Store(snap)
	return nil
}

// AllowClient reports whether remote may use the proxy given the BlockGlobal setting.
func (a *Authenticator) AllowClient(remote string) bool {
	if !a.blockGlobal {
		return true
	}
	addr, ok := parseRemote(remote)
	if !ok {
		return false
	}
	return isLocalAddr(addr)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isLocalAddr(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	if addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr)
}

func parseRemote(remote string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if addr, err := netip.ParseAddr(remote); err == nil {
		return addr.Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

// Check authenticates a request given the client address and the raw
// Proxy-Authorization header value.
func (a *Authenticator) Check(remote, proxyAuthorization string) Decision {
	if a.blockGlobal && !a.AllowClient(remote) {
		return Decision{Status: 403}
	}
	if a.mode == ModeNone {
		return Decision{Allowed: true}
	}
	key, _ := parseRemote(remote)
	now := a.now()

	if fe, ok := a.failures.Get(key); ok {
		if reset := fe.resetAt.Load(); now.UnixNano() < reset && fe.count.Load() >= a.maxFailures {
			retry := time.Duration(reset - now.UnixNano())
			if retry < time.Second {
				retry = time.Second
			}
			return Decision{Status: 429, RetryAfter: retry}
		}
	}

	if proxyAuthorization == "" {
		return Decision{Status: 407, Challenge: a.challenge}
	}
	if device, ok := a.verify(proxyAuthorization); ok {
		return Decision{Allowed: true, Device: device}
	}
	a.recordFailure(key, now)
	return Decision{Status: 407, Challenge: a.challenge}
}

func (a *Authenticator) recordFailure(key netip.Addr, now time.Time) {
	reset := now.Add(a.window).UnixNano()
	fe, ok := a.failures.Get(key)
	if ok && now.UnixNano() < fe.resetAt.Load() {
		fe.count.Add(1)
		fe.resetAt.Store(reset)
	} else {
		fe = &failureEntry{}
		fe.count.Store(1)
		fe.resetAt.Store(reset)
	}
	a.failures.AddWithTTL(key, fe, a.window)
}

func (a *Authenticator) verify(header string) (string, bool) {
	if len(header) > maxHeaderLen {
		return "", false
	}
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok {
		return "", false
	}
	rest = strings.TrimLeft(rest, " ")
	switch {
	case strings.EqualFold(scheme, "Basic"):
		var buf [maxDecodedLen]byte
		user, pass, ok := decodeBasic(rest, buf[:])
		if !ok {
			return "", false
		}
		if a.mode == ModeBasic {
			return a.verifyBasic(user, pass)
		}
		return a.verifyDeviceBasic(user, pass)
	case strings.EqualFold(scheme, "Bearer") && a.mode == ModeToken:
		var tok [maxHeaderLen]byte
		n := copy(tok[:], rest)
		sum := sha256.Sum256(tok[:n])
		if e, ok := a.devices.Load().byToken[sum]; ok {
			return e.id, true
		}
	}
	return "", false
}

func decodeBasic(encoded string, dst []byte) (user, pass []byte, ok bool) {
	var src [maxHeaderLen]byte
	if len(encoded) > len(src) || base64.StdEncoding.DecodedLen(len(encoded)) > len(dst) {
		return nil, nil, false
	}
	n := copy(src[:], encoded)
	m, err := base64.StdEncoding.Decode(dst, src[:n])
	if err != nil {
		return nil, nil, false
	}
	creds := dst[:m]
	i := indexByte(creds, ':')
	if i < 0 {
		return nil, nil, false
	}
	return creds[:i], creds[i+1:], true
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func (a *Authenticator) verifyBasic(user, pass []byte) (string, bool) {
	uh := sha256.Sum256(user)
	ph := sha256.Sum256(pass)
	u := subtle.ConstantTimeCompare(uh[:], a.userHash[:])
	p := subtle.ConstantTimeCompare(ph[:], a.passHash[:])
	if u&p == 1 {
		return a.user, true
	}
	return "", false
}

// DeviceProfile returns the profile assigned to a device when it was added, or
// the empty string for none or for a device that does not exist.
func (a *Authenticator) DeviceProfile(id string) string {
	if a == nil || a.mode != ModeToken {
		return ""
	}
	if e, ok := a.devices.Load().byID[id]; ok {
		return e.profile
	}
	return ""
}

func (a *Authenticator) verifyDeviceBasic(id, token []byte) (string, bool) {
	th := sha256.Sum256(token)
	e, found := a.devices.Load().byID[string(id)]
	want := &a.dummyHash
	if found {
		want = &e.hash
	}
	eq := subtle.ConstantTimeCompare(th[:], want[:])
	if found && eq == 1 {
		return e.id, true
	}
	return "", false
}
