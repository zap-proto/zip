package zip

import (
	"context"
	"reflect"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// TypedHandler is the generic handler signature: takes an *In, returns
// (*Out, error). zip generates OpenAPI 3.1 spec from the In/Out types
// and registers a Fiber route that unmarshals body → In, runs the
// handler, and marshals Out → JSON response.
type TypedHandler[In, Out any] func(ctx context.Context, in *In) (*Out, error)

// registeredOp is the bookkeeping zip keeps for a typed route — the ONE value
// every projection reads: the REST route, the OpenAPI doc, and the MCP tool all
// come from this. invoke is the transport-agnostic handler core (decode → run →
// result), so a REST request and an MCP tools/call run the exact same fn.
type registeredOp struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	Tags        []string
	InType      reflect.Type
	OutType     reflect.Type
	invoke      func(ctx context.Context, rawIn []byte) (any, error)
}

// Get registers a GET typed handler at path.
func Get[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "GET", path, fn, opts...)
}

// Post registers a POST typed handler at path.
func Post[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "POST", path, fn, opts...)
}

// Put registers a PUT typed handler at path.
func Put[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "PUT", path, fn, opts...)
}

// Patch registers a PATCH typed handler at path.
func Patch[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "PATCH", path, fn, opts...)
}

// Delete registers a DELETE typed handler at path.
func Delete[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "DELETE", path, fn, opts...)
}

// OpOption configures a typed handler registration (OpenAPI metadata).
type OpOption func(*registeredOp)

// WithSummary sets the operation summary in OpenAPI.
func WithSummary(s string) OpOption { return func(op *registeredOp) { op.Summary = s } }

// WithTags sets the operation tags in OpenAPI.
func WithTags(tags ...string) OpOption { return func(op *registeredOp) { op.Tags = tags } }

// WithOperationID sets the operation ID in OpenAPI.
func WithOperationID(id string) OpOption {
	return func(op *registeredOp) { op.OperationID = id }
}

func registerTyped[In, Out any](app *App, method, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	var inZero In
	var outZero Out
	op := &registeredOp{
		Method:  method,
		Path:    path,
		InType:  reflect.TypeOf(inZero),
		OutType: reflect.TypeOf(outZero),
	}
	for _, o := range opts {
		o(op)
	}

	// The op's stable identity, resolved once (after opts) and handed to the
	// authorizer on every invoke — REST and MCP alike.
	meta := Op{Method: op.Method, Path: op.Path, OperationID: opName(op)}

	// The transport-agnostic core: decode raw JSON args → In, validate, authorize,
	// run fn, return Out (or a literal nil for a void result). REST and MCP both
	// call THIS — one handler, many projections. A nil *Out becomes a nil `any`.
	op.invoke = func(ctx context.Context, rawIn []byte) (any, error) {
		var in In
		if len(rawIn) > 0 {
			if err := jsonenc.Unmarshal(rawIn, &in); err != nil {
				return nil, ErrBadRequest("invalid json body: " + err.Error())
			}
		}
		if err := validate(&in); err != nil {
			return nil, ErrBadRequest(err.Error())
		}
		// Authorize the DECODED input — the exact value the handler will bind — so
		// the decision cannot diverge from execution. Runs for REST and MCP alike.
		if auth := app.authorizer; auth != nil {
			if err := auth(ctx, meta, &in); err != nil {
				return nil, err
			}
		}
		out, err := fn(ctx, &in)
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, nil
		}
		return out, nil
	}
	app.ops = append(app.ops, op)

	handler := func(c fiber.Ctx) error {
		var body []byte
		if method != "GET" && method != "HEAD" {
			body = c.Body()
		}
		out, err := op.invoke(c.Context(), body)
		if err != nil {
			return err
		}
		if out == nil {
			c.Status(204)
			return nil
		}
		return c.JSON(out)
	}
	app.fiber.Add([]string{method}, path, handler)
}
