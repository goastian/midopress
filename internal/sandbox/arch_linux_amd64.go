package sandbox

import "golang.org/x/sys/unix"

const auditArch = unix.AUDIT_ARCH_X86_64

func allowedSyscalls() []uintptr {
	return append(commonSyscalls(), unix.SYS_ARCH_PRCTL, unix.SYS_EPOLL_WAIT)
}
