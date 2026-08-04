// Package sock is the fleet's socket-path scheme, in one place.
//
// A service NAME maps to exactly one path — <Dir()>/<name>.sock — and both
// halves of every connection use this mapping, which is what keeps them from
// drifting: a service serves at that path and a caller dials it by name, with no
// registry and no discovery service in between.
//
// It lives down here, below zip, because zip is not the only caller any more.
// zip.RuntimeDir and zip.SocketPath are the public spelling of it, and the
// telemetry exporter needs the same answer without importing zip — which it
// cannot do, since zip imports the exporter. A second copy of the rule would be
// a fleet where a program serves at one path and its telemetry looks for another.
package sock

import (
	"os"
	"path/filepath"
)

// DirEnv names the directory holding one socket per service. It is the one knob
// in the scheme.
const DirEnv = "ZIP_RUNTIME_DIR"

// Dir is the directory that holds the fleet's sockets, resolved in one order:
//
//	$ZIP_RUNTIME_DIR        set explicitly — always wins
//	$XDG_RUNTIME_DIR/zip    a developer's per-user runtime dir (/run/user/1000)
//	/run/zip                the system default
//
// The middle case is what makes a dev box work without configuration: /run/zip
// is not writable by a normal user and $XDG_RUNTIME_DIR is.
//
// The environment is read on every call rather than cached, so a process that
// arranges its own runtime directory after start — and a test that does — sees
// the directory it actually has.
func Dir() string {
	if d := os.Getenv(DirEnv); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "zip")
	}
	return "/run/zip"
}

// Path is the socket a service of this name is reached at.
func Path(name string) string { return In(Dir(), name) }

// In is the shape of every socket path, stated once: the plugin loader names a
// child's private socket with it too, so "a service's socket is
// <dir>/<name>.sock" has one definition.
func In(dir, name string) string { return filepath.Join(dir, name+".sock") }
