package zip

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"
	zapmcp "github.com/zap-proto/mcp"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// MCP — the THIRD projection. The same typed-op registry (a.registry) that produces
// the REST routes and the OpenAPI doc also produces a Model Context Protocol
// tool surface, for FREE: every typed handler (Get/Post[In,Out]) becomes an MCP
// tool whose inputSchema is schemaOf(In) and whose call runs the exact same fn
// (op.invoke). One value (the op), three projections (REST · OpenAPI · MCP).
//
// # ZAP is underneath, not beside
//
// MCP is a protocol on the ZAP wire, the sibling of zap-proto/http — so the door
// here takes a [zapmcp.Frame] and answers one, and knows nothing about any
// transport. [App.MCP] IS a zapmcp.Handler, which means an agent reaches these
// tools over ZAP with no HTTP listener running at all:
//
//	srv := &zapmcp.Server{Network: "unix", Addr: sock, Handler: app.MCP}
//
// HTTP is then an ADAPTER over that door — the /mcp route in installMCP reads a
// JSON-RPC body into a frame, calls App.MCP, and writes the answer back — rather
// than the thing the door is built out of.
//
// It used to be the other way round: the door's own signature took a fiber.Ctx
// and read the caller off it, so a tools/call could not happen unless an HTTP
// request already had. "ZAP-native MCP" was true only in the sense that ZAP
// carried the HTTP request that carried the tool call. Now the frame is the
// unit and the wire is a choice. See TestMCP_ServesOverZapWithNoHTTPListener,
// and TestFiberIsNotInTheDispatchPath for the rule that keeps it that way.
//
// The JSON-RPC 2.0 envelope is zapmcp.Frame's own JSON projection, so the bytes
// an agent reads over HTTP and the bytes it reads over ZAP describe one value
// and cannot drift.

// MCPConfig configures the auto-derived MCP surface.
type MCPConfig struct {
	// Disabled suppresses the /mcp route (MCP is on by default — it's free).
	Disabled bool
	// Path overrides the HTTP mount path (default "/mcp").
	Path string
	// Addr is where this app serves MCP over ZAP, natively — no HTTP listener,
	// no request, no status code, just frames. Empty binds nothing, exactly as
	// [Config.OpsAddr] does: an address is deployment configuration and a
	// deployment states it.
	//
	//	zip.MCPConfig{Addr: zip.SocketPath("flags-mcp")}
	//
	// The door itself is [App.MCP], so a deployment that wants it somewhere this
	// does not reach serves that handler with a zapmcp.Server of its own. This is
	// the convenience, not a second mechanism.
	Addr string
	// Name is the server name reported to MCP clients (default AppName, else "zip").
	Name string
	// Source is the door's PER-CALLER half: the tools that exist because of who
	// is asking, which no build-time projection can hold. Nil — the default —
	// leaves the door exactly the typed-op projection, answered as bytes.
	Source Source
}

// Source is the half of an MCP door that depends on the CALLER.
//
// A typed op is a value known at build time, so zip projects it into a tool once
// and serves the array as bytes. A tenant's own capabilities are rows: they exist
// because of who is asking, so they cannot be projected and cannot be cached
// across callers. Both are tools on ONE door — this is how the second kind gets
// there, without a second registry in front of it.
//
// Tools is called on every tools/list that carries a caller, so an implementation
// answers from data it already has and never fans out. Call runs a name the
// build-time catalogue did not claim; returning an error is reported to the model
// as MCP isError content, exactly like a typed op's error, so a refusal is
// something it can read and react to.
//
// The context is the one the request is being served on — the same value a typed
// op receives — so an implementation reads its caller from there and never from
// the arguments, which the caller wrote.
type Source interface {
	// Tools are the caller's own tool descriptors: {name, description, inputSchema},
	// the same shape [App.MCPTools] projects.
	Tools(ctx context.Context) []map[string]any
	// Call runs one of them with the raw JSON arguments object and returns its
	// JSON-encodable result.
	Call(ctx context.Context, name string, args json.RawMessage) (any, error)
}

