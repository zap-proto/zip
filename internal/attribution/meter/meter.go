// Package meter composes handlers somewhere other than where they are
// registered, which is the shape that decides whether an op's prose is found.
package meter

import (
	"context"

	"github.com/zap-proto/zip"
)

// Paid wraps fn in a closure DECLARED HERE, so the handler's package and the
// registering package disagree — the disagreement [zip.Op].Pkg has to resolve
// in the registrar's favour, since the registrar is the source zipdoc read the
// doc comments from.
func Paid[In, Out any](fn zip.TypedHandler[In, Out]) zip.TypedHandler[In, Out] {
	return func(ctx context.Context, in *In) (*Out, error) {
		return fn(ctx, in)
	}
}
