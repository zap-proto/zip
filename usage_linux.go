//go:build linux

package zip

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// readUsage reports what the kernel already knows about a running plugin.
//
// This is the observability a monolith cannot give you. In one process you can
// measure the process; you cannot say which subsystem is holding the RSS or
// burning the CPU, because there is no boundary to measure at. A plugin IS a
// process, so per-plugin CPU, memory, thread and descriptor counts come from
// /proc for free — exact, continuous, and with no instrumentation in the
// plugin at all. It cannot drift from reality because it is not self-reported.
func readUsage(pid int) Usage {
	var u Usage
	if pid <= 0 {
		return u
	}
	dir := "/proc/" + strconv.Itoa(pid)

	// utime+stime are fields 14 and 15, and num_threads is 20 — but comm
	// (field 2) is parenthesised and may itself contain spaces and parens, so
	// fields are counted from the LAST ')' rather than by splitting the line.
	if b, err := os.ReadFile(dir + "/stat"); err == nil {
		if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) {
			f := strings.Fields(string(b)[i+2:])
			// After comm, field 3 is state, so utime/stime/num_threads land at
			// offsets 11, 12 and 17 of this slice.
			if len(f) > 17 {
				ut, _ := strconv.ParseInt(f[11], 10, 64)
				st, _ := strconv.ParseInt(f[12], 10, 64)
				// USER_HZ is 100 on every Linux target we ship to.
				u.CPU = time.Duration(ut+st) * 10 * time.Millisecond
				u.Threads, _ = strconv.Atoi(f[17])
			}
		}
	}

	// VmRSS is the resident set — what this plugin actually costs in memory.
	if b, err := os.ReadFile(dir + "/status"); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if kb, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				if f := strings.Fields(kb); len(f) > 0 {
					n, _ := strconv.ParseInt(f[0], 10, 64)
					u.RSS = n * 1024
				}
				break
			}
		}
	}

	if ents, err := os.ReadDir(dir + "/fd"); err == nil {
		u.FDs = len(ents)
	}
	return u
}