// mimeJSON is the boundary content type, spelled once. HIP-0106: JSON at the
// edge, ZAP between services — so this is what an answer a browser or an agent
// reads carries, and it is stated here rather than reached for through the
// router, which is not in the path of everything that writes one.
const mimeJSON = "application/json"

// mcpProtocolVersion is the MCP spec revision zip implements.
const mcpProtocolVersion = "2025-06-18"

// defaultMCPPath is where an app serves its own MCP door unless MCPConfig moves
// it, and therefore where a host forwards a composed tools/call.
const defaultMCPPath = "/mcp"

func (a *App) mcpPath() string {
	if a.cfg.MCP.Path != "" {
		return a.cfg.MCP.Path
	}
	return defaultMCPPath
}

func (a *App) mcpName() string {
	switch {
	case a.cfg.MCP.Name != "":
		return a.cfg.MCP.Name
	case a.cfg.AppName != "":
		return a.cfg.AppName
	default:
		return "zip"
	}
}

// installMCP mounts the JSON-RPC 2.0 MCP endpoint when there is anything to
// expose — this app's own typed ops, or the catalogues of the plugins it
// composed. Called from prepare() alongside installOpenAPIRoutes.
//
// The composed list is rendered ONCE, here, and served as bytes: tools/list is
// the method an MCP client calls constantly, and a host composing a lazy fleet
// must answer it without touching a single child. A pluginTools entry is a
// build-time catalogue, so the answer is a memcpy and the process count is zero.
func (a *App) installMCP() {
	if a.cfg.MCP.Disabled || (len(a.Registry()) == 0 && len(a.tools()) == 0 && !a.hasCaller()) {
		return
	}
	a.mcpOnce.Do(a.renderTools)
	// HTTP is an ADAPTER over the door, not a peer of it: read the JSON-RPC body
	// into a frame, answer it, write the answer back. Everything HTTP knows about
	// this surface is in these nine lines — which is the whole point, and the
	// reason the same door needs no second implementation to be served over ZAP.
	a.control(fiber.MethodPost, a.mcpPath(), func(fc fiber.Ctx) error {
		var f zapmcp.Frame
		if err := json.Unmarshal(fc.Body(), &f); err != nil {
			return mcpSend(fc, &zapmcp.Frame{Kind: zapmcp.Response,
				Err: &zapmcp.Error{Code: zapmcp.CodeParse, Message: "parse error"}})
		}
		ans := a.MCP(callerContext(fc), &f)
		if ans == nil {
			return fc.SendStatus(fiber.StatusAccepted) // a notification
		}
		return mcpSend(fc, ans)
	})
	a.logger.Info("zip mcp", "path", a.mcpPath(), "ops", len(a.Registry()),
		"plugin tools", len(a.tools()), "per-caller", a.hasCaller())
}

// mcpSend writes one frame as the JSON-RPC message an HTTP client reads. The
// rendering is [zapmcp.Frame]'s own, so the bytes an agent sees over HTTP and
// the bytes it sees over ZAP describe the same value and cannot drift.
func mcpSend(fc fiber.Ctx, f *zapmcp.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	fc.Set(fiber.HeaderContentType, mimeJSON)
	return fc.Send(b)
}

// hasCaller reports that this door has a per-caller half at all — a Source of its
// own, or an OPEN plugin to ask. When it has none, tools/list stays the memcpy it
// was and nothing below this line runs.
func (a *App) hasCaller() bool {
	if a.cfg.MCP.Source != nil {
		return true
	}
	return a.openPlugin() != nil
}

// renderTools re-renders the served list from the current ops + catalogues. Called
// by installMCP, and again by any load that lands AFTER it — a host that composes a
// plugin at run time must not serve a list frozen at boot, and the alternative
// (rendering per request) would give back the memcpy that makes tools/list free.
func (a *App) renderTools() {
	list, names := a.composeTools()
	a.mcpList.Store(&list)
	a.mcpNames.Store(&names)
}

// mcpTool is one tool descriptor with its name lifted out, so the composed list
// can be sorted without re-parsing and a plugin's catalogue can be carried
// verbatim — the bytes its own MCPTools() projected, never a re-encoding.
type mcpTool struct {
	name string
	raw  json.RawMessage
}

