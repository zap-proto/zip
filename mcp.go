package zip

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// MCP — the THIRD projection. The same typed-op registry (a.registry) that produces
// the REST routes and the OpenAPI doc also produces a Model Context Protocol
// tool surface, for FREE: every typed handler (Get/Post[In,Out]) becomes an MCP
// tool whose inputSchema is schemaOf(In) and whose call runs the exact same fn
// (op.invoke). One value (the op), three projections (REST · OpenAPI · MCP).
//
// Because /mcp is an ordinary route on the app, it is served over EVERY
// transport the app Listens on — so ZAP-native MCP is automatic: an agent
// speaking ZAP gets the tool surface with zero extra wiring. Enabled by default.

// MCPConfig configures the auto-derived MCP surface.
type MCPConfig struct {
	// Disabled suppresses the /mcp route (MCP is on by default — it's free).
	Disabled bool
	// Path overrides the mount path (default "/mcp").
	Path string
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
	if a.cfg.MCP.Disabled || (len(a.registry) == 0 && len(a.pluginTools) == 0 && !a.hasCaller()) {
		return
	}
	a.renderTools()
	a.control(fiber.MethodPost, a.mcpPath(), a.handleMCP)
	a.logger.Info("zip mcp", "path", a.mcpPath(), "ops", len(a.registry),
		"plugin tools", len(a.pluginTools), "per-caller", a.hasCaller())
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
	all := make([]mcpTool, 0, len(a.registry)+len(a.pluginTools))
	for _, op := range a.registry {
		b, err := json.Marshal(mcpToolOf(op))
		if err != nil {
			a.logger.Warn("zip mcp: op has no renderable schema", "op", opName(op), "err", err)
			continue
		}
		all = append(all, mcpTool{name: opName(op), raw: b})
	}
	all = append(all, a.pluginTools...)
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

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// handleMCP dispatches one JSON-RPC 2.0 MCP message.
func (a *App) handleMCP(fc fiber.Ctx) error {
	var req mcpRequest
	if err := json.Unmarshal(fc.Body(), &req); err != nil {
		return fc.JSON(mcpErr(nil, -32700, "parse error"))
	}
	switch req.Method {
	case "initialize":
		return fc.JSON(mcpResult(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": a.mcpName(), "version": a.cfg.OpenAPI.Version},
		}))
	case "tools/list":
		return fc.JSON(mcpResult(req.ID, map[string]any{"tools": a.listTools(fc, req)}))
	case "tools/call":
		return a.mcpCall(fc, req)
	case "ping":
		return fc.JSON(mcpResult(req.ID, map[string]any{}))
	default:
		// notifications/* carry no id and expect no result — ack with 202.
		if len(req.ID) == 0 {
			return fc.SendStatus(fiber.StatusAccepted)
		}
		return fc.JSON(mcpErr(req.ID, -32601, "method not found: "+req.Method))
	}
}

