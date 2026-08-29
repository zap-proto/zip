package zapenc

import (
	"strings"
	"sync"
	"testing"
)

// A type that contains itself has no bounded width, so it has no ZAP layout.
// Four fleet apps carried one, and the derivation recursed until the stack
// ended — a stack overflow being a poor way to learn a type cannot cross.
type tree struct {
	Name     string
	Children []tree
}

type viaPointer struct {
	Next *viaPointer
}

type mutualA struct{ B []mutualB }
type mutualB struct{ A []mutualA }

func TestARecursiveTypeIsRefusedNotFatal(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"a slice of itself", tree{}},
		{"a pointer to itself", viaPointer{}},
		{"two types through each other", mutualA{}},
	} {
		_, err := Marshal(tc.v)
		if err == nil {
			t.Errorf("%s: crossed the plane; it has no bounded width", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "contains itself") {
			t.Errorf("%s: refused, but not for the reason: %v", tc.name, err)
		}
	}
}

// The refusal must not leave the in-progress set dirty: a second call has to
// fail the same way, not report a cycle that is no longer being walked.
func TestTheRefusalIsRepeatable(t *testing.T) {
	_, first := Marshal(tree{})
	_, second := Marshal(tree{})
	if first == nil || second == nil {
		t.Fatal("expected both to be refused")
	}
	if first.Error() != second.Error() {
		t.Errorf("second answer differs:\n  %v\n  %v", first, second)
	}
}

// A refused type must not poison the types beside it — the guard is a cold-path
// set, not a permanent verdict on everything derived after it.
func TestAGoodTypeStillCrossesAfterOneIsRefused(t *testing.T) {
	if _, err := Marshal(tree{}); err == nil {
		t.Fatal("expected refusal")
	}
	type fine struct {
		N uint64
		S string
	}
	if _, err := Marshal(fine{N: 1, S: "x"}); err != nil {
		t.Errorf("a sound type was refused after a recursive one: %v", err)
	}
}

// The derivation is reached from many goroutines; the guard must not be a
// concurrent map write. Run with -race.
func TestConcurrentDerivationIsSafe(t *testing.T) {
	type wide struct {
		A uint64
		B string
		C []byte
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Marshal(wide{})
			_, _ = Marshal(tree{})
		}()
	}
	wg.Wait()
}
