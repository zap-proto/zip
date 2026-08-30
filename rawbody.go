package zip

import (
	"context"
	"sort"
)

// Raw bodies — the bytes that arrived, and not a value decoded out of them.
//
// A typed op's In is a DECODED body: invoke unmarshals the request into it
// before the handler runs (see typed.go), which is exactly what lets one
// handler serve REST, MCP, a command and an op-call over four different
// codecs. For nearly every op that is the whole point. For a few it destroys
// the input:
//
//   - A webhook verifies a signature OVER THE BYTES. Slack's v0 HMAC, GitHub's
//     X-Hub-Signature-256 and Discord's Ed25519 are each computed over the
//     exact payload, so a struct decoded from it is not what was signed and a
//     re-encoding of that struct is a different message with the same meaning
//     — verifying a re-encoding verifies nothing.
//   - A body that is not JSON at all — a PDF, a CSV, an image — has no In to
//     be decoded into.
//   - A body that MIGHT not parse. A typed op answers 400 to one, and a sender
//     that retries on 4xx (every webhook platform does) turns one malformed
//     message into a retry storm. Answering 200 to a body you cannot read is
//     sometimes the correct protocol behaviour, and an op whose first act is to
//     decode cannot express it.
//
// So opacity is DECLARED, on the op, beside its status and its tags — because
// it is part of the CONTRACT every projection publishes and not a detail of one
// handler. [WithRawBody] states it once and the whole registry reads it: the
// route stops decoding, the document publishes the media instead of a schema,
// the CLI sends a file instead of flags, and the two by-name planes leave the
// op out because neither can carry opaque bytes.
//
// What does not change is everything the URL addresses. Path and query still
// bind onto In, `validate:` still runs on it and the [Authorizer] still sees
// the value the handler will act on — for a raw op the URL is the WHOLE of its
// structured input, which is why the document declares those parameters here
// too.

// defaultRawMedia is what a raw body carries when the op names nothing: the
// media type HTTP already means "opaque bytes".
const defaultRawMedia = "application/octet-stream"

// WithRawBody declares that this op's request body is BYTES the handler reads
// itself, and that zip must not decode it. The handler takes them from
// [BodyOf]:
//
//	zip.Post(app, "/v1/hooks/:provider", hook, zip.WithRawBody("application/json"))
//
//	func hook(ctx context.Context, in *HookIn) (*HookOut, error) {
//	    if !verify(zip.BodyOf(ctx), sig) { // over the bytes, not a re-encoding
//	        return nil, zip.ErrUnauthorized("bad signature")
//	    }
//	    ...
//	}
//
// media names what the body carries and is published as the operation's
// requestBody content: application/json for a webhook whose payload is JSON it
// must hold verbatim, application/pdf for an upload, several where the sender
// chooses (GitHub sends either JSON or form encoding). Absent, it is
// application/octet-stream. The set is sorted, so ONE media is the media
// whichever projection names it — the registry and a document read back off it
// pick the same one rather than depending on argument order surviving a JSON
// object.
//
// A raw op is reached over its URL: the REST route, and the CLI, which sends
// the file `--body` names to that route. It is deliberately NOT on the two
// by-name planes — see [registeredOp.callableByName] for why neither an MCP
// arguments object nor a ZAP message can be "the bytes a client sent". Its
// URL-borne fields still bind, are still validated and are still published as
// parameters, so what the op says about its input is unchanged; only the body
// stops being a value.
//
// It is refused on a method that carries no body, at declaration: a raw GET
// would hand its handler nothing at all, and silently reading no bytes is the
// failure this option exists to remove. [OpOption] has no error channel, so the
// refusal is a panic at registration — the same fail-fast [WithStatus] uses.
func WithRawBody(media ...string) OpOption {
	return func(op *registeredOp) {
		if len(media) == 0 {
			media = []string{defaultRawMedia}
		}
		op.RawBody = append([]string(nil), media...)
		sort.Strings(op.RawBody)
	}
}

// raw reports whether this op's body is opaque bytes. The MEDIA is the
// declaration — there is no second flag beside it to disagree with.
func (op *registeredOp) raw() bool { return len(op.RawBody) > 0 }

// bodyMedia is the media a raw body goes on the wire as: the first of the
// sorted set, so a command derived from the registry and one derived from the
// document send the same content type.
func (op *registeredOp) bodyMedia() string {
	if !op.raw() {
		return ""
	}
	return op.RawBody[0]
}

