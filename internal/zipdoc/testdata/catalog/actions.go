// actions.go — the two write actions, kept apart from the read path.
//
// This file opens with a note about ITSELF, adjacent to the package clause and
// therefore a doc comment as far as go/ast is concerned. It is alphabetically
// first, so anything that takes "the first file carrying a leading comment"
// publishes this sentence as the product's description. The convention — a
// package doc opens "Package …" — is what tells the two apart.
package catalog

// Store is the dependency a built handler closes over.
type Store struct{}
