package zip_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestIdle_ReclaimsRealMemory is the claim measured rather than asserted: what
// eviction is FOR is resident memory, so this reads the operating system's
// number for the child processes, not a counter the code keeps about itself.
//
// It warms N plugins, records the resident set of every child, evicts, and
// records it again. A test that only checked Running would pass on an
// implementation that leaked the process while forgetting the pointer.
//
// Run with -v to see the numbers:
//
//	go test -run TestIdle_ReclaimsRealMemory -v .
func TestIdle_ReclaimsRealMemory(t *testing.T) {
	const plugins = 6
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	for i := 0; i < plugins; i++ {
		name := fmt.Sprintf("demo%d", i)
		app.Use(must(zip.Load(
			zip.Plugin{
				Name: name, Bin: bin, Dir: sockDir(t), Lazy: true,
				IdleAfter: 50 * time.Millisecond,
			}, "/v1/"+name,
		)))
	}
	defer func() { _ = app.Shutdown() }()

	// Warm every one, the way a host serving its whole catalog would.
	for i := 0; i < plugins; i++ {
		if status, _ := call(t, app, "GET", fmt.Sprintf("/v1/demo%d/version", i), ""); status != 200 {
			t.Fatalf("demo%d did not serve", i)
		}
	}
	warmPIDs := runningPIDs(t, app)
	if len(warmPIDs) != plugins {
		t.Fatalf("warmed %d plugins, want %d", len(warmPIDs), plugins)
	}
	warmRSS := totalRSS(t, warmPIDs)

	time.Sleep(80 * time.Millisecond)
	if n := app.Evict(); n != plugins {
		t.Fatalf("evicted %d of %d", n, plugins)
	}
	if !settles(3*time.Second, func() bool { return len(runningPIDs(t, app)) == 0 }) {
		t.Fatalf("plugins still running after eviction: %v", runningPIDs(t, app))
	}

	// The processes are gone from the OS, not merely forgotten by the host.
	// This is the half a Running check cannot make.
	//
	// It has to WAIT for them. retire() is asynchronous by design — the caller
	// is not blocked for the drain — so Running goes false the moment cur is
	// cleared, which is strictly before the child is reaped. Asserting
	// residency at that instant tests the scheduler, not the code: it passed
	// on one machine and failed on the next, which is how this comment came to
	// be written.
	alive := len(warmPIDs)
	settles(5*time.Second, func() bool {
		alive = 0
		for _, pid := range warmPIDs {
			if rssOf(pid) > 0 {
				alive++
			}
		}
		return alive == 0
	})
	if alive != 0 {
		t.Errorf("%d of %d evicted processes still resident after 5s — the pointer was dropped but the process was not", alive, len(warmPIDs))
	}

	t.Logf("%d plugins warm: %.1f MiB resident across %d child processes",
		plugins, float64(warmRSS)/1024, len(warmPIDs))
	t.Logf("after eviction:  0 child processes, %.1f MiB reclaimed", float64(warmRSS)/1024)
	t.Logf("per plugin:      %.1f MiB — multiply by a catalog to see why this matters",
		float64(warmRSS)/1024/float64(plugins))

	// And the catalog still works: eviction is not a teardown.
	if status, _ := call(t, app, "GET", "/v1/demo0/version", ""); status != 200 {
		t.Fatal("a route stopped answering after its plugin was evicted")
	}
}

func runningPIDs(t *testing.T, app *zip.App) []int {
	t.Helper()
	var out []int
	for _, p := range app.Plugins() {
		if p.Running && p.PID != 0 {
			out = append(out, p.PID)
		}
	}
	return out
}

// rssOf reads VmRSS in KiB from /proc, or 0 if the process is gone. The OS is
// the authority here; anything the host reports about itself could be wrong in
// exactly the way this test exists to catch.
func rssOf(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return kb
			}
		}
	}
	return 0
}

func totalRSS(t *testing.T, pids []int) int {
	t.Helper()
	sum := 0
	for _, pid := range pids {
		sum += rssOf(pid)
	}
	return sum
}