// callableByName reports whether an op can be addressed by NAME — MCP
// tools/call and the op-call plane, the two projections that have no URL and
// carry the whole input as one encoded value.
//
// A raw-body op cannot. An MCP tool's arguments are a JSON object by
// specification, and the call plane's body is a ZAP message; neither is "the
// bytes a client sent", so an op that exists to hold those bytes has nothing
// truthful to receive there. Advertising it would offer a model a tool it
// cannot call correctly and let a sibling service reach a signature check its
// own encoding is guaranteed to fail. A raw op is REST — and the CLI, which
// sends its file over REST — and these two surfaces say so by not carrying it.
func (op *registeredOp) callableByName() bool { return !op.raw() }

// byNameCount is how many ops the by-name planes can actually carry, which is
// what says whether there is a surface to mount at all: an app of nothing but
// webhooks has ops and no tools, and an /mcp that lists none while its log line
// counts them is a surface that describes itself wrongly.
func (a *App) byNameCount() int {
	n := 0
	for _, op := range a.ops {
		if op.callableByName() {
			n++
		}
	}
	return n
}

// bodyKey carries an op's raw body into its handler's context. The bytes come
// from invoke's own rawIn parameter rather than off the request, so each
// projection supplies the body IT carried — the same reason the decoder is a
// parameter and not a property of the op (see typed.go).
type bodyKey struct{}

// withBody binds the bytes an op was invoked with to its handler's context.
func withBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, bodyKey{}, body)
}

// BodyOf returns the request body of an op declared [WithRawBody] — the bytes
// this projection carried, undecoded. It is the typed-handler counterpart of
// [Ctx.Body], read off the context the handler was handed exactly as [CallerOf]
// and [PeerOf] are.
//
// Over REST it is the payload that arrived, byte for byte, with content-coding
// removed the same way [Ctx.Body] removes it: a signature is computed over the
// payload the sender composed, and ONE reading of "the body" serves both that
// verification and whatever parse follows it.
//
// It is nil for an op that declared no raw body. Such an op has its In decoded
// from the body already, and a second way to read the same input is a second
// thing to keep true.
//
// The slice is valid for the life of the request: it aliases the transport's
// read buffer, which is reused for the next request on that connection. To keep
// the bytes, copy them.
func BodyOf(ctx context.Context) []byte {
	b, _ := ctx.Value(bodyKey{}).([]byte)
	return b
}

// rawBodyDecl is a raw-body op's requestBody in the OpenAPI document: every
// media it accepts, each carrying the binary schema — {"type":"string",
// "format":"binary"}, which is what a generator reads as bytes and what Swagger
// UI renders as a file picker.
//
// There is no In schema in it. The In is NOT the body (see [WithRawBody]), so a
// document that $ref'd it would tell every generated SDK to send a JSON object
// this route never decodes. The type is published where it is actually bound
// from — the URL parameters declared beside this.
func rawBodyDecl(op *registeredOp) map[string]any {
	content := make(map[string]any, len(op.RawBody))
	for _, media := range op.RawBody {
		content[media] = map[string]any{
			"schema": map[string]any{"type": "string", "format": "binary"},
		}
	}
	return map[string]any{"required": true, "content": content}
}

// flagFile is the flag kind that names a FILE instead of carrying a value — the
// only spelling a command line has for an opaque body, bytes having no flat
// flag form. It sits alongside the string/integer/number/boolean/json kinds
// every other flag uses.
const flagFile = "file"

// bodyFlagName is the flag a raw-body command sends its body with.
const bodyFlagName = "body"

// bodyFlag is a raw body as a command-line flag: `--body <path>`, whose file
// goes on the wire verbatim under the op's declared media.
//
// Field carries the flag's own name rather than an In field, because the body is
// not a field of the In and nothing reads it as one — [Command.parse] dispatches
// on Type. Leaving it EMPTY would put it in the slot that means "this flag is the
// whole input, as JSON", which is the one thing an opaque body is not.
//
// Both derivations build it HERE: the registry's (App.Commands) and the
// document's (CommandsFromSpec). A command spelled two ways has to be one
// command.
func bodyFlag() Flag {
	return Flag{
		Name:     bodyFlagName,
		Field:    bodyFlagName,
		Type:     flagFile,
		Help:     "path to the file sent as the request body, verbatim",
		Required: true,
	}
}
