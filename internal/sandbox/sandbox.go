package sandbox

import "errors"

var ErrUnavailable = errors.New("codec sandbox unavailable")

type Limits struct {
	MemoryBytes uint64 `json:"memory_bytes"`
	CPUSeconds  uint64 `json:"cpu_seconds"`
}

type Status struct {
	Enforced          bool   `json:"enforced"`
	Policy            string `json:"policy"`
	Threads           string `json:"threads"`
	Filesystem        string `json:"filesystem"`
	Network           string `json:"network"`
	ProcessCreation   string `json:"process_creation"`
	MemoryBytes       uint64 `json:"memory_headroom_bytes"`
	AddressSpaceBytes uint64 `json:"address_space_limit_bytes"`
	DataBytes         uint64 `json:"data_limit_bytes"`
	CPUSeconds        uint64 `json:"cpu_seconds"`
}

func normalize(l Limits) Limits {
	if l.MemoryBytes == 0 {
		l.MemoryBytes = 1536 << 20
	}
	if l.CPUSeconds == 0 {
		l.CPUSeconds = 30
	}
	return l
}
