// Package kinds answers with zip's kinds: an event stream, a union, an
// either-or and a relay, each of whose wire shapes is a type argument or a
// OneOf result.
package kinds

import (
	"context"

	"github.com/zap-proto/zip"
)

// Chunk is one event of a completion.
type Chunk struct {
	// Text is the next piece of the completion.
	Text string `json:"text"`
}

// Done is a completion answered whole.
type Done struct {
	// Answer is the whole completion.
	Answer string `json:"answer"`
}

// Upstream is the upstream's answer.
type Upstream struct {
	// Model is the model the upstream ran.
	Model string `json:"model"`
}

// Event is one ingested event.
type Event struct {
	// Kind names the event.
	Kind string `json:"kind"`
}

// Batch is several events at once.
type Batch struct {
	// Events are the events, oldest first.
	Events []Event `json:"events"`
}

// Ingest is one event or a batch.
type Ingest struct{ one *Event }

// OneOf declares the alternatives.
func (Ingest) OneOf() (Event, Batch) { return Event{}, Batch{} }

// Prompt asks for a completion.
type Prompt struct {
	// Stream asks for events rather than one answer.
	Stream bool `json:"stream"`
}

// Stream streams a completion.
func Stream(ctx context.Context, in *Prompt) (*zip.Sse[Chunk], error) { return &zip.Sse[Chunk]{}, nil }

// Either answers whole or streams, as asked.
func Either(ctx context.Context, in *Prompt) (*zip.Or[Done, zip.Sse[Chunk]], error) {
	return &zip.Or[Done, zip.Sse[Chunk]]{}, nil
}

// Relay passes the upstream's answer through as sent.
func Relay(ctx context.Context, in *Prompt) (*zip.Verbatim[Upstream], error) {
	return &zip.Verbatim[Upstream]{}, nil
}

// Take ingests what was sent.
func Take(ctx context.Context, in *Ingest) (*struct{}, error) { return nil, nil }

// Register declares the ops.
func Register(app *zip.App) {
	app.Post("/v1/stream", Stream)
	app.Post("/v1/either", Either)
	app.Post("/v1/relay", Relay)
	app.Post("/v1/ingest", Take)
}
