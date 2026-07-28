//go:build linux

package zip

import (
	"net"
	"syscall"
)

// peerOf reads SO_PEERCRED off a unix connection — the pid, uid and gid the
// KERNEL recorded for the process at the far end when it connected. It is not
// a value the peer sends, so there is nothing for it to forge.
//
// A non-unix conn has no answer: a tcp peer is an address, and an address is
// not an identity. That case returns nil rather than a zero Peer, so "not
// attested" is never mistaken for "attested as root".
func peerOf(c net.Conn) *Peer {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	var cred *syscall.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || sockErr != nil || cred == nil {
		return nil
	}
	return &Peer{PID: int(cred.Pid), UID: int(cred.Uid), GID: int(cred.Gid)}
}
