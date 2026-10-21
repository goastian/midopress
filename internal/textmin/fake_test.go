package textmin

import (
	"runtime"
	"sync"
	"testing"
)

// fakeT records failures instead of failing the test, so that the semantic
// checkers can be shown to detect damage.
type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Helper() {}

func (f *fakeT) Name() string { return "fake" }

func (f *fakeT) Errorf(string, ...any) { f.failed = true }

func (f *fakeT) FailNow() {
	f.failed = true
	runtime.Goexit()
}

func (f *fakeT) Logf(string, ...any) {}

// checkFails reports whether check marks the fake test as failed.
func checkFails(check func(t testing.TB)) bool {
	ft := &fakeT{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		check(ft)
	}()
	wg.Wait()
	return ft.failed
}
