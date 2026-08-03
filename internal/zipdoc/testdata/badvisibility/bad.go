// Package badvisibility compiles fine and declares itself wrongly: its
// Visibility is a word this grammar does not know. Generation has to fail on it,
// because the alternative is a product that was meant to be sold and is silently
// withheld — the one mistake a defaulting-to-hidden field can make invisible.
//
// Visibility: Public
package badvisibility

// Store is here so the package is not empty.
type Store struct{}
