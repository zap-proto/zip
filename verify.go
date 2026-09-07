package zip

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A plugin host is the one place a service becomes an arbitrary-code-execution
// vector: whatever binary it starts verifies blocks, signs, or serves, with the
// host's authority. So the binary a host runs has to be the binary it decided
// to run, and that is two separate claims — WHICH program (identity) and WHICH
// BUILD of it (integrity) — that must not be collapsed into one value.
//
// Identity is stable across versions, because the thing referring to it does
// not get to change: a chain's vmID is written into a transaction that is
// already signed and already in the chain, so an identity derived from the
// build would stop resolving on the next rebuild.
//
// Integrity is per build, and it is what Sum states. Verifying it is worth
// almost nothing unless three things hold, which is what this file is for:
//
//  1. Every source is verified, not just the one that arrives over a network.
//     A binary read off disk is not more trustworthy than a downloaded one —
//     it is the SAME bytes with the download already done by somebody else.
//  2. The bytes verified are the bytes executed. Hashing a path and then
//     executing that path leaves a window in which the file can change, and
//     the window is trivially winnable because starting a plugin is an
//     observable event. Holding a descriptor is NOT enough on its own: a
//     write to the same inode is seen through an already-open descriptor, so
//     only a rename is defended against and the cheaper attack is not. The
//     binary is therefore COPIED into a directory only the host can write,
//     and it is that copy which is hashed and executed.
//  3. A mismatch refuses. A host that logs and carries on has performed the
//     check and kept the risk.
//
// What this file cannot decide is where a trustworthy Sum comes from. A digest
// in the same config an attacker just edited proves nothing; the anchor has to
// be something they do not control. For a public permissionless chain the
// network already agrees on things by construction, so the digest belongs in
// consensus — every validator then verifies the same statement without
// trusting an operator, a registry or a URL.

// binary is a private, verified copy of a plugin executable, held open. It is
// executed THROUGH this descriptor, so no path is resolved a second time.
type binary struct {
	f    *os.File
	name string // for argv[0] and messages
}

// openVerified copies src into priv — a directory only the host can write —
// and, when want is non-empty, refuses unless the COPY's SHA-256 matches.
//
// The copy is the point. Verifying the original in place and then running it
// leaves whoever can write that file free to change it afterwards, and holding
// a descriptor does not close that: a write to the SAME INODE is visible
// through an already-open descriptor, so only a rename would be defended
// against and the cheaper attack would not be. Copying first moves the bytes
// somewhere the attacker has no write access, so "what was checked" and "what
// runs" are one object for the whole of its life.
func openVerified(src, priv, name, want string) (*binary, error) {
	staged := filepath.Join(priv, name+".bin")
	if err := stage(src, staged, name); err != nil {
		return nil, err
	}

	// Reopened READ-ONLY, and this is the descriptor that is both hashed and
	// executed. It has to be read-only: Linux refuses to execute a file that
	// anyone holds open for writing, so keeping the staging handle would trade
	// one failure for another.
	f, err := os.Open(staged)
	if err != nil {
		return nil, err
	}
	if want != "" {
		sum := sha256.New()
		if _, err := io.Copy(sum, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("plugin %s: read for verification: %w", name, err)
		}
		if got := hex.EncodeToString(sum.Sum(nil)); got != want {
			f.Close()
			return nil, fmt.Errorf("plugin %s: sha256 %s does not match Sum %s — refusing to run it", name, got, want)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return &binary{f: f, name: name}, nil
}

// stage copies src to dst, which lives in a directory only the host can write.
func stage(src, dst, name string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return fmt.Errorf("plugin %s: stage binary: %w", name, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("plugin %s: stage binary: %w", name, err)
	}
	return out.Close()
}

func (b *binary) Close() error {
	if b == nil || b.f == nil {
		return nil
	}
	return b.f.Close()
}
