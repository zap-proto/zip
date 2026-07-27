package zip

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// A plugin is a service that builds as its own binary and is composed in at
// run time rather than at link time. The host links zip and a transport, never
// the plugin's dependency graph, so the host's build does not grow when a
// plugin does, plugins build in parallel, and one changing rebuilds only
// itself.
//
// Single-artifact deployment still holds, via go:embed — the host embeds the
// binaries it shipped with and remains one file:
//
//	//go:embed bin/billing
//	var billingBin []byte
//
//	app.Add(zip.Load("/v1/billing", zip.Plugin{Name: "billing", Bin: billingBin}))
//
// The same call reaches an already-running instance by setting Addr instead,
// so one binary covers embedded-subprocess and separately-deployed without a
// second code path.
//
// A plugin is an ordinary zip app — no SDK, no schema. It serves routes and
// the host mounts them:
//
//	func main() {
//	    app := zip.New(zip.Config{AppName: "billing"})
//	    app.Get("/v1/billing/invoices", listInvoices)
//	    app.Listen(zip.Addr(":9653"))
//	}
//
// # Reloading
//
// [App.Reload] swaps a running plugin for a new build without dropping a
// request. This is the reason a plugin is a process and not a linked-in
// object: Go can map a shared object in but never unmap one, so an in-process
// plugin's code, globals, and goroutines are unreclaimable for the life of the
// host. A child process releases every byte when it exits.
//
// Reload holds three invariants that make repeated reloads flat in memory:
//
//   - Routes register ONCE, at Load, and resolve their target per request.
//     Re-registering on reload would grow the route table without bound.
//   - The new process must be listening before any request moves to it; the
//     old one keeps serving until then, and a failed start changes nothing.
//   - The old process is killed, reaped exactly once, its pooled connections
//     closed, and its directory removed — so nothing survives the swap.

// AddrEnv is the variable a host sets to tell a plugin where to listen. A
// plugin reads it through [Addr].
const AddrEnv = "ZIP_ADDR"

// Addr returns the address this process was asked to serve on, or fallback
// when it was started directly rather than by a host. This is the whole plugin
// side of the contract.
func Addr(fallback string) string {
	if a := os.Getenv(AddrEnv); a != "" {
		return a
	}
	return fallback
}

// Plugin is a service that ships as its own binary. Exactly one of Addr, Bin,
// or Path says where to find it:
//
//	Addr — already running there; nothing is started, and Reload does not apply
//	Bin  — the binary itself, normally go:embed'd
//	Path — the binary on disk
type Plugin struct {
	Name string   // identifies it in log lines and names its socket
	Addr string   // already running here — start nothing, just mount
	Bin  []byte   // the binary, normally go:embed'd
	Path string   // ...or where it lives on disk
	Args []string // passed after argv[0]
	Env  []string // added to the child's environment

	// Start bounds how long to wait for the plugin to listen. Zero means 10s.
	// A plugin that has not bound by then is a startup failure, not a slow
	// one — nothing is mounted onto a process that never came up.
	Start time.Duration

	// Drain is how long a replaced process keeps serving after a Reload, so
	// requests already in flight on it finish. Zero means 5s.
	Drain time.Duration
}

// plugin is one Load'ed service across restarts. cur is read on every request
// through an atomic, and mu serializes lifecycle transitions so two Reloads
// cannot interleave.
type plugin struct {
	name   string
	prefix string
	spec   Plugin

	cur atomic.Pointer[instance]
	mu  sync.Mutex
}

// instance is one running child process and everything that must be released
// when it stops.
type instance struct {
	cmd    *exec.Cmd
	dir    string
	sock   string
	client Client
	exited chan error // cmd.Wait's single result
}

// target is the hot path: what the mounted route should talk to right now.
func (p *plugin) target() (Client, string) {
	in := p.cur.Load()
	if in == nil {
		return nil, ""
	}
	return in.client, in.sock
}

// Load returns the [Service] that makes p serve prefix. When p.Addr is set it
// is mounted directly. Otherwise the binary is started as a child process
// listening on its own unix socket in a private 0700 directory — no port to
// allocate, and filesystem permissions are the ACL.
//
// It returns a Service rather than taking an *App so a plugin and a linked-in
// service have the SAME type, and composing either is the same line.
//
// The child is stopped and its directory removed on Shutdown, in LIFO order
// with every other hook, so a host that exits cleanly leaves nothing behind.
func Load(prefix string, p Plugin) Service {
	return func(a *App) error { return a.load(prefix, p) }
}

func (a *App) load(prefix string, spec Plugin) error {
	if spec.Name == "" {
		return fmt.Errorf("zip: Load(%s) needs a Plugin.Name", prefix)
	}
	if spec.Addr != "" {
		return a.Mount(prefix, spec.Addr)
	}
	if len(spec.Bin) == 0 && spec.Path == "" {
		return fmt.Errorf("zip: Load(%s): plugin %q has no Addr, Bin, or Path", prefix, spec.Name)
	}

	p := &plugin{name: spec.Name, prefix: prefix, spec: spec}
	in, err := start(spec)
	if err != nil {
		return fmt.Errorf("zip: Load(%s): %w", spec.Name, err)
	}
	p.cur.Store(in)

	a.plugMu.Lock()
	if a.plugins == nil {
		a.plugins = map[string]*plugin{}
	}
	if _, dup := a.plugins[spec.Name]; dup {
		a.plugMu.Unlock()
		stop(in, 0)
		return fmt.Errorf("zip: Load(%s): plugin %q already loaded", prefix, spec.Name)
	}
	a.plugins[spec.Name] = p
	a.plugMu.Unlock()

	a.logger.Info("zip loaded plugin", "name", spec.Name, "prefix", prefix,
		"pid", in.cmd.Process.Pid, "addr", in.sock)

	// Registered once, for the life of the app. Reload swaps what target()
	// returns; it never touches the router.
	a.mountVia(prefix, p.target)

	a.OnShutdown(func(context.Context) error {
		if cur := p.cur.Swap(nil); cur != nil {
			stop(cur, 0)
		}
		return nil
	})
	return nil
}

