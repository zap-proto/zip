package runtime

import (
	"strings"
	"testing"
)

func TestTranspileToES5_RunsInGoja(t *testing.T) {
	// TS with arrow fn + spread + type annotation. esbuild lowers it to
	// goja-runnable JS and strips the types.
	src := []byte(`
		const add = (...xs: number[]): number => xs.reduce((a, b) => a + b, 0);
		module.exports = { total: add(1, 2, 3, 4) };
	`)
	out, err := TranspileToES5(src, ESOptions{Loader: "ts", Sourcefile: "add.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ": number") {
		t.Fatalf("types not stripped: %s", out)
	}

	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})
	if err := rt.LoadModule("add", string(out)); err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval(`require("add").total`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(int64); got != 10 {
		t.Fatalf("total = %d, want 10", got)
	}
}

func TestTranspileError(t *testing.T) {
	_, err := TranspileToES5([]byte("const x = ;"), ESOptions{Loader: "ts"})
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
}
