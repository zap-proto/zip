//go:build linux

package zip

import (
	"os"
	"os/exec"
)

// exec points cmd at the ALREADY-VERIFIED descriptor rather than at a path.
//
// Linux resolves /proc/self/fd/N in the child between fork and execve, so the
// image that runs is the one whose bytes were hashed — not whatever the path
// names by then. Verifying a path and then executing that path is a check an
// attacker can step around by replacing the file in between, and they get to
// choose when, because starting a plugin is an observable event.
//
// argv[0] stays the plugin's name: the descriptor is how the image is found,
// not what the program should believe it is called.
func (b *binary) exec(args []string) *exec.Cmd {
	cmd := exec.Command("/proc/self/fd/3", args...)
	cmd.Args = append([]string{b.name}, args...)
	cmd.ExtraFiles = []*os.File{b.f} // becomes fd 3 in the child
	return cmd
}
