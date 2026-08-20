package zip

import (
	"context"
	"errors"
	"reflect"
)

// Op is the stable identity of a registered typed handler, handed to an
// Authorizer so the decision can key on the operation as well as the input.
// OperationID is the resolved id the OpenAPI document and the MCP tool surface
// share — the explicit WithOperationID, else the method+path default.
type Op struct {
	Method      string
	Path        string
	OperationID string
}

// Effect is the three-valued outcome of authorizing an op: run it, refuse it,
// or hold it for a human. It is the whole vocabulary — there is no fourth answer
// — so every projection renders exactly these three and cannot invent a state
// the others do not know.
type Effect uint8

const (
	Allow   Effect = iota // run the handler; the op proceeds
	Deny                  // refuse before the handler runs; render 403 with Rule/Reason
	Approve               // hold the op; the handler does NOT run, and every projection renders it held
)

// Decision is what the authorizer answers with. Effect is the outcome; Rule
// names which rule reached it (a stable token like "toll.standing"), and Reason
// is the caller-facing sentence — the 403 body over REST, the held record's
// text, the line the CLI prints. The two strings are meaningful on a Deny or an
// Approve and ignored on an Allow, which carries no explanation because nothing
// was withheld.
type Decision struct {
	Effect Effect
	Rule   string
	Reason string
}

// Grant is the allow decision. It carries no rule or reason because an op that
// runs withheld nothing to explain.
func Grant() Decision { return Decision{Effect: Allow} }

// Deny_ refuses an op. rule names the rule; reason is the sentence the caller
// reads. (The trailing underscore keeps the bare name for the Effect constant,
// so `d.Effect == Deny` and `Deny_(rule, reason)` never shadow one another.)
func Deny_(rule, reason string) Decision {
	return Decision{Effect: Deny, Rule: rule, Reason: reason}
}

// Approve_ holds an op for a human: the handler does not run, and the caller
// receives a held record naming the rule that asked for sign-off and why.
func Approve_(rule, reason string) Decision {
	return Decision{Effect: Approve, Rule: rule, Reason: reason}
}

// Held is the ONE word for an op a Decision stopped and nobody has answered yet.
// One spelling, so a caller — and a generated SDK's Result<T> = Done<T> | Approval
// — tells a held op from a finished one without guessing.
const Held = "held"

// Approval is the body a projection renders when a Decision approves an op, and
// the record that op yields: the handler did NOT run, Status is Held, Rule names
// which rule asked for sign-off, and Reason is what a human reads before granting
// it. It carries no id because minting and tracking an approval is the authority's
// job (IAM/cloud), not the framework's — zip states that the op is held and why,
// once, in a shape every projection renders identically. Over REST and the call
// plane it rides a 202; over MCP and the CLI it rides the value, and to a Go caller
// it rides the error lane — so no projection can mistake a held op for a success.
type Approval struct {
	Status string `json:"status"` // always Held
	Rule   string `json:"rule"`
	Reason string `json:"reason,omitempty"`
}

// Error lets an Approval travel the lane every Go caller already reads. A held op
// has no Out and never will have one — the handler did not run — so a projection
// that must answer (*Out, error) has exactly one honest shape for it, and this is
// it. It is not a failure: nothing broke, and the next step is a person.
func (a *Approval) Error() string {
	if a.Reason == "" {
		return "held for approval: " + a.Rule
	}
	return "held for approval: " + a.Rule + ": " + a.Reason
}

// HeldOf reads the Approval back out of an error — the ONE way a caller asks "was
// this held?", on any projection, with no status matching and no string parsing.
func HeldOf(err error) (*Approval, bool) {
	var a *Approval
	return a, errors.As(err, &a)
}

// held builds the body from a Decision that approved.
func held(d Decision) *Approval {
	return &Approval{Status: Held, Rule: d.Rule, Reason: d.Reason}
}

