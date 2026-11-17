//go:build linux && (amd64 || arm64)

package sandbox

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const policy = "linux-seccomp-tsync-v1"

// Enter must run in a disposable worker after trusted codec initialization and
// before any resource parsing. The filter covers existing and future threads.
func Prepare(l Limits) (Status, error) {
	l = normalize(l)
	status := Status{Policy: policy, Threads: "all-existing-and-future", Filesystem: "denied", Network: "denied", ProcessCreation: "denied", MemoryBytes: l.MemoryBytes, CPUSeconds: l.CPUSeconds}
	if l.MemoryBytes < 32<<20 || l.MemoryBytes > 2<<30 || l.CPUSeconds < 1 || l.CPUSeconds > 120 {
		return status, fmt.Errorf("%w: invalid limits", ErrUnavailable)
	}
	stat, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return status, fmt.Errorf("%w: memory accounting", ErrUnavailable)
	}
	var vmSize, vmData uint64
	for _, line := range strings.Split(string(stat), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "VmSize:":
			vmSize = n * 1024
		case "VmData:":
			vmData = n * 1024
		}
	}
	if vmSize == 0 || vmData == 0 || vmSize > 8<<30 {
		return status, fmt.Errorf("%w: invalid memory baseline", ErrUnavailable)
	}
	for resource, value := range map[int]uint64{
		unix.RLIMIT_AS: vmSize + l.MemoryBytes, unix.RLIMIT_DATA: vmData + l.MemoryBytes,
		unix.RLIMIT_CPU: l.CPUSeconds, unix.RLIMIT_CORE: 0, unix.RLIMIT_NOFILE: 32,
		unix.RLIMIT_FSIZE: 0,
	} {
		var old unix.Rlimit
		if err := unix.Getrlimit(resource, &old); err != nil {
			return status, fmt.Errorf("%w: read resource limits", ErrUnavailable)
		}
		value = min(value, old.Max)
		if err := unix.Setrlimit(resource, &unix.Rlimit{Cur: value, Max: value}); err != nil {
			return status, fmt.Errorf("%w: resource limits", ErrUnavailable)
		}
		switch resource {
		case unix.RLIMIT_AS:
			status.AddressSpaceBytes = value
		case unix.RLIMIT_DATA:
			status.DataBytes = value
		case unix.RLIMIT_CPU:
			status.CPUSeconds = value
		}
	}
	return status, nil
}

func Enter(l Limits) (Status, error) {
	status, err := Prepare(l)
	if err != nil {
		return status, err
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return status, fmt.Errorf("%w: descriptor inventory", ErrUnavailable)
	}
	for _, e := range entries {
		fd, _ := strconv.Atoi(e.Name())
		if fd <= 2 {
			continue
		}
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && target != "anon_inode:[eventpoll]" && target != "anon_inode:[eventfd]" {
			_ = unix.Close(fd)
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return status, fmt.Errorf("%w: dump restriction", ErrUnavailable)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return status, fmt.Errorf("%w: no_new_privs", ErrUnavailable)
	}
	filters := syscallFilter(uint32(os.Getpid()))
	prog := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	r, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	runtime.KeepAlive(filters)
	if errno != 0 || r != 0 {
		return status, fmt.Errorf("%w: seccomp thread synchronization", ErrUnavailable)
	}
	status.Enforced = true
	return status, nil
}

func syscallFilter(pid uint32) []unix.SockFilter {
	stmt := func(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }
	eq := func(k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: k, Jt: jt, Jf: jf}
	}
	deny := stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM))
	allow := stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW)
	f := []unix.SockFilter{stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, 4), eq(auditArch, 1, 0), stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_KILL_PROCESS), stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, 0)}
	if runtime.GOARCH == "amd64" {
		f = append(f, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: 0x40000000, Jt: 0, Jf: 1}, deny)
	}
	for _, nr := range allowedSyscalls() {
		f = append(f, eq(uint32(nr), 0, 1), allow)
	}
	f = append(f, eq(unix.SYS_CLONE3, 0, 1), stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS)))
	f = append(f, eq(unix.SYS_CLONE, 0, 5), stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, 16), stmt(unix.BPF_ALU|unix.BPF_AND|unix.BPF_K, unix.CLONE_VM|unix.CLONE_THREAD), eq(unix.CLONE_VM|unix.CLONE_THREAD, 0, 1), allow, deny)
	// The previous branch returns for clone and reloads nr for all other calls.
	f = append(f, stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, 0), eq(unix.SYS_TGKILL, 0, 3), stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, 16), eq(pid, 0, 1), allow, deny)
	return f
}

func commonSyscalls() []uintptr {
	return []uintptr{unix.SYS_READ, unix.SYS_WRITE, unix.SYS_READV, unix.SYS_WRITEV, unix.SYS_CLOSE,
		unix.SYS_FSTAT, unix.SYS_FCNTL, unix.SYS_LSEEK, unix.SYS_MMAP, unix.SYS_MPROTECT,
		unix.SYS_MUNMAP, unix.SYS_MADVISE, unix.SYS_MREMAP, unix.SYS_BRK, unix.SYS_FUTEX,
		unix.SYS_FUTEX_WAITV, unix.SYS_SCHED_YIELD, unix.SYS_SCHED_GETAFFINITY,
		unix.SYS_CLOCK_GETTIME, unix.SYS_CLOCK_NANOSLEEP, unix.SYS_NANOSLEEP,
		unix.SYS_GETRANDOM, unix.SYS_GETPID, unix.SYS_GETTID, unix.SYS_GETUID, unix.SYS_GETEUID,
		unix.SYS_GETGID, unix.SYS_GETEGID, unix.SYS_SET_TID_ADDRESS, unix.SYS_SET_ROBUST_LIST,
		unix.SYS_RSEQ, unix.SYS_RT_SIGACTION, unix.SYS_RT_SIGPROCMASK, unix.SYS_RT_SIGRETURN,
		unix.SYS_RT_SIGPENDING, unix.SYS_SIGALTSTACK, unix.SYS_EVENTFD2, unix.SYS_EPOLL_CREATE1,
		unix.SYS_EPOLL_CTL, unix.SYS_EPOLL_PWAIT, unix.SYS_GETRUSAGE, unix.SYS_UNAME,
		unix.SYS_EXIT, unix.SYS_EXIT_GROUP, unix.SYS_RESTART_SYSCALL}
}
