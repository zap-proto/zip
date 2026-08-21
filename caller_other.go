//go:build !linux && !darwin

package zip

import "net"

// peerOf reports nothing where the OS does not hand us a peer credential.
// nil is the complete answer, not a placeholder: it says "this host cannot
// attest the caller", which is exactly what a caller-identity check must see
// so it fails closed rather than trusting an empty Peer.
func peerOf(net.Conn) *Peer { return nil }
