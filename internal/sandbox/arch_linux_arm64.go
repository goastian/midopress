package sandbox

import "golang.org/x/sys/unix"

const auditArch = unix.AUDIT_ARCH_AARCH64

func allowedSyscalls() []uintptr { return commonSyscalls() }
