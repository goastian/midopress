package img

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRuntimeExecutionRoutesMatchTheSelectedCodecs(t *testing.T) {
	be := NewWASM()
	assert.Equal(t, "wasm", ExecutionPath(be, JXL))
	assert.Equal(t, "go", ExecutionPath(be, PNG))
	for _, f := range []Format{JPEG, WebP, AVIF} {
		want := gen2brainExecutionPath(f)
		if be.Native(f) {
			want = "native"
		}
		assert.Equal(t, want, ExecutionPath(be, f), f.String())
	}
	if !be.Native(WebP) {
		assert.Equal(t, "go-wasm2go", ExecutionPath(be, WebP))
	}
	if gen2brainExecutionPath(JPEG) == "go-wasm2go" {
		assert.Contains(t, RuntimeInfo(be)["jpeg"].Reason, "generated Go")
	}
	assert.Equal(t, "wasm", RuntimeInfo(be)["jxl"].Decoder)
}
