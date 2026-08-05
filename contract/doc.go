// Package contract holds one proof: what a zip app exports is what the
// collector decodes.
//
// # Why it is a module of its own
//
// The proof needs hanzoai/o11y, and zip must not require it. Two reasons, both
// measured rather than assumed. It is circular — o11y already requires zip, so
// zip requiring o11y leaves neither end able to release without the other. And
// it is expensive for everyone else: adding o11y to zip's own module takes its
// go.sum from 147 lines to about 720 and its module graph past a thousand
// modules, a cost paid by every program built on zip in order to run a test that
// belongs to zip alone.
//
// A module here pays neither. Nothing imports this package, `go build ./...` and
// `go test ./...` in zip do not descend into it, and no consumer of zip inherits
// a line of it. CI runs it as its own step, which is where the cost belongs.
//
// # What it proves
//
// The two ends re-declare one contract: zip's internal/o11y package and the
// collector's pkg/zapreceiver and pkg/zaplogreceiver each state the batch shapes
// in their own types, so that a collector can be deployed without rebuilding
// every service that reports to it. Two declarations need something holding them
// together, and transcribing field names into a test only proves that a
// transcription matches itself.
//
// So this test runs the real receivers, has a real zip app answer a real
// request, and reads what the receivers decoded. A field renamed on either side
// fails it, and a field zip invents fails it too — it arrives as nothing.
package contract
