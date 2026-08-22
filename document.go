package zip

// Reading a GraphQL document — the lexer and parser behind [App.GraphQL].
//
// Hand-written and about four hundred lines, because a dependency here would be
// a dependency in every service that links zip, to read a grammar whose whole
// executable half is six token kinds and five productions. The schema half of
// GraphQL is large; the query half a client actually sends is not.
//
// # What it reads
//
// Operations (named, anonymous, and the bare `{ ... }` shorthand), variables
// with defaults, fields with aliases and arguments, nested selections, named
// fragments, inline fragments, and @skip/@include. Every value form: strings
// including block strings, ints, floats, booleans, null, enums, lists, objects,
// and variable references.
//
// # What it refuses, and why refusing is the honest answer
//
// A subscription parses to an operation no registry can answer, since no op is a
// stream. An unknown directive changes what a field means, so ignoring one would
// answer a question that was not asked. Both are named in the error rather than
// quietly dropped.

import (
	"fmt"
	"strconv"
	"strings"
)

// depthLimit bounds nesting. The parser recurses once per level, so a document
// nested deeply enough would exhaust the stack before any handler saw it. This
// is a bound on recursion, not a policy: no real query approaches it.
const depthLimit = 64

// gqlDoc is one parsed document: what it can run, and the fragments those runs
// may spread.
type gqlDoc struct {
	ops   []*gqlOp
	frags map[string][]*gqlSel
}

type gqlOp struct {
	kind string // "query" or "mutation"
	name string
	vars []gqlVar
	sel  []*gqlSel
}

type gqlVar struct {
	name string
	typ  string
	req  bool
	def  any
}

// gqlSel is one selected thing: a field, or a spread of a named fragment.
type gqlSel struct {
	alias     string
	name      string
	args      map[string]any
	sel       []*gqlSel
	spread    string // non-empty: spread this fragment here instead
	skipIf    any
	includeIf any
}

// key is the name this selection answers under — its alias where it has one,
// which is the whole point of an alias.
func (s *gqlSel) key() string {
	if s.alias != "" {
		return s.alias
	}
	return s.name
}

// pick chooses the operation to run.
//
// An unnamed request against a multi-operation document is refused rather than
// resolved by position: which one ran would then depend on the order they were
// written in, and a client that adds an operation would silently start running
// a different one.
func (d *gqlDoc) pick(name string) (*gqlOp, error) {
	if len(d.ops) == 0 {
		return nil, fmt.Errorf("the document defines no operation to run")
	}
	if name == "" {
		if len(d.ops) > 1 {
			return nil, fmt.Errorf("the document defines %d operations; name the one to run", len(d.ops))
		}
		return d.ops[0], nil
	}
	for _, o := range d.ops {
		if o.name == name {
			return o, nil
		}
	}
	return nil, fmt.Errorf("no operation named %q in the document", name)
}

// gqlVarRef is a variable where a value would be. It stays a reference through
// parsing so one parsed document could serve any set of variables, and so an
// undefined one is caught by name at execution rather than becoming a null.
type gqlVarRef string

// ---------------------------------------------------------------- the lexer

// gqlToken kinds: 'n' name, 'i' int, 'f' float, 's' string, 'p' punctuator,
// and 0 for the end of the document.
type gqlToken struct {
	kind byte
	text string
}

type gqlLexer struct {
	src string
	pos int
}

const punctuators = "!$&()[]{}:=@|"

func (l *gqlLexer) next() (gqlToken, error) {
	l.skip()
	if l.pos >= len(l.src) {
		return gqlToken{}, nil
	}
	c := l.src[l.pos]
	switch {
	case c == '_' || isAlpha(c):
		start := l.pos
		for l.pos < len(l.src) && (isAlpha(l.src[l.pos]) || isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
		return gqlToken{'n', l.src[start:l.pos]}, nil
	case c == '-' || isDigit(c):
		return l.number(), nil
	case c == '"':
		return l.text()
	case strings.IndexByte(punctuators, c) >= 0:
		l.pos++
		return gqlToken{'p', string(c)}, nil
	case strings.HasPrefix(l.src[l.pos:], "..."):
		l.pos += 3
		return gqlToken{'p', "..."}, nil
	}
	return gqlToken{}, fmt.Errorf("unexpected character %q", string(rune(c)))
}

// skip passes over everything GraphQL calls ignored: whitespace, line breaks,
// commas — a comma IS whitespace in this grammar — and comments.
func (l *gqlLexer) skip() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\n', '\r', ',':
			l.pos++
		case '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		default:
			return
		}
	}
}

