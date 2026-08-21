//go:build darwin

package zip

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerOf reads the credential the KERNEL recorded for the process at the far
// end of a unix connection — the same attestation SO_PEERCRED gives on Linux,
// and the same guarantee: it is not a value the peer sends, so there is
// nothing for it to forge.
//
// It takes two socket options because Darwin splits what Linux returns at
// once: LOCAL_PEERCRED carries the user and groups, LOCAL_PEERPID the process.
//
// A non-unix conn has no answer: a tcp peer is an address, and an address is
// not an identity. That case returns nil rather than a zero Peer, so "not
// attested" is never mistaken for "attested as root" — which is also why a
// credential arriving without a group is refused whole rather than reported
// with GID 0. Every field of a non-nil Peer came from the kernel.
func peerOf(c net.Conn) *Peer {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	var cred *unix.Xucred
	var pid int
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		if cred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); sockErr != nil {
			return
		}
		pid, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil || sockErr != nil || cred == nil || cred.Ngroups < 1 {
		return nil
	}
	return &Peer{PID: pid, UID: int(cred.Uid), GID: int(cred.Groups[0])}
}
