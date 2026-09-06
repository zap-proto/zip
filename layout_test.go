// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

import (
	"reflect"
	"testing"
	"time"
)

type layEmbedded struct {
	Height uint32 `json:"height"`
}

type layAll struct {
	Flag   bool
	Small  uint8
	Wide   uint32
	Big    int64
	Ratio  float64
	Name   string
	Blob   []byte
	ID     [32]byte
	List   []uint32
	Nested layEmbedded
	When   time.Time
}

// The rules restated over a manifest are the rules the encoder writes. Every
// offset, width and IDL spelling is held against [LayoutOf], which is what the
// Go encoder itself derives — so the two cannot drift, and a language that
// derives the same layout at compile time is deriving THIS one.
func TestLayoutAgreesWithTheEncoder(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeOf(layAll{}),
		reflect.TypeOf(layEmbedded{}),
	} {
		want, err := LayoutOf(rt)
		if err != nil {
			t.Fatalf("%s: the encoder refused: %v", rt, err)
		}
		m, ty := describeType(rt)
		got, err := layoutOf(m, ty, map[string]bool{})
		if err != nil {
			t.Fatalf("%s: the manifest refused what the encoder took: %v", rt, err)
		}
		if got.Size != want.Size {
			t.Errorf("%s: size %d, encoder says %d", rt, got.Size, want.Size)
		}
		if len(got.Slots) != len(want.Slots) {
			t.Fatalf("%s: %d slots, encoder says %d", rt, len(got.Slots), len(want.Slots))
		}
		for i, s := range got.Slots {
			w := want.Slots[i]
			if s.Name != w.Name || s.Offset != w.Offset || s.Width != w.Width || s.Type != w.Type || s.Elem != w.Elem {
				t.Errorf("%s.%s: %+v, encoder says %+v", rt, s.Name, s, w)
			}
		}
	}
}

// What the encoder refuses, the manifest refuses — and names the same field.
func TestLayoutRefusesWhatTheEncoderRefuses(t *testing.T) {
	type hasMap struct{ M map[string]int }
	type hasAny struct{ A any }
	type hasChan struct{ C chan int }
	for _, rt := range []reflect.Type{
		reflect.TypeOf(hasMap{}),
		reflect.TypeOf(hasAny{}),
		reflect.TypeOf(hasChan{}),
	} {
		if _, err := LayoutOf(rt); err == nil {
			t.Fatalf("%s: the encoder took it; this test is about what it does not", rt)
		}
		m, ty := describeType(rt)
		if _, err := layoutOf(m, ty, map[string]bool{}); err == nil {
			t.Errorf("%s: the manifest laid out what the encoder refused", rt)
		}
	}
}

// A type that contains itself has no bounded width, and saying so is better than
// running out of stack.
func TestLayoutRefusesATypeThatContainsItself(t *testing.T) {
	type node struct {
		Name string
		Next *node
	}
	m, ty := describeType(reflect.TypeOf(node{}))
	if _, err := layoutOf(m, ty, map[string]bool{}); err == nil {
		t.Fatal("a recursive type was given a layout")
	}
}
