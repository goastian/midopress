//go:build linux && (amd64 || arm64)

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSandboxHelper(t *testing.T) {
	action := os.Getenv("CODEC_SANDBOX_TEST")
	if action == "" {
		return
	}
	runtime.GOMAXPROCS(2)
	secret, err := os.Open(os.Args[0])
	if err != nil {
		os.Exit(41)
	}
	secretFD := int(secret.Fd())
	var ready atomic.Int32
	start, results := make(chan struct{}), make(chan bool, 2)
	for range 2 {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready.Add(1)
			<-start
			_, err := unix.Open("/codec-secret", unix.O_RDONLY, 0)
			results <- errors.Is(err, unix.EPERM)
		}()
	}
	for ready.Load() != 2 {
		runtime.Gosched()
	}
	l := Limits{}
	if action == "cpu" {
		l.CPUSeconds = 1
	}
	status, err := Enter(l)
	if err != nil {
		os.Exit(40)
	}
	if action == "cpu" {
		var n atomic.Uint64
		for {
			n.Add(1)
		}
	}
	checks := map[string]bool{}
	var secretByte [1]byte
	_, readErr := unix.Read(secretFD, secretByte[:])
	checks["inherited_file_closed"] = errors.Is(readErr, unix.EBADF)
	close(start)
	checks["existing_threads"] = <-results && <-results
	fd, e := unix.Open("/codec-secret", unix.O_RDONLY, 0)
	checks["read_file"] = errors.Is(e, unix.EPERM)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	fd, e = unix.Open("/codec-created", unix.O_WRONLY|unix.O_CREAT, 0600)
	checks["write_file"] = errors.Is(e, unix.EPERM)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	fd, e = unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	checks["network"] = errors.Is(e, unix.EPERM)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	e = unix.Exec("/bin/true", []string{"true"}, nil)
	checks["exec"] = errors.Is(e, unix.EPERM)
	_, _, e2 := unix.RawSyscall(unix.SYS_IO_URING_SETUP, 0, 0, 0)
	checks["io_uring"] = e2 == unix.EPERM
	e = unix.Kill(os.Getppid(), 0)
	checks["signal_other_process"] = errors.Is(e, unix.EPERM)
	b, e := unix.Mmap(-1, 0, 3<<30, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	checks["memory_limit"] = errors.Is(e, unix.ENOMEM)
	if e == nil {
		_ = unix.Munmap(b)
	}
	newThread := make(chan bool)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		newThread <- errors.Is(err, unix.EPERM)
	}()
	checks["future_threads"] = <-newThread
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Status Status
		Checks map[string]bool
	}{status, checks})
	os.Exit(0)
}

func TestPermissionsApplyToExistingAndFutureThreads(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSandboxHelper$")
	cmd.Env = []string{"CODEC_SANDBOX_TEST=permissions", "GOMAXPROCS=2"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox helper: %v: %s", err, out)
	}
	var result struct {
		Status Status
		Checks map[string]bool
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("helper output: %v: %s", err, out)
	}
	if !result.Status.Enforced || len(result.Checks) != 10 {
		t.Fatalf("missing enforced checks: %#v", result)
	}
	for name, passed := range result.Checks {
		if !passed {
			t.Errorf("restriction failed: %s", name)
		}
	}
}

func TestCPULimitKillsDisposableWorker(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSandboxHelper$")
	cmd.Env = []string{"CODEC_SANDBOX_TEST=cpu", "GOMAXPROCS=2"}
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || ctx.Err() != nil {
		t.Fatalf("CPU limit did not stop helper: %v", err)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("expected RLIMIT_CPU SIGKILL, got %v", exitErr.Sys())
	}
}
