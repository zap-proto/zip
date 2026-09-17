// Package jsonenc is zip's single JSON entry point.
//
// All JSON marshalling in zip — c.JSON(), c.Bind().Body(), typed.go's
// generic In/Out handlers, module.go's extension envelope — goes
// through Marshal / Unmarshal here. There is no other JSON path.
//
// It is stdlib encoding/json and nothing else. Since Go 1.27 that API runs on
// the v2 engine with v1 semantics, so the speed arrives with the toolchain and
// the wire stays where it is. Calling encoding/json/v2 directly would not: v2
// keeps a zero number or false under omitempty, writes a nil slice as [] and a
// nil map as {}, and matches field names case-sensitively. This package used to
// select v2 on the goexperiment.jsonv2 build tag, which Go 1.27 sets by default,
// so moving a module's go directive to 1.27 changed what every response said.
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
