package xcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// File format: "MPC1", flags byte, expiry (unix seconds, 0 for never), length
// of the content type, the content type, length of the data, the data, and a
// CRC-32C of everything before it.
const (
	fileMagic    = "MPC1"
	flagKeep     = 1
	headerLen    = 4 + 1 + 8 + 2
	tmpSuffix    = ".tmp"
	writeQueue   = 256
	diskLowWater = 90 // percent of the limit that cleaning goes down to
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

type diskItem struct {
	name    string
	size    int64
	expires int64 // unix seconds, 0 for never
	el      *list.Element
}

type writeJob struct {
	name string
	data []byte
}

type diskTier struct {
	dir   string
	max   int64
	now   func() time.Time
	owner *Cache

	mu    sync.Mutex
	index map[string]*diskItem
	lru   *list.List // front is the most recently used
	bytes int64

	queue   chan writeJob
	pending sync.WaitGroup
	done    chan struct{}
	wg      sync.WaitGroup
	closed  atomic.Bool

	writes, errs atomic.Uint64
}

func nameOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func openDisk(o Options, now func() time.Time, owner *Cache) (*diskTier, error) {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, err
	}
	d := &diskTier{
		dir: o.Dir, max: o.DiskBytes, now: now, owner: owner,
		index: map[string]*diskItem{}, lru: list.New(),
		queue: make(chan writeJob, writeQueue), done: make(chan struct{}),
	}
	if err := d.scan(); err != nil {
		return nil, err
	}
	d.wg.Add(2)
	go d.writer()
	every := o.SweepEvery
	if every <= 0 {
		every = time.Minute
	}
	go d.janitor(every)
	return d, nil
}

func (d *diskTier) path(name string) string { return filepath.Join(d.dir, name[:2], name) }

// scan reads the directory: it indexes the cache files, oldest first so that
// the oldest are evicted first, and removes leftovers of interrupted writes.
func (d *diskTier) scan() error {
	type found struct {
		name  string
		size  int64
		mtime time.Time
	}
	var files []found
	err := filepath.WalkDir(d.dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() {
			return nil
		}
		name := e.Name()
		if strings.HasSuffix(name, tmpSuffix) || len(name) != 64 {
			_ = os.Remove(path)
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		files = append(files, found{name, info.Size(), info.ModTime()})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		it := &diskItem{name: f.name, size: f.size}
		it.expires = d.readExpiry(f.name)
		it.el = d.lru.PushFront(it)
		d.index[f.name] = it
		d.bytes += f.size
	}
	d.sweep()
	return nil
}

func (d *diskTier) readExpiry(name string) int64 {
	f, err := os.Open(d.path(name))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	var h [headerLen]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:4]) != fileMagic {
		return 0
	}
	return int64(binary.BigEndian.Uint64(h[5:13]))
}

func (d *diskTier) get(key string) (*Entry, bool) {
	name := nameOf(key)
	d.mu.Lock()
	it, ok := d.index[name]
	if !ok {
		d.mu.Unlock()
		return nil, false
	}
	if it.expires != 0 && d.now().Unix() >= it.expires {
		d.removeLocked(it)
		d.mu.Unlock()
		_ = os.Remove(d.path(name))
		d.owner.expired.Add(1)
		return nil, false
	}
	d.lru.MoveToFront(it.el)
	d.mu.Unlock()

	raw, err := os.ReadFile(d.path(name))
	if err != nil {
		d.drop(name)
		d.errs.Add(1)
		return nil, false
	}
	e, err := decode(raw)
	if err != nil {
		d.drop(name)
		_ = os.Remove(d.path(name))
		d.errs.Add(1)
		return nil, false
	}
	return e, true
}

func (d *diskTier) drop(name string) {
	d.mu.Lock()
	if it, ok := d.index[name]; ok {
		d.removeLocked(it)
	}
	d.mu.Unlock()
}

func (d *diskTier) removeLocked(it *diskItem) {
	d.lru.Remove(it.el)
	delete(d.index, it.name)
	d.bytes -= it.size
}

