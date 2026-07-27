//go:build linux

package zip

import (
	"os/exec"
	"syscall"
)

// tieToHost makes the kernel signal this child when its host dies.
//
// Graceful Shutdown already stops every child, but a host that is SIGKILLed,
// OOM-killed, or crashes never runs its hooks — and a plugin is a separate
// process, so it would keep running, keep holding its socket, and keep serving
// requests for a host that no longer exists. Pdeathsig closes that: the child
// dies with the parent whether or not the parent got to say so.
//
// This is the one place a subprocess plugin is genuinely more dangerous than a
// linked-in one, so it is worth the platform-specific code.
func tieToHost(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
