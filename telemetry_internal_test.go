package zip

import (
	"testing"

	luxlog "github.com/luxfi/log"
)

// The address is the switch, so these tests are about one question: what does
// this app export, and what does it cost when the answer is nothing.

// TestEnvironmentNames pins the spelling a deployment sets. A manifest and a
// program have to agree on it letter for letter, and a rename here is invisible
// at runtime — an unread variable and an absent one look identical.
func TestEnvironmentNames(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{envSpans, "O11Y_SPANS_ADDR"},
		{envLogs, "O11Y_LOGS_ADDR"},
		{envMetrics, "O11Y_METRICS_ADDR"},
	} {
		if c.got != c.want {
			t.Errorf("environment name = %q, want %q", c.got, c.want)
		}
	}
}

// TestResolveReadsTheStatedAddressThenTheEnvironment covers the whole rule: a
// field first, the environment second, nothing otherwise.
func TestResolveReadsTheStatedAddressThenTheEnvironment(t *testing.T) {
	t.Run("nothing stated anywhere", func(t *testing.T) {
		t.Setenv(envSpans, "")
		t.Setenv(envLogs, "")
		t.Setenv(envMetrics, "")
		got := resolve(Config{})
		if got.any() {
			t.Errorf("resolved %+v with nothing configured; an unstated signal is not exported", got)
		}
	})

	t.Run("the environment states them", func(t *testing.T) {
		t.Setenv(envSpans, "127.0.0.1:4317")
		t.Setenv(envLogs, "127.0.0.1:4318")
		t.Setenv(envMetrics, "127.0.0.1:4319")
		got := resolve(Config{})
		want := addresses{spans: "127.0.0.1:4317", logs: "127.0.0.1:4318", metrics: "127.0.0.1:4319"}
		if got != want {
			t.Errorf("resolved %+v, want %+v", got, want)
		}
	})

	t.Run("the program's own field wins", func(t *testing.T) {
		t.Setenv(envSpans, "127.0.0.1:4317")
		got := resolve(Config{Telemetry: Telemetry{Spans: "collector:4317"}})
		if got.spans != "collector:4317" {
			t.Errorf("spans = %q, want the address the program stated", got.spans)
		}
	})

	t.Run("blank is not an address", func(t *testing.T) {
		t.Setenv(envSpans, "   ")
		if got := resolve(Config{Telemetry: Telemetry{Spans: "  "}}); got.any() {
			t.Errorf("resolved %+v from whitespace; that is a variable somebody meant to unset", got)
		}
	})
}

// TestNoAddressStartsNothing is the cost of being off, stated where it can be
// measured: export returns before it registers the hook that its goroutine,
// its exporters and its connections all hang from. No hook is therefore no
// goroutine, no node, no port and no dial.
func TestNoAddressStartsNothing(t *testing.T) {
	t.Setenv(envSpans, "")
	t.Setenv(envLogs, "")
	t.Setenv(envMetrics, "")

	quiet := New(Config{AppName: "quiet", Logger: luxlog.NewNoOpLogger(), DisableStartupMessage: true})
	before := len(quiet.hooks)
	quiet.export()
	if len(quiet.hooks) != before {
		t.Fatalf("an app with nowhere to report registered %d shutdown hooks", len(quiet.hooks)-before)
	}

	// The same app with one address stated does start, so the test above is
	// measuring the switch rather than a broken export.
	loud := New(Config{
		AppName:               "loud",
		Logger:                luxlog.NewNoOpLogger(),
		DisableStartupMessage: true,
		Telemetry:             Telemetry{Spans: "127.0.0.1:1"},
	})
	loud.export()
	if len(loud.hooks) == 0 {
		t.Fatal("an app with a span address registered no shutdown hook, so nothing drains it")
	}
	if err := loud.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestOffKeepsNothing proves the buffer follows the address: an app that cannot
// ship must not accumulate spans and records for a drain that will never come.
func TestOffKeepsNothing(t *testing.T) {
	t.Setenv(envSpans, "")
	t.Setenv(envLogs, "")
	t.Setenv(envMetrics, "")

	if newTelemetry(Config{}).exports() {
		t.Error("an app with no span or log address collects for a shipment that cannot happen")
	}
	if !newTelemetry(Config{Telemetry: Telemetry{Logs: "127.0.0.1:4318"}}).exports() {
		t.Error("an app with a log address does not collect, so its flush has nothing to send")
	}
	// The metric address alone does not make the boundary collect: the numbers
	// come from the registry at flush time, not from a buffer.
	if newTelemetry(Config{Telemetry: Telemetry{Metrics: "127.0.0.1:4319"}}).exports() {
		t.Error("a metrics-only app buffers spans and records nothing will drain")
	}
}