// composeTools renders the whole build-time tool array: this app's own ops plus
// every plugin catalogue, sorted by name. One order, so a client's list is stable
// and a committed catalogue does not churn on registration order.
//
// It returns the rendered bytes AND the set of names in them, because the
// per-caller half has to know what the build-time half already claims and reading
// that back out of the bytes would re-parse hundreds of schemas per request.
func (a *App) composeTools() (json.RawMessage, map[string]bool) {
	own := a.Registry()
	pluginTools := a.tools()
	all := make([]mcpTool, 0, len(own)+len(pluginTools))
	for _, op := range own {
		b, err := json.Marshal(mcpToolOf(op))
		if err != nil {
			a.logger.Warn("zip mcp: op has no renderable schema", "op", opName(op), "err", err)
			continue
		}
		all = append(all, mcpTool{name: opName(op), raw: b})
	}
	all = append(all, pluginTools...)
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })

	raws := make([]json.RawMessage, len(all))
	names := make(map[string]bool, len(all))
	for i, t := range all {
		raws[i] = t.raw
		names[t.name] = true
	}
	b, err := json.Marshal(raws)
	if err != nil {
		return json.RawMessage("[]"), names
	}
	return b, names
}

// installTools adds one plugin's catalogue to the composed surface, refusing a
// tool name another plugin already holds. plugMu must be held.
//
// It is the Load-time analogue of [App.claim] and of the document's unique
// operation ids: a tool NAME is dispatch, so two owners make it unroutable. A
// composition that would serve one name from two places fails at boot with both
// names, rather than silently forwarding every call to whichever loaded first.
func (a *App) installTools(p *plugin, tools []mcpTool) error {
	// Refused BEFORE anything is recorded, so a rejected Load leaves the door
	// exactly as it found it.
	if p.spec.Open && a.open != nil {
		return fmt.Errorf("plugin %q is already the open one — a name no catalogue claims has one owner, and two would make it ambiguous", a.open.name)
	}
	if len(tools) > 0 {
		if a.toolOwners == nil {
			a.toolOwners = map[string]*plugin{}
		}
		for i, t := range tools {
			if held, dup := a.toolOwners[t.name]; dup {
				for _, undo := range tools[:i] {
					delete(a.toolOwners, undo.name)
				}
				return fmt.Errorf("tool %q is already served by plugin %q", t.name, held.name)
			}
			a.toolOwners[t.name] = p
		}
		a.pluginTools = append(a.pluginTools, tools...)
		// The door is already up (this Load came after Listen): re-render, so a
		// plugin composed at run time is on the list rather than invisible until a
		// restart.
		if a.mcpList.Load() != nil {
			a.renderTools()
		}
	}
	if p.spec.Open {
		a.open = p
	}
	return nil
}

// parseTools reads a plugin's catalogue — the JSON array [App.App.MCPTools]
// projects, captured at build time — and lifts each tool's name out for the
// owner index. A malformed catalogue is an error at Load, not a surprise at the
// first tools/list.
func parseTools(name string, b []byte) ([]mcpTool, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(b, &raws); err != nil {
		return nil, fmt.Errorf("plugin %q: Tools is not a JSON array of tool descriptors: %w", name, err)
	}
	out := make([]mcpTool, 0, len(raws))
	for _, raw := range raws {
		var hdr struct{ Name string }
		if err := json.Unmarshal(raw, &hdr); err != nil || hdr.Name == "" {
			return nil, fmt.Errorf("plugin %q: a tool descriptor has no name", name)
		}
		out = append(out, mcpTool{name: hdr.Name, raw: raw})
	}
	return out, nil
}

