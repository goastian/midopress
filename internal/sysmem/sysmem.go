// Package sysmem says how much memory the machine, or the container the process
// runs in, has, so that limits can follow it instead of a number that suits no
// machine.
package sysmem

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Total returns the memory available to the process in bytes: the limit of its
// cgroup when it has one, else the memory of the machine. It returns 0 when it
// can not tell, which callers treat as "use your default".
func Total() int64 {
	machine := meminfo("/proc/meminfo")
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		if lim := cgroupLimit(p); lim > 0 && (machine == 0 || lim < machine) {
			return lim
		}
	}
	return machine
}

func cgroupLimit(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return parseLimit(string(b))
}

// parseLimit reads a cgroup limit: a number of bytes, or "max" for none. The
// value of cgroups v1 for no limit is a number near 2^63, which is not a limit
// either.
func parseLimit(s string) int64 {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0
	}
	return n
}

func meminfo(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			return parseKB(v)
		}
	}
	return 0
}

// parseKB reads "  16384 kB" as bytes.
func parseKB(s string) int64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n << 10
}

// Budget is the memory the image pipeline may hold at once for a machine with
// total bytes: a quarter of it, at least min and at most max. When total is not
// known it is min.
func Budget(total, min, max int64) int64 {
	if total <= 0 {
		return min
	}
	b := total / 4
	switch {
	case b < min:
		return min
	case b > max:
		return max
	}
	return b
}