func (l *gqlLexer) number() gqlToken {
	start := l.pos
	kind := byte('i')
	if l.src[l.pos] == '-' {
		l.pos++
	}
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		kind = 'f'
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		kind = 'f'
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	return gqlToken{kind, l.src[start:l.pos]}
}

func (l *gqlLexer) text() (gqlToken, error) {
	if strings.HasPrefix(l.src[l.pos:], `"""`) {
		l.pos += 3
		end := strings.Index(l.src[l.pos:], `"""`)
		if end < 0 {
			return gqlToken{}, fmt.Errorf("unterminated block string")
		}
		s := l.src[l.pos : l.pos+end]
		l.pos += end + 3
		return gqlToken{'s', undent(s)}, nil
	}
	l.pos++ // the opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; c {
		case '"':
			l.pos++
			return gqlToken{'s', b.String()}, nil
		case '\n', '\r':
			return gqlToken{}, fmt.Errorf("unterminated string")
		case '\\':
			l.pos++
			if l.pos >= len(l.src) {
				return gqlToken{}, fmt.Errorf("unterminated string")
			}
			switch l.src[l.pos] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case '/', '\\', '"':
				b.WriteByte(l.src[l.pos])
			case 'u':
				if l.pos+5 > len(l.src) {
					return gqlToken{}, fmt.Errorf("truncated \\u escape")
				}
				n, err := strconv.ParseUint(l.src[l.pos+1:l.pos+5], 16, 32)
				if err != nil {
					return gqlToken{}, fmt.Errorf("bad \\u escape %q", l.src[l.pos+1:l.pos+5])
				}
				b.WriteRune(rune(n))
				l.pos += 4
			default:
				return gqlToken{}, fmt.Errorf("unknown escape \\%s", string(rune(l.src[l.pos])))
			}
			l.pos++
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return gqlToken{}, fmt.Errorf("unterminated string")
}

