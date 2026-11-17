package testutil

import (
	"io/fs"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func SandboxSupported() bool {
	return runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}

func RequireSandbox(t testing.TB) {
	t.Helper()
	if !SandboxSupported() {
		t.Skip("codec transformations require Linux seccomp on amd64 or arm64")
	}
}

func AssertFileMode(t testing.TB, want, got fs.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		assert.Equal(t, want&0200, got&0200, "Windows read-only attribute")
		return
	}
	assert.Equal(t, want, got)
}
