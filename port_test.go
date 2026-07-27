package zip_test

import (
	"net"
	"testing"
)

// freeAddr returns a loopback address nothing is listening on.
//
// These tests used fixed ports, which is fine in isolation and wrong on a
// shared machine: an unrelated process holding 19653 makes TestListen_ZAP fail
// with a 404 rather than a connection error, because the port answers — it is
// just answering for someone else. That reads as a routing bug and is not one.
//
// Binding :0 and releasing has a race window, but it is bounded by the next
// line of the test rather than by whatever else lives on the box.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}
