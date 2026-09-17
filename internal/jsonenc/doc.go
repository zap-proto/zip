// Package jsonenc is zip's single JSON entry point.
//
// All JSON marshalling in zip — c.JSON(), c.Bind().Body(), typed.go's
// generic In/Out handlers, module.go's extension envelope — goes
// through Marshal / Unmarshal here. There is no other JSON path.
//
// It is encoding/json/v2, called with the wire stated as options rather than
// taken from whatever the toolchain defaults to. That distinction is the whole
// point of this package: v2 answers differently from v1 in several places — it
// keeps a zero number or false under omitempty, writes a nil slice as [] and a
// nil map as {}, leaves `<` unescaped, matches field names case-sensitively,
// and does not sort a map's keys. This package once selected v2 on the
// goexperiment.jsonv2 build tag, which Go 1.27 sets by default, so moving a
// module to 1.27 silently changed what every response said.
//
// So the options say v1's semantics, plus sorted keys, and every one of the
// remaining differences is a decision to make here, once, in view of the
// clients — never a default to inherit. What v2 gives in return is its API:
// Write and Read stream straight to and from the connection with no second
// copy, options are per call, and `omitzero` says what omitempty could not.
//
// Per HIP-0106 "Wire protocol stack": JSON is the boundary format
// (ingress → gateway → subsystem handler). Inter-subsystem calls use
// ZAP-typed Go values. JSON marshalling happens at most once per
// request, at the subsystem handler boundary, through THIS package.
//
// Brand policy: no third-party JSON library is allowed. Stdlib only.
// goccy/go-json, sonic, jsoniter, and friends are NOT permitted in
// the Hanzo Go stack — see HIP-0106 canonical Hanzo Go stack.
package jsonenc