// MCP answers one MCP message. It is this app's tool door as a VALUE — a frame
// in, a frame out, nothing underneath it — which is the whole of why MCP can be
// served over ZAP natively:
//
//	srv := &zapmcp.Server{Network: "unix", Addr: sock, Handler: app.MCP}
//
// HTTP is then an ADAPTER over this, not a peer of it: installMCP's route reads
// a JSON-RPC body into a frame, calls this, and writes the answer back. Before,
// the door's own signature took a fiber.Ctx, so an agent could not reach a tool
// without an HTTP request existing first — which put the transport underneath
// the protocol instead of the other way round.
//
// The caller is read from ctx ([CallerOf]), never from the frame: over HTTP the
// adapter binds the gateway's assertion onto the context, and over ZAP a caller
// states one with [WithCaller]. The frame's own Subject says who SIGNED it,
// which is a different question and not one this door answers.
//
// A nil answer means there is nothing to say — a notification. [zapmcp.Server]
// writes nothing; the HTTP adapter answers 202.
func (a *App) MCP(ctx context.Context, f *zapmcp.Frame) *zapmcp.Frame {
	// The tool list is a projection of the registry, like the OpenAPI document,
	// and belongs to whichever door asks for it first. Rendering it in the HTTP
	// installer meant an app served ONLY over ZAP answered tools/list with an
	// empty array — the door worked and had nothing to say, which is the subtlest
	// possible way for an inversion to be incomplete.
	a.mcpOnce.Do(a.renderTools)
	switch f.Method {
	case "initialize":
		return f.Answer(mcpJSON(map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": a.mcpName(), "version": a.cfg.OpenAPI.Version},
		}))
	case "tools/list":
		return f.Answer(mcpJSON(map[string]any{"tools": a.list(ctx)}))
	case "tools/call":
		return a.tool(ctx, f)
	case "ping":
		return f.Answer([]byte("{}"))
	}
	// notifications/* carry no id and expect no result.
	if f.Kind == zapmcp.Notify {
		return nil
	}
	return f.Fail(zapmcp.CodeMethod, "method not found: "+f.Method)
}

// mcpJSON renders a result. An unrenderable one is a programming error in this
// file — every value here is a literal map — so it answers null rather than
// growing an error path no caller could act on.
func mcpJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// list answers one tools/list: the build-time array, plus the tools that exist
// only for THIS caller.
//
// The build-time half stays what it was — the pre-rendered bytes, verbatim, no
// marshal of 451 schemas and no plugin touched to produce it. A door with no
// per-caller half returns exactly those bytes, so the memcpy that makes the
// most-called MCP method free is untouched by this file.
//
// The per-caller half runs only when the call NAMES a caller, which is the
// whole of why it can be afforded: a tenant's own tools cannot be known without
// asking, and there is nothing to ask about when nobody is asking. An anonymous
// probe therefore still costs a memcpy and starts no child.
//
// The caller comes off the ctx rather than off a request header, so the answer
// is the same over ZAP as over HTTP — a door that read fc.Get(HeaderOrg) could
// only ever have a per-caller half on the transport that has headers.
func (a *App) list(ctx context.Context) json.RawMessage {
	fleet := json.RawMessage("[]")
	if cur := a.mcpList.Load(); cur != nil {
		fleet = *cur
	}
	if !a.hasCaller() || CallerOf(ctx).Org == "" {
		return fleet
	}
	mine := a.source(ctx)
	if len(mine) == 0 {
		return fleet
	}
	// Appended, not merged: the fleet's half is byte-identical to the artifact
	// every projection agrees on, and re-sorting the union would re-encode it.
	// Each half is sorted, so the answer is stable either way.
	sort.Slice(mine, func(i, j int) bool { return mine[i].name < mine[j].name })
	raws := make([]json.RawMessage, 0, len(mine))
	for _, t := range mine {
		raws = append(raws, t.raw)
	}
	tail, err := json.Marshal(raws)
	if err != nil || len(fleet) < 2 || len(tail) < 2 {
		return fleet
	}
	if string(fleet) == "[]" {
		return tail
	}
	out := make([]byte, 0, len(fleet)+len(tail))
	out = append(out, fleet[:len(fleet)-1]...) // drop the closing ]
	out = append(out, ',')
	out = append(out, tail[1:]...) // drop the opening [
	return out
}

