package zip

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
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
// Or the host carries no plugin at all and installs one from its repository's
// releases, which is what fully decouples the two build cycles — the host
// relinks in well under a second and never compiles a plugin:
//
//	app.Add(zip.Load("/v1/billing", zip.Plugin{
//	    Name: "billing",
//	    URL:  "https://github.com/hanzoai/billing/releases/download/v1.2.3/billing-linux-arm64",
//	    Sum:  "9f2c…",   // required: an unverified download is refused
//	}))
//
// Or it reaches an instance already running elsewhere, by setting Addr. All
// four are the same call and the same type, so one host binary covers
// embedded, installed, on-disk and remote without a second code path — and
// which one a deployment uses is configuration.
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
	URL  string   // ...or a release artifact to fetch (requires Sum)
	Args []string // passed after argv[0]
	Env  []string // added to the child's environment

	// Sum is the hex SHA-256 of the binary at URL, and is REQUIRED with it.
	// Fetching code over a network and executing it is the one place a plugin
	// host becomes an arbitrary-code-execution vector, so an unverified
	// download is refused rather than trusted.
	//
	// It doubles as the cache key: a binary already present under this digest
	// is reused, so a restart costs no download and a rollback to a previously
	// run version is free and offline.
	Sum string

	// Dir is where an embedded binary is extracted and its socket created.
	// Empty means the system temp dir, which on many hosts is a tmpfs — i.e.
	// RAM. A plugin binary is tens to hundreds of megabytes, so extracting one
	// there spends real memory and fails outright when the tmpfs is full. Point
	// this at disk for anything but a small plugin.
	Dir string

	// Start bounds how long to wait for the plugin to listen. Zero means 10s.
	// A plugin that has not bound by then is a startup failure, not a slow
	// one — nothing is mounted onto a process that never came up.
	Start time.Duration

	// Drain is how long a replaced process keeps serving after a Reload, so
	// requests already in flight on it finish. Zero means 5s.
	Drain time.Duration

	// Lazy defers starting the child until the first request actually reaches
	// one of its prefixes. Routes register at Load either way, so the surface
	// is identical — only the process is deferred.
	//
	// This is what makes many plugins affordable. A host composing 69 services
	// eagerly pays 69 processes, 69 resident sets and 69 startup times at boot
	// for a set that is mostly idle; lazily it pays for the ones traffic
	// actually reaches. The cost moves to the first request, which is why it is
	// opt-in: a latency-critical prefix should stay eager.
	Lazy bool

	// Tools is this plugin's MCP catalogue: the JSON array [App.MCPTools]
	// projects, captured at BUILD time and normally go:embed'd beside Bin. Given
	// it, the host serves every plugin's tools on its own /mcp door and forwards
	// a tools/call to the owner.
	//
	// It is a byte slice and not a call because MCPTools is in-process: a host
	// cannot ask a plugin that is not running, and asking would defeat Lazy —
	// tools/list is the method an MCP client calls constantly, so a host that
	// woke every child to answer it would pay 112 processes for a question with
	// a build-time answer. The answer IS build-time: the same typed-op registry
	// that emits the plugin's OpenAPI document emits this.
	//
	// The catalogue can only be INCOMPLETE, never wrong: the child's own registry
	// answers the call, so a name the host no longer serves yields that child's
	// -32602 rather than a mis-dispatch. Two plugins declaring one tool name is
	// refused at Load, for the same reason a duplicate prefix is — a name is
	// dispatch, so a duplicate is unroutable.
	//
	// The forward target is this plugin's own MCP path, which means a plugin that
	// moved it (MCPConfig.Path) or turned it off (MCPConfig.Disabled) must not
	// ship a catalogue: the host would name tools the child does not answer at
	// that path. Leave Tools empty and the plugin is simply not on the host's
	// door.
	Tools []byte

	// MCPPath is where this plugin serves its own MCP door, when it is not zip's
	// default. Set it only alongside a matching MCPConfig.Path in the plugin.
	MCPPath string

	// Open declares this plugin's catalogue INCOMPLETE BY CONSTRUCTION: it also
	// serves tools that exist because of WHO is asking — a tenant's own rows,
	// which no build-time projection can hold. Tools stays what it is (the part
	// that IS build-time); Open is how the rest gets onto the host's door.
	//
	// Given it, the host asks this plugin — and only this plugin — for the
	// caller's own tools on a tools/list that NAMES a caller, and hands it a
	// tools/call no catalogue claimed. An anonymous list still costs a memcpy and
	// starts nothing: a per-caller answer needs a caller, so there is nothing to
	// ask when nobody is asking.
	//
	// At most ONE plugin may be open, for the reason two may not own one tool
	// name: an unclaimed name has to resolve somewhere, and two candidates make
	// it ambiguous. Load refuses the second, naming the first.
	Open bool
}