func encode(e *Entry) []byte {
	var flags byte
	data := e.Data
	if e.Keep {
		flags |= flagKeep
		data = nil
	}
	var exp uint64
	if !e.Expires.IsZero() {
		exp = uint64(e.Expires.Unix())
	}
	out := make([]byte, 0, headerLen+len(e.ContentType)+4+len(data)+4)
	out = append(out, fileMagic...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint64(out, exp)
	out = binary.BigEndian.AppendUint16(out, uint16(len(e.ContentType)))
	out = append(out, e.ContentType...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.Checksum(out, crcTable))
}

var errCorrupt = errors.New("xcache: corrupt file")

func decode(raw []byte) (*Entry, error) {
	if len(raw) < headerLen+4+4 || string(raw[:4]) != fileMagic {
		return nil, errCorrupt
	}
	body, sum := raw[:len(raw)-4], binary.BigEndian.Uint32(raw[len(raw)-4:])
	if crc32.Checksum(body, crcTable) != sum {
		return nil, errCorrupt
	}
	e := &Entry{Keep: raw[4]&flagKeep != 0}
	if exp := binary.BigEndian.Uint64(raw[5:13]); exp != 0 {
		e.Expires = time.Unix(int64(exp), 0)
	}
	ctLen := int(binary.BigEndian.Uint16(raw[13:15]))
	pos := headerLen
	if pos+ctLen+4 > len(body) {
		return nil, errCorrupt
	}
	e.ContentType = string(body[pos : pos+ctLen])
	pos += ctLen
	n := int(binary.BigEndian.Uint32(body[pos:]))
	pos += 4
	if pos+n != len(body) {
		return nil, errCorrupt
	}
	if n > 0 {
		e.Data = body[pos : pos+n : pos+n]
	}
	return e, nil
}

// put queues a write. When the queue is full the entry is only kept in memory:
// the cache is an optimization and must never slow a request down.
func (d *diskTier) put(key string, e *Entry) {
	if d.closed.Load() {
		return
	}
	data := encode(e)
	if int64(len(data)) > d.max/4 {
		return
	}
	d.pending.Add(1)
	select {
	case d.queue <- writeJob{nameOf(key), data}:
	default:
		d.pending.Done()
		d.errs.Add(1)
	}
}

func (d *diskTier) writer() {
	defer d.wg.Done()
	for {
		select {
		case j := <-d.queue:
			d.write(j)
			d.pending.Done()
		case <-d.done:
			for {
				select {
				case j := <-d.queue:
					d.write(j)
					d.pending.Done()
				default:
					return
				}
			}
		}
	}
}

func (d *diskTier) write(j writeJob) {
	path := d.path(j.name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.errs.Add(1)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "w*"+tmpSuffix)
	if err != nil {
		d.errs.Add(1)
		return
	}
	_, werr := tmp.Write(j.data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
		d.errs.Add(1)
		return
	}
	d.writes.Add(1)
	exp := int64(0)
	if e, err := decode(j.data); err == nil && !e.Expires.IsZero() {
		exp = e.Expires.Unix()
	}
	d.mu.Lock()
	if old, ok := d.index[j.name]; ok {
		d.removeLocked(old)
	}
	it := &diskItem{name: j.name, size: int64(len(j.data)), expires: exp}
	it.el = d.lru.PushFront(it)
	d.index[j.name] = it
	d.bytes += it.size
	over := d.bytes > d.max
	d.mu.Unlock()
	if over {
		d.trim()
	}
}

// trim evicts the least recently used files until the tier is under its low
// water mark.
func (d *diskTier) trim() {
	goal := d.max * diskLowWater / 100
	var victims []string
	d.mu.Lock()
	for d.bytes > goal && d.lru.Len() > 0 {
		it := d.lru.Back().Value.(*diskItem)
		d.removeLocked(it)
		victims = append(victims, it.name)
	}
	d.mu.Unlock()
	for _, n := range victims {
		_ = os.Remove(d.path(n))
	}
	d.owner.evictions.Add(uint64(len(victims)))
}

// sweep removes expired files and trims to the limit.
func (d *diskTier) sweep() {
	now := d.now().Unix()
	var victims []string
	d.mu.Lock()
	for _, it := range d.index {
		if it.expires != 0 && now >= it.expires {
			victims = append(victims, it.name)
		}
	}
	for _, n := range victims {
		d.removeLocked(d.index[n])
	}
	over := d.bytes > d.max
	d.mu.Unlock()
	for _, n := range victims {
		_ = os.Remove(d.path(n))
	}
	d.owner.expired.Add(uint64(len(victims)))
	if over {
		d.trim()
	}
}

func (d *diskTier) janitor(every time.Duration) {
	defer d.wg.Done()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.sweep()
		case <-d.done:
			return
		}
	}
}

func (d *diskTier) flush() { d.pending.Wait() }

func (d *diskTier) close() {
	if d.closed.Swap(true) {
		return
	}
	close(d.done)
	d.wg.Wait()
}

func (d *diskTier) fill(s *Stats) {
	s.DiskWrites = d.writes.Load()
	s.DiskErrors = d.errs.Load()
	d.mu.Lock()
	s.DiskBytes, s.DiskItems = d.bytes, d.lru.Len()
	d.mu.Unlock()
}

// clear removes every file. Writes that were queued are done first so that none
// of them lands after.
func (d *diskTier) clear() {
	d.flush()
	d.mu.Lock()
	names := make([]string, 0, len(d.index))
	for n := range d.index {
		names = append(names, n)
	}
	d.index, d.lru, d.bytes = map[string]*diskItem{}, list.New(), 0
	d.mu.Unlock()
	for _, n := range names {
		_ = os.Remove(d.path(n))
	}
}