// source is every tool that exists because of WHO is asking: this app's own
// [Source], and every OPEN plugin's answer to the same question. A name the
// build-time catalogue already claims is dropped — one name is one dispatch,
// and the projected op is the one the whole fleet agreed on.
func (a *App) source(ctx context.Context) []mcpTool {
	// fleet is READ-ONLY and shared by every call; mine is this one's own.
	// Writing the caller's names into the fleet set would be two bugs at once,
	// and both are silent: one tenant's tool name would be "already claimed" for
	// every tenant after it, and two concurrent lists would be concurrent writes
	// to one map — a runtime FATAL no recover() can catch, because tools/list is
	// exactly the method that arrives in parallel.
	var fleet map[string]bool
	if cur := a.mcpNames.Load(); cur != nil {
		fleet = *cur
	}
	mine := map[string]bool{}
	var out []mcpTool
	keep := func(name string, raw json.RawMessage) {
		if name == "" || fleet[name] || mine[name] {
			return
		}
		mine[name] = true
		out = append(out, mcpTool{name: name, raw: raw})
	}
	if src := a.cfg.MCP.Source; src != nil {
		for _, t := range src.Tools(ctx) {
			name, _ := t["name"].(string)
			raw, err := json.Marshal(t)
			if err != nil {
				a.logger.Warn("zip mcp: caller tool has no renderable schema", "tool", name, "err", err)
				continue
			}
			keep(name, raw)
		}
	}
	if p := a.openPlugin(); p != nil {
		for _, t := range a.ask(ctx, p) {
			keep(t.name, t.raw)
		}
	}
	return out
}

// openPlugin is the plugin that declared an OPEN catalogue, or nil. At most one:
// see installTools.
func (a *App) openPlugin() *plugin {
	for _, h := range a.hosts() {
		h.plugMu.Lock()
		p := h.open
		h.plugMu.Unlock()
		if p != nil {
			return p
		}
	}
	return nil
}

// ask puts a tools/list to one open plugin and lifts the tools out of its
// reply. The child answers as itself — its own registry, its own [Source], its
// own view of the caller — so the host learns the tenant's tools without holding
// the tenant's data.
//
// It builds the question rather than forwarding the one that arrived, which is
// what lets a host with no HTTP request in hand (an agent reaching it over ZAP)
// still compose an open plugin's surface. It also means the child receives the
// caller's IDENTITY and nothing else, through the one function that forwards it
// — where forwarding the inbound request verbatim handed the child every header
// the edge happened to carry.
//
// A child that fails contributes nothing. A tools/list is a question with a
// partial answer available, and blanking the fleet's whole surface because one
// plugin is down is a far worse answer than a shorter list.
func (a *App) ask(ctx context.Context, p *plugin) []mcpTool {
	ans := a.relay(ctx, &zapmcp.Frame{
		Kind: zapmcp.Request, ID: "1", Method: "tools/list", Params: []byte("{}"),
	}, p)
	if ans == nil || ans.Err != nil {
		a.logger.Warn("zip mcp: open plugin did not answer tools/list", "plugin", p.name, "err", ans.Err)
		return nil
	}
	var env struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(ans.Result, &env); err != nil {
		a.logger.Warn("zip mcp: open plugin sent an unreadable tools/list", "plugin", p.name, "err", err)
		return nil
	}
	out := make([]mcpTool, 0, len(env.Tools))
	for _, raw := range env.Tools {
		var named struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &named); err != nil || named.Name == "" {
			continue
		}
		out = append(out, mcpTool{name: named.Name, raw: raw})
	}
	return out
}

// MCPTools is the app's full MCP tool surface, projected from the typed-op
// registry — the tool-list counterpart of [App.OpenAPISpec], and the whole of
// what a plugin has to do to be an MCP server: nothing. Every typed op is one
// tool, named by its operation id, described by its doc comment, with the same
// JSON Schema the OpenAPI document carries.
//
// Serving it is already handled: the /mcp route rides every transport the app
// Listens on. Read it directly when a host wants a plugin's tools in-process —
// composing several plugins' surfaces, filtering them, or asserting them in a
// test — without a round trip.
func (a *App) MCPTools() []map[string]any { return a.mcpTools() }