// mcpPath is where a tools/call is forwarded: the plugin's own door.
func (p Plugin) mcpPath() string {
	if p.MCPPath != "" {
		return p.MCPPath
	}
	return defaultMCPPath
}

// plugin is one Load'ed service across restarts. cur is read on every request
// through an atomic, and mu serializes lifecycle transitions so two Reloads
// cannot interleave.
type plugin struct {
	name     string
	prefix   string   // the first, for log lines and status
	prefixes []string // every subtree this plugin answers
	spec     Plugin

	app      *App // for logging and supervision when started on demand
	cur      atomic.Pointer[instance]
	mu       sync.Mutex
	reloads  atomic.Int64
	restarts atomic.Int64
	closed   bool // Shutdown ran; the supervisor must stop resurrecting it

	// disabled means Unload took it down deliberately. Without this a LAZY
	// plugin — the affordable default for a host composing many services — is
	// brought straight back by the next request that reaches its prefix, so
	// Unload would not stick for exactly the plugins most hosts run.
	// Reload clears it: bringing a version back is what re-enables it.
	disabled atomic.Bool

	// stopping tracks the goroutines draining replaced instances. Reload and
	// Unload retire an instance asynchronously so the caller is not blocked for
	// the drain window, which means a child can outlive the call that replaced
	// it. Shutdown waits on this: a host that exits must not leave a child
	// holding its stdout, or the process appears to hang after main returns.
	stopping sync.WaitGroup
}

// retire stops in asynchronously, tracked so Shutdown can wait for it.
func (p *plugin) retire(in *instance, grace time.Duration) {
	if in == nil {
		return
	}
	p.stopping.Add(1)
	go func() {
		defer p.stopping.Done()
		stop(in, grace)
	}()
}

// instance is one running child process and everything that must be released
// when it stops.
type instance struct {
	cmd     *exec.Cmd
	dir     string
	sock    string
	client  Client
	started time.Time

	// done is CLOSED when the child has exited, and exitErr holds why. A
	// closed channel broadcasts: the supervisor, a drain, and Shutdown can all
	// observe the same exit. A single-value channel would let whoever read
	// first starve the others.
	done    chan struct{}
	exitErr error
}

// target is the hot path: what the mounted route should talk to right now.
// For a lazy plugin the first caller through here starts it; the atomic load
// keeps the steady-state cost to one load once it is running.
func (p *plugin) target() (Client, string) {
	if in := p.cur.Load(); in != nil {
		return in.client, in.sock
	}
	if p.app == nil || !p.spec.Lazy || p.disabled.Load() {
		return nil, "" // eager and down, disabled, or unloaded — the route answers 503
	}
	return p.startOnDemand()
}

// Start brings the plugin named name up if it is not already, and reports the
// address it serves on. It is what [App.Reload] is not: idempotent. Calling it
// on a running plugin returns that plugin, where Reload would start a SECOND
// child and retire the first.
//
// It exists because a lazy plugin has exactly one trigger — a request reaching
// one of its prefixes — and a host may have another way in. Hanzo's fleet
// reaches an app over its own unix socket, which never touches the router, so
// the plugin was never started and the socket was never bound: correct by
// design, inert in practice. Start is that second door, and it deliberately
// goes through target() — the SAME single-flighted path a prefix request
// takes — rather than a parallel one, so a burst of first callers arriving by
// either door still produces exactly one child.
//
// An Unload'ed plugin stays down: bringing one back is Reload's job, and a
// deliberate stop that any caller could undo would not be a stop at all.
func (a *App) Start(name string) (string, error) {
	a.plugMu.Lock()
	p := a.plugins[name]
	a.plugMu.Unlock()
	if p == nil {
		return "", fmt.Errorf("zip: Start: no plugin named %q", name)
	}
	if client, addr := p.target(); client != nil {
		return addr, nil
	}
	if p.disabled.Load() {
		return "", fmt.Errorf("zip: Start(%s): unloaded — Reload brings it back", name)
	}
	return "", fmt.Errorf("zip: Start(%s): not running", name)
}

