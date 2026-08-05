package zip

import (
	"reflect"
	"strconv"
	"strings"
)

// A ROUTE'S ADDRESS IS A VALUE, AND ITS INPUT ALREADY CARRIES IT.
//
// [registerTyped] binds the segments the router matched onto the op's In —
// /traces/:traceId puts the matched segment in the field that names itself
// traceId (see bindURL, urlFieldName). So by the time a handler runs, the In IS
// the address's parameters, decoded and named.
//
// [Address] is the inverse of that binding, and it exists because the caller
// that has to reconstruct the concrete path — a relay that hands the call on to
// something that speaks HTTP — otherwise spells the address a SECOND time:
//
//	opGet(g, "/traces/:traceId", traceSpans)                   // once, at the route
//	relay(ctx, "GET", root+"/traces/"+in.TraceID, …)           // and again, by hand
//
// Two spellings of one address drift, and the drift is invisible: the hand-built
// path is what gets requested, so a route pattern that no longer agrees with it
// is never consulted and never contradicted. Reading the parameters back out of
// the In through the SAME rule that put them there closes that by construction —
// there is one spelling, the one the route was registered with.
//
// It is deliberately the inverse of bindURL and not a new rule: same field walk
// (wireFields, so promoted embedded fields count), same naming (urlFieldName, so
// `url:` beats `json:`), same case-insensitive match. A parameter the input does
// not name renders empty rather than panicking, which is what bindURL does with
// the mirror-image miss.
//
// pattern is spelled the way the ROUTER spells it — ":name", optionally
// constrained (":project<guid>") or optional (":id?") — because the router's
// spelling is what a registration states.
func Address(pattern string, in any) string {
	if !strings.Contains(pattern, ":") {
		return pattern
	}
	values := addressValues(in)
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		if len(segment) < 2 || segment[0] != ':' {
			continue
		}
		segments[i] = values[strings.ToLower(paramName(segment))]
	}
	return strings.Join(segments, "/")
}

// Template is pattern in the spelling a DOCUMENT writes — ":traceId" becomes
// "{traceId}" — which is the address's public name: the OpenAPI path, the string
// a reference page prints and the key a caller matches an address on.
//
// A constraint and an optional marker are the ROUTER's business (":project<guid>",
// ":id?") and do not survive: they say how a segment is matched, not what it is
// called, and a document that published them would name a parameter no client
// ever writes.
func Template(pattern string) string {
	if !strings.Contains(pattern, ":") {
		return pattern
	}
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		if len(segment) < 2 || segment[0] != ':' {
			continue
		}
		segments[i] = "{" + paramName(segment) + "}"
	}
	return strings.Join(segments, "/")
}

// paramName is the name a router segment declares, without the constraint or the
// optional marker the router reads it with.
func paramName(segment string) string {
	name := segment[1:]
	if cut := strings.IndexAny(name, "<?"); cut >= 0 {
		name = name[:cut]
	}
	return name
}

// addressValues renders every URL-carried field of in as the string the URL
// would carry, keyed by its lowercased URL name so the lookup folds case the way
// bindURL's match does.
func addressValues(in any) map[string]string {
	v := reflect.ValueOf(in)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	out := map[string]string{}
	for _, f := range wireFields(v.Type()) {
		name := urlFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		out[strings.ToLower(name)] = scalarString(v.FieldByIndex(f.Index))
	}
	return out
}

// scalarString is setScalar read backwards: the wire spelling of one value. A
// kind a URL cannot carry renders empty, which is the same silence setScalar
// keeps when it is handed one.
func scalarString(fv reflect.Value) string {
	switch fv.Kind() {
	case reflect.String:
		return fv.String()
	case reflect.Bool:
		return strconv.FormatBool(fv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(fv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(fv.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(fv.Float(), 'g', -1, fv.Type().Bits())
	default:
		return ""
	}
}
