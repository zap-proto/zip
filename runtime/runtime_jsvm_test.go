package runtime

import (
	"fmt"
	"strconv"
	"testing"
)

func TestEval(t *testing.T) {
	rt, err := NewJSRuntime(JSOptions{PoolSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval("1 + 2")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.(int64); !ok || got != 3 {
		t.Fatalf("eval 1+2 = %v (%T), want int64(3)", v, v)
	}
}

func TestEvalString(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})
	v, err := rt.Eval(`["a","b","c"].join("-")`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "a-b-c" {
		t.Fatalf("got %v, want a-b-c", v)
	}
}

func TestRegisterHostFn(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 2})
	if err := rt.RegisterHostFn("addOne", func(n int) int { return n + 1 }); err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval("addOne(41)")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.(int64); !ok || got != 42 {
		t.Fatalf("addOne(41) = %v (%T), want 42", v, v)
	}
}

func TestHostFnViaOptions(t *testing.T) {
	rt, err := NewJSRuntime(JSOptions{
		PoolSize: 2,
		HostFns: map[string]any{
			"double": func(n int) int { return n * 2 },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := rt.Eval("double(21)")
	if got := v.(int64); got != 42 {
		t.Fatalf("double(21) = %d, want 42", got)
	}
}

func TestLoadModule(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 2})
	src := `module.exports = { greet: function(name){ return "hi " + name } }`
	if err := rt.LoadModule("greeter", src); err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval(`require("greeter").greet("zip")`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "hi zip" {
		t.Fatalf("got %v, want 'hi zip'", v)
	}
}

func TestModuleViaOptions(t *testing.T) {
	rt, err := NewJSRuntime(JSOptions{
		PoolSize: 1,
		Modules: map[string]string{
			"math": `exports.sq = function(n){ return n*n }`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := rt.Eval(`require("math").sq(9)`)
	if got := v.(int64); got != 81 {
		t.Fatalf("sq(9) = %d, want 81", got)
	}
}

func TestPoolConcurrency(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 4})
	_ = rt.RegisterHostFn("identity", func(n int) int { return n })
	done := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func(n int) {
			v, err := rt.Eval("identity(" + strconv.Itoa(n) + ")")
			if err != nil {
				done <- err
				return
			}
			if int(v.(int64)) != n {
				done <- fmt.Errorf("got %v want %d", v, n)
				return
			}
			done <- nil
		}(i)
	}
	for i := 0; i < 32; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
