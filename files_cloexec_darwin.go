//go:build darwin

package zip

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// recvFlags carries no MSG_CMSG_CLOEXEC because Darwin has none. The flag is
// set immediately after instead, which leaves the fork window Linux closes —
// stated here rather than hidden, because it is a property of the platform and
// not something this package can fix.
const recvFlags = 0

// setCloexec marks a received descriptor close-on-exec.
//
// Through x/sys/unix rather than syscall: FcntlInt is not in Darwin's syscall
// package, so the file this replaces did not compile on a Mac at all — main has
// been unbuildable there since the descriptor-passing change landed, while the
// last tag builds. unix is already a dependency, so this adds nothing.
func setCloexec(fd int) {
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
}

// runtimeKeepFiles holds the files alive until the syscall has copied them.
func runtimeKeepFiles(files []*os.File) { runtime.KeepAlive(files) }