// Reload replaces the running plugin named name with a new build, without
// dropping a request. bin is the new binary; nil reuses what the plugin was
// loaded with, which is how you restart a crashed or wedged one.
//
// The new process is started and proven to be listening before any request
// moves to it. If it fails to come up, the old one is still serving and the
// error is returned — a bad build cannot take the route down. Once the swap
// happens the old process keeps serving for Plugin.Drain so in-flight requests
// finish, then is killed, reaped, its connections closed and its directory
// removed.
func (a *App) Reload(name string, bin []byte) error {
	a.plugMu.Lock()
	p := a.plugins[name]
	a.plugMu.Unlock()
	if p == nil {
		return fmt.Errorf("zip: Reload: no plugin named %q", name)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	spec := p.spec
	if len(bin) > 0 {
		spec.Bin = bin
		spec.Path = "" // an explicit new binary wins over the old path
	}
	next, err := start(spec)
	if err != nil {
		return fmt.Errorf("zip: Reload(%s): %w", name, err)
	}

	prev := p.cur.Swap(next) // every request from here uses next
	p.spec = spec
	a.logger.Info("zip reloaded plugin", "name", name,
		"pid", next.cmd.Process.Pid, "addr", next.sock)

	if prev != nil {
		drain := spec.Drain
		if drain <= 0 {
			drain = 5 * time.Second
		}
		go stop(prev, drain)
	}
	return nil
}

// Unload stops the plugin named name. Its routes stay registered and answer
// 503 until a Reload brings it back — the route table is never mutated, which
// is what keeps repeated load/unload cycles flat.
func (a *App) Unload(name string) error {
	a.plugMu.Lock()
	p := a.plugins[name]
	a.plugMu.Unlock()
	if p == nil {
		return fmt.Errorf("zip: Unload: no plugin named %q", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prev := p.cur.Swap(nil); prev != nil {
		go stop(prev, 0)
	}
	return nil
}

// start extracts (if embedded), launches, and waits for the child to listen.
// Every failure path releases what it already took.
func start(spec Plugin) (*instance, error) {
	// One private directory per instance holds the extracted binary and the
	// socket, so a reload's new instance never collides with the old one's
	// socket path and cleanup is a single RemoveAll.
	dir, err := os.MkdirTemp("", "zip-"+spec.Name+"-")
	if err != nil {
		return nil, err
	}
	bin := spec.Path
	if len(spec.Bin) > 0 {
		bin = filepath.Join(dir, spec.Name)
		if err := os.WriteFile(bin, spec.Bin, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("write binary: %w", err)
		}
	}

	sock := filepath.Join(dir, spec.Name+".sock")
	cmd := exec.Command(bin, spec.Args...)
	cmd.Env = append(append(os.Environ(), spec.Env...), AddrEnv+"="+sock)
	// The child's output is the operator's only window into it, so it goes
	// where the host's own does rather than being swallowed.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start: %w", err)
	}

	// Exactly one Wait for this process's lifetime; everything else reads the
	// result off this channel. Two Waits is an error, and a child nobody waits
	// on becomes a zombie.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	in := &instance{cmd: cmd, dir: dir, sock: sock, exited: exited}
	if err := waitListening(sock, spec.Start, exited); err != nil {
		stop(in, 0)
		return nil, err
	}

	_, _, t, err := transportFor(sock) // a path resolves to ZAP over unix
	if err != nil {
		stop(in, 0)
		return nil, err
	}
	in.client = t.Dial(sock)
	return in, nil
}

// idleCloser is the optional half of Client that owns pooled connections.
// Both built-in clients implement it; a custom one need not.
type idleCloser interface{ CloseIdleConnections() }

// stop releases everything an instance holds, after letting it serve for grace.
// Safe to call on a partially-started instance.
func stop(in *instance, grace time.Duration) {
	if in == nil {
		return
	}
	if grace > 0 {
		select {
		case <-in.exited: // already gone; nothing to wait out
		case <-time.After(grace):
		}
	}
	if in.cmd != nil && in.cmd.Process != nil {
		_ = in.cmd.Process.Kill()
		select {
		case <-in.exited:
		case <-time.After(5 * time.Second):
		}
	}
	// Pooled connections to a dead socket are not reusable and would otherwise
	// be held by the transport until GC.
	if ic, ok := in.client.(idleCloser); ok {
		ic.CloseIdleConnections()
	}
	if in.dir != "" {
		_ = os.RemoveAll(in.dir) // takes the binary and the socket with it
	}
}

// waitListening blocks until the socket accepts, the child exits, or the
// deadline passes. Watching the child matters: a plugin that dies immediately
// should say so, not time out.
func waitListening(sock string, limit time.Duration, exited <-chan error) error {
	if limit <= 0 {
		limit = 10 * time.Second
	}
	deadline := time.Now().Add(limit)
	for {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("exited before listening: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("did not listen on %s within %s", sock, limit)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
