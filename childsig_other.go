//go:build !linux

package zip

import "os/exec"

// tieToHost is a no-op where the kernel offers no parent-death signal. A host
// that is killed outright will leave its plugin children running; Shutdown
// still stops them on every ordinary exit path.
func tieToHost(cmd *exec.Cmd) {}