// listTools answers one tools/list: the build-time array, plus the tools that
// exist only for THIS caller.
//
// The build-time half stays what it was — the pre-rendered bytes, verbatim, no
// marshal of 451 schemas and no plugin touched to produce it. A door with no
// per-caller half returns exactly those bytes, so the memcpy that makes the
// most-called MCP method free is untouched by this file.
//
// The per-caller half runs only when the request NAMES a caller, which is the
// whole of why it can be afforded: a tenant's own tools cannot be known without
// asking, and there is nothing to ask about when nobody is asking. An anonymous
// probe therefore still costs a memcpy and starts no child.
func (a *App) listTools(fc fiber.Ctx, req mcpRequest) json.RawMessage {
	fleet := json.RawMessage("[]")
	if cur := a.mcpList.Load(); cur != nil {
		fleet = *cur
	}
	if !a.hasCaller() || fc.Get(HeaderOrg) == "" {
		return fleet
	}
	mine := a.callerTools(fc)
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

// callerTools is every tool that exists because of WHO is asking: this app's own
// Source, and every OPEN plugin's answer to the same question. A name the
// build-time catalogue already claims is dropped — one name is one dispatch, and
// the projected op is the one the whole fleet agreed on.
func (a *App) callerTools(fc fiber.Ctx) []mcpTool {
	claimed := map[string]bool{}
	if cur := a.mcpNames.Load(); cur != nil {
		claimed = *cur
	}
	var out []mcpTool
	keep := func(name string, raw json.RawMessage) {
		if name == "" || claimed[name] {
			return
		}
		claimed[name] = true
		out = append(out, mcpTool{name: name, raw: raw})
	}
	if src := a.cfg.MCP.Source; src != nil {
		for _, t := range src.Tools(fc.Context()) {
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
		for _, t := range a.askOpen(fc, p) {
			keep(t.name, t.raw)
		}
	}
	return out
}

// openPlugin is the plugin that declared an OPEN catalogue, or nil. At most one:
// see installTools.
func (a *App) openPlugin() *plugin {
	a.plugMu.Lock()
	defer a.plugMu.Unlock()
	return a.open
}

// askOpen forwards this very request — fc's own tools/list message — to one open plugin and lifts the tools out
// of its reply. The child answers as itself — its own registry, its own Source,
// its own view of the caller the request names — so the host learns the tenant's
// tools without holding the tenant's data.
//
// A child that fails contributes nothing. A tools/list is a question with a
// partial answer available, and blanking the fleet's whole surface because one
// plugin is down would be a far worse answer than a shorter list.
func (a *App) askOpen(fc fiber.Ctx, p *plugin) []mcpTool {
	client, host := p.target()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if err := forward(fc.Request(), resp, client, host, p.spec.mcpPath(), "mcp "+p.name); err != nil {
		a.logger.Warn("zip mcp: open plugin did not answer tools/list", "plugin", p.name, "err", err)
		return nil
	}
	var env struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp.Body(), &env); err != nil {
		a.logger.Warn("zip mcp: open plugin sent an unreadable tools/list", "plugin", p.name, "err", err)
		return nil
	}
	out := make([]mcpTool, 0, len(env.Result.Tools))
	for _, raw := range env.Result.Tools {
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
	tools := make([]map[string]any, 0, len(a.registry))
	for _, op := range a.registry {
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

// mcpCall runs a tools/call: find the op by name, invoke the SAME handler core
// the REST route uses, and return its JSON result as MCP text content. A handler
// error is reported as MCP isError content (not a JSON-RPC transport error), per
// the MCP spec — the model sees the failure and can react.
func (a *App) mcpCall(fc fiber.Ctx, req mcpRequest) error {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &params)

	op := a.opByName(params.Name)
	if op == nil || op.invoke == nil {
		// Not ours — a composed plugin may own it. Only a CALL wakes a child:
		// tools/list answered from the catalogue and touched nothing.
		if p := a.toolOwner(params.Name); p != nil {
			return a.mcpForward(fc, req, p)
		}
		// Still nobody's: the name may be one that exists only for this caller,
		// which no catalogue could have claimed. It goes to whoever offered the
		// per-caller half of the list — this app's own Source, or the open plugin.
		// Neither can be wrong about it: the same code that named the tool runs it.
		if src := a.cfg.MCP.Source; src != nil {
			out, err := src.Call(fc.Context(), params.Name, params.Arguments)
			return a.mcpAnswer(fc, req, out, err)
		}
		if p := a.openPlugin(); p != nil {
			return a.mcpForward(fc, req, p)
		}
		return fc.JSON(mcpErr(req.ID, -32602, "unknown tool: "+params.Name))
	}

	// No URL over MCP: a tools/call carries every argument in its JSON arguments
	// object, so the body IS the whole input — neither query nor path binds.
	out, err := op.invoke(fc.Context(), jsonenc.Unmarshal, params.Arguments, nil, nil)
	return a.mcpAnswer(fc, req, out, err)
}

// mcpAnswer renders one tool result. A handler error is MCP isError content and
// not a JSON-RPC transport error, per the spec — the model sees the failure and
// can react. ONE renderer, so a typed op and a Source answer the same shape.
func (a *App) mcpAnswer(fc fiber.Ctx, req mcpRequest, out any, err error) error {
	if err != nil {
		return fc.JSON(mcpResult(req.ID, map[string]any{
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
	return fc.JSON(mcpResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}))
}

// toolOwner is the plugin that declared name in its catalogue, or nil.
func (a *App) toolOwner(name string) *plugin {
	a.plugMu.Lock()
	defer a.plugMu.Unlock()
	return a.toolOwners[name]
}

// mcpForward hands the SAME JSON-RPC message to the plugin that owns the tool,
// at that plugin's own MCP path, over the transport it was composed on — for a
// Load'ed child, ZAP on its private unix socket. The plugin's own registry
// answers, so the host can only ever NAME a tool, never invoke one the child did
// not declare: a stale catalogue yields the child's own -32602, not a wrong call.
//
// This is the ONLY trigger that starts a process, and it starts exactly one:
// p.target() is the same single-flighted lazy path a prefix request takes.
//
// A hop failure is MCP isError content rather than an HTTP error, per the spec —
// the model sees "this tool is not available right now" and can react, where a
// 503 body would be a transport failure it cannot interpret.
func (a *App) mcpForward(fc fiber.Ctx, req mcpRequest, p *plugin) error {
	client, host := p.target()
	if err := forward(fc.Request(), fc.Response(), client, host, p.spec.mcpPath(), "mcp "+p.name); err != nil {
		return fc.JSON(mcpResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}))
	}
	return nil
}

func (a *App) opByName(name string) *registeredOp {
	for _, op := range a.registry {
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
	return defaultOpID(op.Method, op.Path)
}

func mcpResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "result": result}
}

func mcpErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "error": map[string]any{"code": code, "message": msg}}
}

func idOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
