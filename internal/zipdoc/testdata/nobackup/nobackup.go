// Package nobackup compiles fine and declares a product with no Backup line.
// Generation has to fail on it: an unstated backup posture reads as "not backed
// up" to the machinery and as "surely somebody backs it up" to everyone else,
// and the two readings meet at a restore.
//
// Product:    Hanzo Unprotected
// Visibility: public
package nobackup

// Store is here so the package is not empty.
type Store struct{}
