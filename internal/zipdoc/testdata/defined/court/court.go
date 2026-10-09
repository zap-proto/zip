// Package court declares a record another package defines a type over.
package court

// Record is one court record.
type Record struct {
	// Court is the court that holds the record.
	Court string `json:"court"`
}
