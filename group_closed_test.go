package zip_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A GROUP CANNOT REACH ABOVE ITSELF, and this file is the compile-and-reflect
// proof of it.
//
// It is the property a host relies on to hand a subsystem a bounded surface: the
// subsystem holds a group at its own prefix, and there is no method that returns
// the app, the parent, the root or the router underneath. So confinement is a
// property of the type rather than a rule something has to police — which is
// what let hanzoai/cloud delete a whole file whose job was policing it, along
// with the bound view, the out-of-bound recording and the boot refusal that
// carried it.
//
// One accessor added back and every subsystem can reach the whole binary again.
// A comment asking for the property is not the property, so this test asks the
// type.
func TestGroup_CannotReachAboveItself(t *testing.T) {
	g := reflect.TypeOf(&zip.Group{})
	app := reflect.TypeOf(&zip.App{})

	for i := range g.NumMethod() {
		m := g.Method(i)
		switch m.Name {
		case "App", "Parent", "Root", "Fiber", "Router", "Unwrap":
			t.Errorf("(*Group).%s exists; a group that hands back its host is not a bound", m.Name)
		}
		for j := range m.Type.NumOut() {
			out := m.Type.Out(j)
			if out == app {
				t.Errorf("(*Group).%s returns *App", m.Name)
			}
			if p := out.PkgPath(); strings.Contains(p, "fiber") {
				t.Errorf("(*Group).%s returns %s, a router this package is meant to hide", m.Name, out)
			}
		}
	}

	// And the state it holds is its own: a caller cannot read the app off it, or
	// write a prefix onto it, from outside this package.
	if n := reflect.TypeOf(zip.Group{}).NumField(); n != 0 {
		for i := range n {
			if f := reflect.TypeOf(zip.Group{}).Field(i); f.IsExported() {
				t.Errorf("Group.%s is exported; the handle is only closed while its fields are", f.Name)
			}
		}
	}
}
