package zip

import (
	"sort"
	"time"
)

// What a host is running. A fleet view needs one thing this package did not
// expose: the ability to ask a live process what it actually loaded, at which
// version, and whether it is still up. Reading it from deployment config
// answers what was INTENDED; only the process knows what is TRUE — which is the
// difference that matters during a rolling upgrade, when the two disagree by
// design.

// PluginStatus is one loaded plugin as the host currently sees it.
type PluginStatus struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`

	// Source is where the binary came from: "embedded", "path", "url", or
	// "remote" for an instance this host did not start.
	Source string `json:"source"`

	// Version is the artifact's SHA-256 when it was installed from a URL —
	// the only version identifier that cannot drift from the bits actually
	// running, since it IS the bits. Empty for the other sources.
	Version string `json:"version,omitempty"`

	// Addr is the socket or address serving it.
	Addr string `json:"addr,omitempty"`

	// PID is the child process, or 0 when this host did not start it.
	PID int `json:"pid,omitempty"`

	// Running is false after Unload, or after a child exited and no Reload has
	// replaced it. Its routes stay registered and answer 503, so a false here
	// is the difference between "not deployed" and "deployed but down".
	Running bool `json:"running"`

	// Since is when the CURRENT instance started — it resets on Reload, so it
	// reports the age of what is running, not of the mount.
	Since time.Time `json:"since,omitzero"`

	// Reloads counts successful swaps since Load. A climbing number on one
	// host and not its peers is the signal that a rollout is uneven.
	Reloads int `json:"reloads"`

	// Restarts counts times the supervisor brought this plugin back after it
	// died on its own. Distinct from Reloads, which are deliberate: a nonzero
	// Restarts is a plugin crashing, and a climbing one is a crash loop.
	Restarts int `json:"restarts"`

	// Usage is what this plugin costs right now, read from the kernel.
	Usage Usage `json:"usage,omitzero"`
}

// Usage is a plugin's resource cost, measured rather than self-reported.
//
// This exists because a plugin is a process. A linked-in subsystem cannot be
// measured this way at any price — one process has one RSS and one CPU clock,
// and nothing inside it can be attributed to a subsystem. Running a service as
// its own process makes "which app is eating the memory" a question with an
// exact answer instead of an argument.
//
// Zero means not measured (no /proc, or nothing running), not measured-as-zero.
type Usage struct {
	// CPU is total user+system time this instance has consumed since it
	// started. It only ever climbs, so a rate is the interesting derivative.
	CPU time.Duration `json:"cpuNs,omitzero"`
	// RSS is resident memory in bytes — the honest number for "what does this
	// plugin cost", as opposed to virtual size.
	RSS int64 `json:"rssBytes,omitzero"`
	// Threads and FDs are the two limits a busy service hits first, and both
	// are leaks when they climb without bound.
	Threads int `json:"threads,omitzero"`
	FDs     int `json:"fds,omitzero"`
}

// Plugins reports every plugin this host has loaded, ordered by name so a diff
// between two hosts is stable. Safe to call while requests are in flight.
func (a *App) Plugins() []PluginStatus {
	a.plugMu.Lock()
	ps := make([]*plugin, 0, len(a.plugins))
	for _, p := range a.plugins {
		ps = append(ps, p)
	}
	a.plugMu.Unlock()

	out := make([]PluginStatus, 0, len(ps))
	for _, p := range ps {
		s := PluginStatus{
			Name:    p.name,
			Prefix:  p.prefix,
			Source:  p.source(),
			Version: p.spec.Sum,
			Reloads:  int(p.reloads.Load()),
			Restarts: int(p.restarts.Load()),
		}
		if in := p.cur.Load(); in != nil {
			s.Running = true
			s.Addr = in.sock
			s.Since = in.started
			if in.cmd != nil && in.cmd.Process != nil {
				s.PID = in.cmd.Process.Pid
				s.Usage = readUsage(s.PID)
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// source names where this plugin's binary came from.
func (p *plugin) source() string {
	switch {
	case p.spec.Addr != "":
		return "remote"
	case p.spec.URL != "":
		return "url"
	case len(p.spec.Bin) > 0:
		return "embedded"
	case p.spec.Path != "":
		return "path"
	}
	return "unknown"
}