// denyError turns a Deny into the error the projections already render: a 403
// carrying the Rule as its code and the Reason as its message. Reusing the error
// lane is what makes a deny fan out to REST, MCP, the call plane and the CLI with
// no per-projection branch — refusal has exactly one shape.
func denyError(d Decision) error {
	return &HTTPError{Status: 403, Code: d.Rule, Msg: d.Reason}
}

// Authorizer authorizes a decoded, validated typed request at the op-invoke
// seam — the ONE point every projection of a typed handler funnels through. It
// runs after the request is decoded into the op's typed In and validated, and
// BEFORE the handler runs, over REST and MCP alike, so the value it authorizes
// is exactly the value the handler will act on: there is no second parse of the
// body for it to diverge from. in is the *In the handler will receive.
//
// It answers a Decision — Grant, Deny_ or Approve_ — which every projection
// renders the same way: allow runs the handler, deny is a 403 carrying Rule and
// Reason, approve parks the op and renders it pending without running it. The
// error return is ONLY for a genuine internal failure of the check itself (the
// authority is unreachable, say); it is not how a refusal is spelled — a refusal
// is Deny_. A non-nil error aborts the op and is the response, so return a
// zip.Err* for a clean status.
type Authorizer func(ctx context.Context, op Op, in any) (Decision, error)

// Authorize installs fn as the op-invoke authorization hook. It is the op-level
// counterpart to Use: Use wraps the whole request with transport middleware,
// which for a body request sees only the raw bytes; Authorize runs one decision
// on the DECODED typed input of every op, REST and MCP alike — the seam a
// mounted subsystem gates on so the value it authorizes is the value the handler
// binds. Call once while mounting, before Listen. A nil fn clears it (every
// decoded request then runs unauthorized).
func (a *App) Authorize(fn Authorizer) { a.authorizer = fn }

// Decide lifts a check written before the Decision hook — one that returned nil
// to allow and an error to refuse — into an Authorizer, in ONE place, so such a
// gate upgrades by wrapping rather than rewriting. A nil error grants; an
// *HTTPError below 500 denies, carrying its code and message as Rule and Reason;
// a 5xx *HTTPError or any other error is a genuine internal failure and passes
// through unchanged. It cannot produce an Approve — parking an op is a new answer
// only a Decision-returning check can give.
func Decide(check func(ctx context.Context, op Op, in any) error) Authorizer {
	return func(ctx context.Context, op Op, in any) (Decision, error) {
		err := check(ctx, op, in)
		if err == nil {
			return Grant(), nil
		}
		var he *HTTPError
		if errors.As(err, &he) && he.Status < 500 {
			return Deny_(he.Code, he.Msg), nil
		}
		return Decision{}, err
	}
}

// heldType is the reflect.Type the OpenAPI projection registers so a gated
// service publishes the held body beside every op's success response.
var heldType = reflect.TypeOf(Approval{})

// Build constructs and installs a generation without starting a listener: the
// walk runs, every validation runs, the projections are rendered, and the
// router goes live — exactly what [App.Listen] does minus the sockets.
//
// It RETURNS THE VERDICT, which is the whole reason it replaced Prepare. A
// composition can be invalid — two definitions claiming one address, a cycle —
// and the old Prepare returned nothing, so the only way to learn was to start
// a server. Now a codegen step, a test or a wiring file's own main can ask
// whether the program it just wrote is a program, and get every conflict at
// once with both claimants named.
//
// Idempotent for the projections (they render once) and monotonic for the
// freeze. Listen calls it; there is no other way to build.
func (a *App) Build() error {
	// VALIDATE FIRST. prepare() renders the projections, and a projection of a
	// program that does not compose now panics — so validating after it would
	// turn every build error into a panic escaping the one function whose whole
	// job is to return it.
	if err := verify(a); err != nil {
		return err
	}
	a.prepare()
	a.buildMu.Lock()
	g, err := a.build()
	if err == nil {
		a.install(g)
	}
	a.buildMu.Unlock()
	return err
}
