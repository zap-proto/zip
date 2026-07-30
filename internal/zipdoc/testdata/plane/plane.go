// Package plane holds the in-fleet call plane's shapes. An op's In and Out live
// HERE, one name one shape, and the ops that carry them live in the packages
// that serve them — which is the whole reason a doc comment has to cross a
// package boundary to reach the spec.
package plane

// ListIn asks for a page of workers.
type ListIn struct {
	// Org whose workers to list.
	Org string `json:"org" validate:"required"`
	// Limit caps how many come back.
	Limit int `json:"limit,omitempty"`
}

// ListOut is a page of workers.
type ListOut struct {
	// Workers, newest first.
	Workers []Worker `json:"workers"`
	// Next is the cursor for the following page.
	Next string `json:"next"`
}

// Worker is one deployed script. Nested a package away, so its prose has the
// furthest to travel.
type Worker struct {
	// Name is the worker's stable name, unique in the org.
	Name string `json:"name"`
	// Live reports whether it is serving traffic.
	Live bool `json:"live"`
}