// startOnDemand brings a lazy plugin up on its first request, single-flighted
// so a burst of concurrent first requests produces ONE child rather than one
// per request. A start failure is not cached: the next request tries again,
// because the usual cause is a dependency that has not come up yet.
func (p *plugin) startOnDemand() (Client, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if in := p.cur.Load(); in != nil {
		return in.client, in.sock // won by another caller while we waited
	}
	// Re-checked under the lock, where Unload set it, so a request racing an
	// Unload cannot slip a child in behind it.
	if p.closed || p.disabled.Load() {
		return nil, ""
	}
	in, err := start(p.spec)
	if err != nil {
		p.app.logger.Error("zip lazy plugin failed to start", "name", p.name, "err", err)
		return nil, ""
	}
	p.cur.Store(in)
	p.app.logger.Info("zip lazy plugin started on first request",
		"name", p.name, "pid", in.cmd.Process.Pid, "addr", in.sock)
	go p.app.supervise(p, in)
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
// prefixes is variadic because a service often owns more than one route
// subtree — o11y answers both /v1/o11y and /v1/sentry — and a single-prefix
// Load silently 404s the others. One call declares everything the plugin owns.
// Load returns the plugin as a DEFINITION — an [*App] the host composes with
// Use, like any other. It is not a verb and not a Service: a plugin is a unit of
// functionality that answers addresses, which is exactly what an App is, so it
// arrives through the one composition verb rather than through a second one.
//
//	billing, err := zip.Load(zip.Plugin{Name: "billing", Bin: bin}, "/v1/billing")
//	if err != nil { return err }
//	app.Use(billing)
//
// Prefix collisions between two plugins are caught where every other address
// collision is caught — by the walk, at build, naming both claimants and every
// collision at once. There is no separate prefix-claim ledger, because a second
// mechanism for one question is a second answer waiting to disagree.
func Load(p Plugin, prefixes ...string) (*App, error) {
	leaf := newApp(Config{AppName: p.Name, DisableStartupMessage: true,
		OpenAPI: OpenAPIConfig{Disabled: true}, MCP: MCPConfig{Disabled: true}})
	if err := leaf.load(prefixes, p); err != nil {
		return nil, err
	}
	return leaf, nil
}

func (a *App) load(prefixes []string, spec Plugin) error {
	if spec.Name == "" {
		return fmt.Errorf("zip: Load needs a Plugin.Name")
	}
	if len(prefixes) == 0 {
		return fmt.Errorf("zip: Load(%s) needs at least one prefix", spec.Name)
	}
	prefix := prefixes[0]


	// The MCP catalogue is read BEFORE anything is mounted or spawned, for the
	// same reason the prefixes are claimed first: a composition that cannot be
	// described must not first pay for its process.
	tools, err := parseTools(spec.Name, spec.Tools)
	if err != nil {
		return fmt.Errorf("zip: Load(%s): %w", spec.Name, err)
	}

	if spec.Addr != "" {
		for _, pre := range prefixes {
			if err := a.mount(pre, spec.Addr); err != nil {
				return err
			}
		}
		// Recorded even though this host did not start it, so Plugins() reports
		// the whole surface a request can reach rather than only the children.
		// A fleet view that silently omits remote mounts is worse than none.
		a.plugMu.Lock()
		if a.plugins == nil {
			a.plugins = map[string]*plugin{}
		}
		rp := &plugin{name: spec.Name, prefix: prefix, prefixes: prefixes, spec: spec}
		a.plugins[spec.Name] = rp
		err := a.installTools(rp, tools)
		a.plugMu.Unlock()
		if err != nil {
			return fmt.Errorf("zip: Load(%s): %w", spec.Name, err)
		}
		return nil
	}
	if len(spec.Bin) == 0 && spec.Path == "" && spec.URL == "" {
		return fmt.Errorf("zip: Load(%s): plugin %q has no Addr, Bin, Path, or URL", prefix, spec.Name)
	}
	if spec.URL != "" && spec.Sum == "" {
		return fmt.Errorf("zip: Load(%s): plugin %q has URL but no Sum — refusing to run an unverified download", prefix, spec.Name)
	}

	p := &plugin{name: spec.Name, prefix: prefix, prefixes: prefixes, spec: spec, app: a}
	var in *instance
	if !spec.Lazy {
		var err error
		if in, err = start(spec); err != nil {
			return fmt.Errorf("zip: Load(%s): %w", spec.Name, err)
		}
		p.cur.Store(in)
	}

	a.plugMu.Lock()
	if a.plugins == nil {
		a.plugins = map[string]*plugin{}
	}
	if _, dup := a.plugins[spec.Name]; dup {
		a.plugMu.Unlock()
		stop(in, 0) // nil for a lazy plugin — stop handles that
		return fmt.Errorf("zip: Load(%s): plugin %q already loaded", prefix, spec.Name)
	}
	a.plugins[spec.Name] = p
	toolErr := a.installTools(p, tools)
	a.plugMu.Unlock()
	if toolErr != nil {
		a.plugMu.Lock()
		delete(a.plugins, spec.Name)
		a.plugMu.Unlock()
		stop(in, 0)
		return fmt.Errorf("zip: Load(%s): %w", spec.Name, toolErr)
	}

	if in != nil {
		a.logger.Info("zip loaded plugin", "name", spec.Name, "prefix", prefix,
			"pid", in.cmd.Process.Pid, "addr", in.sock)
	} else {
		a.logger.Info("zip loaded plugin (lazy — starts on first request)",
			"name", spec.Name, "prefix", prefix)
	}

	// Registered once, for the life of the app. Reload swaps what target()
	// returns; it never touches the router.
	for _, pre := range prefixes {
		a.mountVia(pre, p.target)
	}
	if in != nil {
		go a.supervise(p, in)
	}

	a.OnShutdown(func(context.Context) error {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		if cur := p.cur.Swap(nil); cur != nil {
			stop(cur, 0)
		}
		// Wait out any instance a Reload or Unload is still draining, so no
		// child survives this host.
		p.stopping.Wait()
		return nil
	})
	return nil
}

// Reload swaps the plugin named name to to, without dropping a request.
// The zero Plugin restarts what it is already running; Bin runs new bytes;
// URL+Sum pins a version or rolls one back, off disk if that digest has run
// before. Only the source moves: name and prefixes are fixed at Load.
//
// The new process must be listening before any request moves to it, so a bad
// build returns an error and leaves the old one serving. The old one drains
// for Plugin.Drain, then dies.
func (a *App) Reload(name string, to Plugin) error {
	_, p := a.pluginNamed(name)
	if p == nil {
		return fmt.Errorf("zip: Reload: no plugin named %q", name)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	spec, err := p.spec.replacedBy(to)
	if err != nil {
		return fmt.Errorf("zip: Reload(%s): %w", name, err)
	}
	next, err := start(spec)
	if err != nil {
		return fmt.Errorf("zip: Reload(%s): %w", name, err)
	}

	prev := p.cur.Swap(next) // every request from here uses next
	p.spec = spec
	p.disabled.Store(false) // a version is running again, so the route is live again
	p.reloads.Add(1)
	a.logger.Info("zip reloaded plugin", "name", name,
		"pid", next.cmd.Process.Pid, "addr", next.sock, "version", spec.Sum)

	if prev != nil {
		drain := spec.Drain
		if drain <= 0 {
			drain = 5 * time.Second
		}
		p.retire(prev, drain)
	}
	return nil
}

// replacedBy returns the spec to start next. A to that names no artifact reuses
// the current one, which is how a crashed or wedged plugin is restarted.
func (spec Plugin) replacedBy(to Plugin) (Plugin, error) {
	if spec.Addr != "" {
		// Nothing was started, so there is nothing to swap; pointing the mount
		// somewhere else would have to rewrite the route table.
		return spec, fmt.Errorf("plugin is a remote Addr — reload does not apply")
	}
	if to.Bin == nil && to.Path == "" && to.URL == "" {
		return spec, nil
	}
	// A new artifact replaces the old one whole. Clearing the other three
	// matters because start() prefers Bin, then URL, then Path: a leftover Bin
	// would silently shadow the URL just asked for, and the swap would report
	// success having started the version it was already running.
	spec.Bin, spec.Path, spec.URL, spec.Sum = to.Bin, to.Path, to.URL, to.Sum
	if spec.URL != "" && spec.Sum == "" {
		return spec, fmt.Errorf("URL but no Sum — refusing to run an unverified download")
	}
	if to.Args != nil {
		spec.Args = to.Args
	}
	if to.Env != nil {
		spec.Env = to.Env
	}
	return spec, nil
}

// Unload stops the plugin named name. Its routes stay registered and answer
// 503 until a Reload brings it back — the route table is never mutated, which
// is what keeps repeated load/unload cycles flat.
//
// 503 rather than 404 is deliberate. 404 says "no such API", which a client is
// entitled to cache and stop retrying; 503 says "this API exists and is down",
// which is both true and retryable. [Status.Disabled] is how an operator
// tells a deliberate stop from a crash, since the wire looks the same either way.
func (a *App) Unload(name string) error {
	_, p := a.pluginNamed(name)
	if p == nil {
		return fmt.Errorf("zip: Unload: no plugin named %q", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled.Store(true) // before the swap, so no request in the gap restarts it
	p.retire(p.cur.Swap(nil), 0)
	return nil
}

// start extracts (if embedded), launches, and waits for the child to listen.
// Every failure path releases what it already took.
func start(spec Plugin) (*instance, error) {
	// One private directory per instance holds the extracted binary and the
	// socket, so a reload's new instance never collides with the old one's
	// socket path and cleanup is a single RemoveAll.
	dir, err := os.MkdirTemp(spec.Dir, "zip-"+spec.Name+"-")
	if err != nil {
		return nil, err
	}
	bin := spec.Path
	switch {
	case len(spec.Bin) > 0:
		bin = filepath.Join(dir, spec.Name)
		if err := os.WriteFile(bin, spec.Bin, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("write binary: %w", err)
		}
	case spec.URL != "":
		// Cached beside dir rather than inside it, so the download survives
		// this instance and a reload or restart reuses it.
		var err error
		bin, err = fetch(spec)
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}

	sock := socketIn(dir, spec.Name)
	cmd := exec.Command(bin, spec.Args...)
	cmd.Env = append(append(os.Environ(), spec.Env...), AddrEnv+"="+sock)
	// The child's output is the operator's only window into it, so it goes where
	// the host's own does rather than being swallowed — but tagged, because an
	// untagged line in a merged stream is worse than useless: you can see that
	// something is wrong and not which plugin is wrong. Tagging is the whole
	// difference between one log stream and N attributable ones.
	cmd.Stdout = &tagWriter{w: os.Stdout, tag: []byte("[" + spec.Name + "] ")}
	cmd.Stderr = &tagWriter{w: os.Stderr, tag: []byte("[" + spec.Name + "] ")}
	tieToHost(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start: %w", err)
	}

	// Exactly one Wait for this process's lifetime; everyone else observes the
	// close. Two Waits is an error, and a child nobody waits on is a zombie.
	in := &instance{cmd: cmd, dir: dir, sock: sock, started: time.Now(), done: make(chan struct{})}
	go func() {
		in.exitErr = cmd.Wait()
		close(in.done)
	}()

	if err := waitListening(sock, spec.Start, in.done, in); err != nil {
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

// fetch downloads spec.URL, verifies it against spec.Sum, and returns the path
// to the cached executable. A binary already cached under that digest is reused
// without touching the network, so a restart is offline and a rollback to a
// previously run version costs nothing.
//
// The digest is checked BEFORE the file is made executable or given its final
// name, so a truncated or substituted download is never runnable — the failure
// mode is a missing plugin, not a wrong one.
func fetch(spec Plugin) (string, error) {
	cache := filepath.Join(spec.Dir, "zip-plugins")
	if spec.Dir == "" {
		cache = filepath.Join(os.TempDir(), "zip-plugins")
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", fmt.Errorf("plugin cache: %w", err)
	}
	// Keyed on the digest ALONE, never on the name: one artifact commonly serves
	// many plugins — a multi-call binary mounted 108 times under 108 names is
	// one 195MB download, not 108 of the same bytes. The first plugin to start
	// warms every other.
	final := filepath.Join(cache, spec.Sum)
	if _, err := os.Stat(final); err == nil {
		return final, nil // already verified once; the name IS the digest
	}

	resp, err := http.Get(spec.URL)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", spec.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", spec.URL, resp.Status)
	}

	tmp, err := os.CreateTemp(cache, spec.Name+"-*.part")
	if err != nil {
		return "", fmt.Errorf("plugin cache: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, sum), resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("fetch %s: %w", spec.URL, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != spec.Sum {
		return "", fmt.Errorf("plugin %s: sha256 %s does not match Sum %s — refusing to run it", spec.Name, got, spec.Sum)
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil {
		return "", err
	}
	// Rename last: the digest-named file exists only once it is verified, so a
	// concurrent host either sees nothing or sees a good binary.
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	return final, nil
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
		case <-in.done: // already gone; nothing to wait out
		case <-time.After(grace):
		}
	}
	if in.cmd != nil && in.cmd.Process != nil {
		_ = in.cmd.Process.Kill()
		select {
		case <-in.done:
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
func waitListening(sock string, limit time.Duration, done <-chan struct{}, in *instance) error {
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
		case <-done:
			return fmt.Errorf("exited before listening: %v", in.exitErr)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("did not listen on %s within %s", sock, limit)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// tagWriter prefixes each line a plugin writes with its name, so a host serving
// several plugins produces one stream that is still attributable. It splits on
// newlines rather than on Write calls, because a child may emit a line across
// several writes or several lines in one.
type tagWriter struct {
	w       io.Writer
	tag     []byte
	mu      sync.Mutex
	midline bool // a previous Write ended without a newline
}

func (t *tagWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		if !t.midline {
			if _, err := t.w.Write(t.tag); err != nil {
				return 0, err
			}
			t.midline = true
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			_, err := t.w.Write(p)
			return n, err
		}
		if _, err := t.w.Write(p[:i+1]); err != nil {
			return 0, err
		}
		t.midline = false
		p = p[i+1:]
	}
	return n, nil
}

// supervise watches one instance and brings the plugin back if it dies on its
// own. Without this a crashed plugin is invisible and permanent: the child is
// gone but cur still points at it, so every request gets a connection error
// dressed up as a 502, Plugins() keeps reporting Running, and only a manual
// Reload recovers. A plugin is a separate process precisely so a panic cannot
// take the host down — that is only true if the host notices.
//
// An instance replaced by Reload or Unload also "exits", which is expected and
// not a crash. The compare-and-swap distinguishes them: only the instance that
// is still current when it dies was unexpected.
func (a *App) supervise(p *plugin, in *instance) {
	<-in.done
	if !p.cur.CompareAndSwap(in, nil) {
		return // replaced deliberately; its retirement is already someone's job
	}
	a.logger.Error("zip plugin exited unexpectedly",
		"name", p.name, "pid", in.cmd.Process.Pid, "err", in.exitErr)
	stop(in, 0)

	// Restart with backoff. Requests answer 503 in the gap — "deployed but
	// down", which is the truth — rather than 502, which would blame the
	// upstream for a process that no longer exists.
	//
	// Backoff is capped and retried indefinitely rather than given up on: the
	// common cause is a dependency that is itself restarting, and a plugin that
	// recovers on its own is worth far more than one that stays dead because a
	// retry budget ran out while nobody was watching.
	delay := 100 * time.Millisecond
	for {
		p.mu.Lock()
		shuttingDown := p.closed
		spec := p.spec
		p.mu.Unlock()
		if shuttingDown {
			return
		}
		time.Sleep(delay)
		if delay *= 2; delay > 30*time.Second {
			delay = 30 * time.Second
		}
		next, err := start(spec)
		if err != nil {
			a.logger.Error("zip plugin restart failed", "name", p.name, "err", err)
			continue
		}
		if !p.cur.CompareAndSwap(nil, next) {
			stop(next, 0) // a Reload beat us to it
			return
		}
		p.restarts.Add(1)
		a.logger.Info("zip plugin restarted", "name", p.name, "pid", next.cmd.Process.Pid)
		go a.supervise(p, next)
		return
	}
}
