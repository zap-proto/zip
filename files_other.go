//go:build !linux && !darwin

package zip

import (
	"errors"
	"net"
	"os"
)

// ErrNoFilePassing reports a host with no SCM_RIGHTS. It is the same error the
// unix build returns for a non-unix connection, so a caller has one condition
// to handle rather than one per platform.
var ErrNoFilePassing = errors.New("zip: file passing needs a unix connection")

// SendFiles refuses rather than dropping the files and delivering the bytes.
func SendFiles(net.Conn, []byte, []*os.File) error { return ErrNoFilePassing }

// RecvFiles refuses for the same reason.
func RecvFiles(net.Conn, []byte) (int, []*os.File, error) { return 0, nil, ErrNoFilePassing }