// mcpTools projects every typed op into an MCP tool descriptor. The inputSchema
// is the SAME schemaOfDoc(InType) the OpenAPI doc uses, carrying the SAME prose
// cmd/zipdoc lifted from the handler — one comment, three surfaces (spec, CLI
// help, tool list). A model picking a tool from a list is doing what a human
// reading the spec does and needs the same words to do it; reading only
// WithSummary here left every zipdoc'd op with a nameless schema and an empty
// description. WithSummary remains the fallback for a package the generator has
// not run over, so an undocumented op still names itself.
// Sorted by name, because this list is SERIALIZED — a host embeds it as a
// build-time catalogue — and an artifact ordered by registration churns on an
// edit that changed nothing a client can see.
func (a *App) mcpTools() []map[string]any {
	reg := a.Registry()
	tools := make([]map[string]any, 0, len(reg))
	for _, op := range reg {
		tools = append(tools, mcpToolOf(op))
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i]["name"].(string) < tools[j]["name"].(string)
	})
	return tools
}

// mcpToolOf is the ONE op→tool descriptor. Both the in-process projection
// (MCPTools) and the composed list read it, so a host's catalogue and a plugin's
// own /mcp can never describe one op two ways.
func mcpToolOf(op *registeredOp) map[string]any {
	doc, hasDoc := docFor(op.Method, op.Path)
	desc := op.Summary
	if hasDoc && doc.Description != "" {
		desc = doc.Description
	}
	return map[string]any{
		"name":        opName(op),
		"description": desc,
		"inputSchema": rootSchemaOf(op.InType, docFields(hasDoc, doc)),
	}
}

// tool runs a tools/call: find the op by name, invoke the SAME handler core the
// REST route uses, and return its JSON result as MCP text content. A handler
// error is reported as MCP isError content (not a JSON-RPC error), per the spec
// — the model sees the failure and can react.
func (a *App) tool(ctx context.Context, f *zapmcp.Frame) *zapmcp.Frame {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(f.Params, &params)

	op := a.opByName(params.Name)
	if op == nil || op.invoke == nil {
		// Not ours — a composed plugin may own it. Only a CALL wakes a child:
		// tools/list answered from the catalogue and touched nothing.
		if p := a.toolOwner(params.Name); p != nil {
			return a.relay(ctx, f, p)
		}
		// Still nobody's: the name may be one that exists only for this caller,
		// which no catalogue could have claimed. It goes to whoever offered the
		// per-caller half of the list — this app's own Source, or the open plugin.
		// Neither can be wrong about it: the same code that named the tool runs it.
		if src := a.cfg.MCP.Source; src != nil {
			out, err := src.Call(ctx, params.Name, params.Arguments)
			return a.answer(f, out, err)
		}
		if p := a.openPlugin(); p != nil {
			return a.relay(ctx, f, p)
		}
		return f.Fail(zapmcp.CodeParams, "unknown tool: "+params.Name)
	}

	// No URL over MCP: a tools/call carries every argument in its JSON arguments
	// object, so the body IS the whole input — neither query nor path binds.
	// The header reader comes off the ctx, so it answers honestly on a transport
	// that has a request behind it and nothing on one that does not.
	out, err := op.invoke(ctx, jsonenc.Unmarshal, params.Arguments, nil, nil, headerOf(ctx))
	return a.answer(f, out, err)
}

// answer renders one tool result. A handler error is MCP isError content and not
// a JSON-RPC error, per the spec — the model sees the failure and can react. ONE
// renderer, so a typed op, a [Source] and a relayed plugin answer the same shape.
func (a *App) answer(f *zapmcp.Frame, out any, err error) *zapmcp.Frame {
	if err != nil {
		return f.Answer(mcpJSON(map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}))
	}
	text := "null"
	if out != nil {
		if b, mErr := jsonenc.Marshal(out); mErr == nil {
			text = string(b)
		}
	}
	return f.Answer(mcpJSON(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}))
}

// toolOwner is the plugin that declared name in its catalogue, or nil.
func (a *App) toolOwner(name string) *plugin {
	for _, h := range a.hosts() {
		h.plugMu.Lock()
		p := h.toolOwners[name]
		h.plugMu.Unlock()
		if p != nil {
			return p
		}
	}
	return nil
}

