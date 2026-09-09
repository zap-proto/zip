//go:build linux

package zip

import (
	"os"
	"runtime"
	"syscall"
)

// recvFlags asks the kernel to set close-on-exec as it installs each received
// descriptor. Doing it here rather than afterwards closes a real window: a
// concurrent fork between recvmsg and fcntl leaks every descriptor in flight
// into the child, and nothing about the leak is visible at either end.
const recvFlags = syscall.MSG_CMSG_CLOEXEC

// setCloexec is a no-op on Linux — recvFlags already did it atomically.
func setCloexec(int) {}

// runtimeKeepFiles holds the files alive until the syscall has copied them.
func runtimeKeepFiles(files []*os.File) { runtime.KeepAlive(files) }
