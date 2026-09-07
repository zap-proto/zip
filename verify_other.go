//go:build !linux

package zip

import "os/exec"

// exec falls back to executing by path where a descriptor cannot be executed.
// The digest is still checked, so a binary that does not match never starts;
// what is missing is only the guarantee that nothing replaced it between the
// check and the start.
func (b *binary) exec(args []string) *exec.Cmd {
	cmd := exec.Command(b.f.Name(), args...)
	cmd.Args = append([]string{b.name}, args...)
	return cmd
}