// tools is every composed plugin's build-time catalogue, gathered by walking.
//
// A plugin used to install its catalogue onto whatever host loaded it, so one
// map answered. A plugin is now a DEFINITION, so its catalogue lives with it and
// the host finds it the way it finds everything else about its composition. Same
// move the op registry made, and the same one Plugins/Reload/Unload made: one
// walk, and no projection keeps a private copy of what the program already says.
func (a *App) tools() []mcpTool {
	var out []mcpTool
	for _, h := range a.hosts() {
		h.plugMu.Lock()
		out = append(out, h.pluginTools...)
		h.plugMu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// relay hands the message to the plugin that owns the tool, at that plugin's own
// MCP door, over the transport it was composed on — for a Load'ed child, ZAP on
// its private unix socket. The plugin's own registry answers, so the host can
// only ever NAME a tool, never invoke one the child did not declare: a stale
// catalogue yields the child's own refusal, not a wrong call.
//
// This is the ONLY trigger that starts a process, and it starts exactly one:
// p.target() is the same single-flighted lazy path a prefix request takes.
//
// The message is RENDERED for the hop rather than the inbound request being
// passed through, which is what makes a relay possible at all from a door with
// no HTTP request behind it. The caller's identity rides along through
// [forwardIdentity] — the one function that forwards it — and nothing else does.
//
// A hop failure is MCP isError content rather than a transport error, per the
// spec: the model sees "this tool is not available right now" and can react,
// where a 503 body is a failure it cannot interpret.
func (a *App) relay(ctx context.Context, f *zapmcp.Frame, p *plugin) *zapmcp.Frame {
	client, host := p.target()
	body, err := json.Marshal(f)
	if err != nil {
		return a.answer(f, nil, err)
	}
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType(mimeJSON)
	req.SetBody(body)
	forwardIdentity(ctx, req)
	if err := forward(req, resp, client, host, p.spec.mcpPath(), "mcp "+p.name); err != nil {
		return a.answer(f, nil, err)
	}
	var ans zapmcp.Frame
	if err := json.Unmarshal(resp.Body(), &ans); err != nil {
		return a.answer(f, nil, fmt.Errorf("mcp %s: unreadable answer: %w", p.name, err))
	}
	// The child answered its own copy of the message; this hop owns the
	// correlation, so the reply is stamped with what the CALLER sent.
	ans.Kind, ans.Session, ans.Seq, ans.ID = zapmcp.Response, f.Session, f.Seq, f.ID
	return &ans
}

func (a *App) opByName(name string) *registeredOp {
	for _, op := range a.Registry() {
		if opName(op) == name {
			return op
		}
	}
	return nil
}

// opName is the stable tool/operation id: the explicit OperationID, else the
// method+path default (shared with OpenAPI so the two surfaces agree).
func opName(op *registeredOp) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	return ID(op.Method, op.Path)
}

// serveMCP brings the ZAP-native MCP listener up when this process owns an
// address for it. Called from Listen alongside the ops and peer listeners, so a
// plugin main ends with one verb and the app's own declaration decides whether
// there is another listener.
//
// It is [zapmcp.Server] wrapped around [App.MCP] and nothing else. That one line
// is the whole of "MCP rides ZAP natively": there is no adapter here because
// there is nothing to adapt — the door already speaks frames.
func (a *App) serveMCP() {
	if a.sibling || a.cfg.MCP.Disabled {
		return // a sibling serves no siblings
	}
	addr := strings.TrimSpace(a.cfg.MCP.Addr)
	if addr == "" {
		return
	}
	srv := &zapmcp.Server{Network: networkOf(addr), Addr: addr, Handler: a.MCP}
	a.mcpMu.Lock()
	if a.mcpSrv != nil {
		a.mcpMu.Unlock()
		return // Listen may be called twice
	}
	a.mcpSrv = srv
	a.mcpMu.Unlock()
	a.logger.Info("zip mcp listening", "transport", "zap", "addr", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			a.logger.Error("zip mcp listener stopped", "addr", addr, "err", err)
		}
	}()
}

// closeMCP stops the ZAP MCP listener. Called from Shutdown alongside
// closeServers.
func (a *App) closeMCP() {
	a.mcpMu.Lock()
	srv := a.mcpSrv
	a.mcpSrv = nil
	a.mcpMu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}
