//go:build !linux || (!amd64 && !arm64)

package sandbox

func Enter(Limits) (Status, error) {
	return Status{Policy: "unavailable", Filesystem: "not-enforced", Network: "not-enforced", ProcessCreation: "not-enforced"}, ErrUnavailable
}

func Prepare(l Limits) (Status, error) { return Enter(l) }