// undent removes a block string's common indentation, so a value written inside
// an indented query means what it looks like it means.
func undent(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	common := -1
	for _, ln := range lines[1:] {
		trimmed := strings.TrimLeft(ln, " \t")
		if trimmed == "" {
			continue // a blank line indents nothing
		}
		if n := len(ln) - len(trimmed); common < 0 || n < common {
			common = n
		}
	}
	if common > 0 {
		for i, ln := range lines[1:] {
			if len(ln) >= common {
				lines[i+1] = ln[common:]
			}
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ---------------------------------------------------------------- the parser

type gqlParser struct {
	lex   gqlLexer
	tok   gqlToken
	err   error
	depth int
}

// advance reads one token. A lexer error ends the document as well as recording
// itself, so every loop below terminates on bad input instead of spinning on a
// token that will not change.
func (p *gqlParser) advance() {
	tok, err := p.lex.next()
	if err != nil {
		p.err, p.tok = err, gqlToken{}
		return
	}
	p.tok = tok
}

func (p *gqlParser) is(text string) bool {
	return p.tok.kind == 'p' && p.tok.text == text
}

func (p *gqlParser) name() (string, bool) {
	if p.tok.kind != 'n' {
		return "", false
	}
	s := p.tok.text
	p.advance()
	return s, true
}

func parseGraph(src string) (*gqlDoc, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("the request carries no query")
	}
	p := &gqlParser{lex: gqlLexer{src: strings.TrimPrefix(src, "\ufeff")}}
	p.advance()
	if p.err != nil {
		return nil, p.err
	}
	d := &gqlDoc{frags: map[string][]*gqlSel{}}
	for p.tok.kind != 0 {
		switch {
		case p.is("{"):
			sel, err := p.selection()
			if err != nil {
				return nil, err
			}
			d.ops = append(d.ops, &gqlOp{kind: "query", sel: sel})
		case p.tok.kind == 'n' && (p.tok.text == "query" || p.tok.text == "mutation"):
			op, err := p.operation()
			if err != nil {
				return nil, err
			}
			d.ops = append(d.ops, op)
		case p.tok.kind == 'n' && p.tok.text == "subscription":
			return nil, fmt.Errorf("subscriptions are not served: no op is a stream")
		case p.tok.kind == 'n' && p.tok.text == "fragment":
			name, sel, err := p.fragment()
			if err != nil {
				return nil, err
			}
			if _, dup := d.frags[name]; dup {
				return nil, fmt.Errorf("fragment %q is defined twice", name)
			}
			d.frags[name] = sel
		default:
			return nil, fmt.Errorf("unexpected %q where an operation or fragment was expected", p.tok.text)
		}
		if p.err != nil {
			return nil, p.err
		}
	}
	return d, p.err
}

func (p *gqlParser) operation() (*gqlOp, error) {
	op := &gqlOp{kind: p.tok.text}
	p.advance()
	if n, ok := p.name(); ok {
		op.name = n
	}
	if p.is("(") {
		vars, err := p.varDefs()
		if err != nil {
			return nil, err
		}
		op.vars = vars
	}
	if _, _, err := p.directives(); err != nil {
		return nil, err
	}
	sel, err := p.selection()
	if err != nil {
		return nil, err
	}
	op.sel = sel
	return op, nil
}

func (p *gqlParser) fragment() (string, []*gqlSel, error) {
	p.advance() // "fragment"
	name, ok := p.name()
	if !ok {
		return "", nil, fmt.Errorf("expected a name after `fragment`")
	}
	if on, ok := p.name(); !ok || on != "on" {
		return "", nil, fmt.Errorf("expected `on` after fragment %q", name)
	}
	if _, ok := p.name(); !ok {
		return "", nil, fmt.Errorf("expected a type after `on` in fragment %q", name)
	}
	if _, _, err := p.directives(); err != nil {
		return "", nil, err
	}
	sel, err := p.selection()
	if err != nil {
		return "", nil, err
	}
	return name, sel, nil
}

func (p *gqlParser) varDefs() ([]gqlVar, error) {
	p.advance() // "("
	var out []gqlVar
	for !p.is(")") {
		if p.tok.kind == 0 {
			return nil, fmt.Errorf("unterminated variable definitions")
		}
		if !p.is("$") {
			return nil, fmt.Errorf("expected $variable, got %q", p.tok.text)
		}
		p.advance()
		name, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("expected a variable name after $")
		}
		if !p.is(":") {
			return nil, fmt.Errorf("expected : after $%s", name)
		}
		p.advance()
		typ, req, err := p.typeRef()
		if err != nil {
			return nil, err
		}
		v := gqlVar{name: name, typ: typ, req: req}
		if p.is("=") {
			p.advance()
			def, err := p.value()
			if err != nil {
				return nil, err
			}
			v.def = def
		}
		out = append(out, v)
	}
	p.advance() // ")"
	return out, nil
}

// typeRef reads a variable's declared type. The name is kept for error messages
// and the ! for whether a value must be given; the type itself is not checked
// here, because the op's own validator checks the value that actually arrives —
// checking twice would let the two disagree.
func (p *gqlParser) typeRef() (string, bool, error) {
	var name string
	switch {
	case p.is("["):
		p.advance()
		inner, innerReq, err := p.typeRef()
		if err != nil {
			return "", false, err
		}
		if !p.is("]") {
			return "", false, fmt.Errorf("expected ] closing a list type")
		}
		p.advance()
		if innerReq {
			inner += "!"
		}
		name = "[" + inner + "]"
	default:
		n, ok := p.name()
		if !ok {
			return "", false, fmt.Errorf("expected a type name, got %q", p.tok.text)
		}
		name = n
	}
	if p.is("!") {
		p.advance()
		return name, true, nil
	}
	return name, false, nil
}

func (p *gqlParser) selection() ([]*gqlSel, error) {
	if !p.is("{") {
		if p.err != nil {
			return nil, p.err
		}
		return nil, fmt.Errorf("expected { opening a selection, got %q", p.tok.text)
	}
	p.depth++
	if p.depth > depthLimit {
		return nil, fmt.Errorf("selections nest deeper than %d levels", depthLimit)
	}
	defer func() { p.depth-- }()
	p.advance()

	var out []*gqlSel
	for !p.is("}") {
		if p.tok.kind == 0 {
			if p.err != nil {
				return nil, p.err
			}
			return nil, fmt.Errorf("unterminated selection")
		}
		if p.is("...") {
			inner, err := p.spread()
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		f, err := p.field()
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	p.advance() // "}"
	if len(out) == 0 {
		return nil, fmt.Errorf("a selection cannot be empty")
	}
	return out, nil
}

// spread reads `...Name` or `... on Type { ... }`.
//
// An inline fragment is flattened into the selection that holds it, and its type
// condition carries no meaning here: every type this schema publishes is a
// concrete object, no field answers with an interface or a union, so a condition
// can only ever name the type the field already has.
func (p *gqlParser) spread() ([]*gqlSel, error) {
	p.advance() // "..."
	if p.tok.kind == 'n' && p.tok.text == "on" {
		p.advance()
		if _, ok := p.name(); !ok {
			return nil, fmt.Errorf("expected a type after `on`")
		}
		if _, _, err := p.directives(); err != nil {
			return nil, err
		}
		return p.selection()
	}
	if p.is("{") {
		return p.selection()
	}
	name, ok := p.name()
	if !ok {
		return nil, fmt.Errorf("expected a fragment name after ...")
	}
	s := &gqlSel{spread: name}
	skip, incl, err := p.directives()
	if err != nil {
		return nil, err
	}
	s.skipIf, s.includeIf = skip, incl
	return []*gqlSel{s}, nil
}

func (p *gqlParser) field() (*gqlSel, error) {
	name, ok := p.name()
	if !ok {
		return nil, fmt.Errorf("expected a field name, got %q", p.tok.text)
	}
	f := &gqlSel{name: name}
	if p.is(":") {
		p.advance()
		real, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("expected a field name after the alias %q", name)
		}
		f.alias, f.name = name, real
	}
	if p.is("(") {
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		f.args = args
	}
	skip, incl, err := p.directives()
	if err != nil {
		return nil, err
	}
	f.skipIf, f.includeIf = skip, incl
	if p.is("{") {
		sel, err := p.selection()
		if err != nil {
			return nil, err
		}
		f.sel = sel
	}
	return f, nil
}

// directives reads @skip and @include and refuses anything else.
//
// A directive changes what a field means. Ignoring an unknown one would answer a
// question the client did not ask — which is worse than saying it is unknown.
func (p *gqlParser) directives() (skip, include any, err error) {
	for p.is("@") {
		p.advance()
		name, ok := p.name()
		if !ok {
			return nil, nil, fmt.Errorf("expected a directive name after @")
		}
		var args map[string]any
		if p.is("(") {
			args, err = p.args()
			if err != nil {
				return nil, nil, err
			}
		}
		switch name {
		case "skip", "include":
			cond, ok := args["if"]
			if !ok {
				return nil, nil, fmt.Errorf("@%s needs an `if` argument", name)
			}
			if name == "skip" {
				skip = cond
			} else {
				include = cond
			}
		default:
			return nil, nil, fmt.Errorf("unknown directive @%s", name)
		}
	}
	return skip, include, nil
}

func (p *gqlParser) args() (map[string]any, error) {
	p.advance() // "("
	out := map[string]any{}
	for !p.is(")") {
		if p.tok.kind == 0 {
			if p.err != nil {
				return nil, p.err
			}
			return nil, fmt.Errorf("unterminated arguments")
		}
		name, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("expected an argument name, got %q", p.tok.text)
		}
		if !p.is(":") {
			return nil, fmt.Errorf("expected : after the argument %q", name)
		}
		p.advance()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	p.advance() // ")"
	return out, nil
}

func (p *gqlParser) value() (any, error) {
	p.depth++
	if p.depth > depthLimit {
		return nil, fmt.Errorf("values nest deeper than %d levels", depthLimit)
	}
	defer func() { p.depth-- }()

	switch {
	case p.is("$"):
		p.advance()
		name, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("expected a variable name after $")
		}
		return gqlVarRef(name), nil
	case p.tok.kind == 's':
		s := p.tok.text
		p.advance()
		return s, nil
	case p.tok.kind == 'i':
		n, err := strconv.ParseInt(p.tok.text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %q is out of range", p.tok.text)
		}
		p.advance()
		return n, nil
	case p.tok.kind == 'f':
		n, err := strconv.ParseFloat(p.tok.text, 64)
		if err != nil {
			return nil, fmt.Errorf("number %q is out of range", p.tok.text)
		}
		p.advance()
		return n, nil
	case p.tok.kind == 'n':
		switch s := p.tok.text; s {
		case "true", "false":
			p.advance()
			return s == "true", nil
		case "null":
			p.advance()
			return nil, nil
		default:
			// An enum is carried as its own name: the Go side receives a string,
			// which is what an enum field is declared as.
			p.advance()
			return s, nil
		}
	case p.is("["):
		p.advance()
		out := []any{}
		for !p.is("]") {
			if p.tok.kind == 0 {
				return nil, fmt.Errorf("unterminated list value")
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		p.advance()
		return out, nil
	case p.is("{"):
		p.advance()
		out := map[string]any{}
		for !p.is("}") {
			if p.tok.kind == 0 {
				return nil, fmt.Errorf("unterminated object value")
			}
			name, ok := p.name()
			if !ok {
				return nil, fmt.Errorf("expected a field name in an object value, got %q", p.tok.text)
			}
			if !p.is(":") {
				return nil, fmt.Errorf("expected : after the object field %q", name)
			}
			p.advance()
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			out[name] = v
		}
		p.advance()
		return out, nil
	}
	if p.err != nil {
		return nil, p.err
	}
	return nil, fmt.Errorf("unexpected %q where a value was expected", p.tok.text)
}
