//go:build darwin

package zip

import (
	"os"
	"runtime"
	"syscall"
)

// recvFlags carries no MSG_CMSG_CLOEXEC because Darwin has none. The flag is
// set immediately after instead, which leaves the fork window Linux closes —
// stated here rather than hidden, because it is a property of the platform and
// not something this package can fix.
const recvFlags = 0

// setCloexec marks a received descriptor close-on-exec.
func setCloexec(fd int) {
	_, _ = syscall.FcntlInt(uintptr(fd), syscall.F_SETFD, syscall.FD_CLOEXEC)
}

// runtimeKeepFiles holds the files alive until the syscall has copied them.
func runtimeKeepFiles(files []*os.File) { runtime.KeepAlive(files) }
