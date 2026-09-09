//go:build linux || darwin

package zip

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// Passing open files, which is the one thing a byte stream cannot express.
//
// A descriptor is not data. Sending its NUMBER means nothing across a process
// boundary — the integer is an index into a table the peer does not share — so
// a socket that carries only bytes cannot hand over a pipe, a console or an
// already-open file. SCM_RIGHTS is the kernel doing the translation: it
// duplicates the descriptor into the receiver's table and reports the number it
// landed on there.
//
// This is why a sandbox supervisor needs it. The gofer's connection, a
// process's stdio and a restored memory file all exist before the call that
// hands them over, and none of them can be reopened by name on the other side.
//
// It rides the same unix connection as everything else, so nothing about
// addressing or dialling changes.

// ErrNoFilePassing reports a connection that cannot carry descriptors. It is a
// distinct error rather than a silent zero because dropping the files while
// delivering the bytes is the worst outcome: the receiver gets a message that
// says a console was attached and no console.
var ErrNoFilePassing = errors.New("zip: file passing needs a unix connection")

// maxFiles bounds one message. SCM_RIGHTS is limited by the receiver's own
// descriptor table and by SCM_MAX_FD (253 on Linux); a request for more is
// refused here, where the number is known, rather than by a sendmsg errno that
// says only EINVAL.
const maxFiles = 253

// SendFiles writes b and hands over files on the same message.
//
// The byte is not optional. A message carrying only control data may be
// discarded before the receiver ever sees it, so at least one real byte must
// travel with the descriptors or the transfer is silently lost — which is why
// this refuses an empty b rather than sending something that usually works.
func SendFiles(c net.Conn, b []byte, files []*os.File) error {
	if len(b) == 0 {
		return errors.New("zip: SendFiles needs at least one byte alongside the files")
	}
	if len(files) > maxFiles {
		return fmt.Errorf("zip: %d files exceeds the %d a message may carry", len(files), maxFiles)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ErrNoFilePassing
	}
	fds := make([]int, len(files))
	for i, f := range files {
		if f == nil {
			return fmt.Errorf("zip: file %d is nil", i)
		}
		fds[i] = int(f.Fd())
	}
	// Hold every *os.File alive across the syscall. Without this the finalizer
	// may close a descriptor after Fd() read it and before the kernel copies
	// it, and the peer receives a number pointing at whatever was opened next.
	defer runtimeKeepFiles(files)

	var rights []byte
	if len(fds) > 0 {
		rights = syscall.UnixRights(fds...)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	if err := raw.Control(func(fd uintptr) {
		_, sendErr = syscall.SendmsgN(int(fd), b, rights, nil, 0)
	}); err != nil {
		return err
	}
	return sendErr
}

// RecvFiles reads one message into b and returns the descriptors that came
// with it, already wrapped as *os.File so they are closed by the usual means.
//
// Every received descriptor is closed if the call fails afterwards. A leaked
// descriptor from a rejected message is invisible until the process runs out,
// and then it is attributed to whatever asked for the next one.
func RecvFiles(c net.Conn, b []byte) (int, []*os.File, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, nil, ErrNoFilePassing
	}
	// Space for the control message. CmsgSpace accounts for the header and the
	// alignment the kernel requires; sizing this by hand is how a truncated
	// SCM_RIGHTS silently drops the last descriptor.
	oob := make([]byte, syscall.CmsgSpace(maxFiles*4))
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, nil, err
	}
	var n, oobn int
	var recvErr error
	if err := raw.Read(func(fd uintptr) bool {
		n, oobn, _, _, recvErr = syscall.Recvmsg(int(fd), b, oob, recvFlags)
		// Report not-ready so the runtime waits and calls again, rather than
		// turning a would-block into a short read the caller reads as EOF.
		if recvErr == syscall.EAGAIN || recvErr == syscall.EWOULDBLOCK {
			return false
		}
		return true
	}); err != nil {
		return 0, nil, err
	}
	if recvErr != nil {
		return 0, nil, recvErr
	}
	if oobn == 0 {
		return n, nil, nil
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return n, nil, err
	}
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, m := range msgs {
		if m.Header.Level != syscall.SOL_SOCKET || m.Header.Type != syscall.SCM_RIGHTS {
			continue
		}
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			closeAll()
			return n, nil, err
		}
		for _, fd := range fds {
			setCloexec(fd)
			files = append(files, os.NewFile(uintptr(fd), "zip-received-file"))
		}
	}
	return n, files, nil
}
