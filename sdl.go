package zip

// Reading a GraphQL schema — the type-system half of the language, which a
// graph PUBLISHES rather than receives.
//
// The engine executes against resolvers and never against a schema, so nothing
// here runs on a request. It exists for the other side of publishing: a schema
// is what every client, code generator and editor builds against, and one that
// names a type it never defines, or takes an object where only an input may go,
// is a schema none of them can use. [CheckSchema] is how a graph — zip's own
// projection, or one a host composes — finds that out in a test rather than from
// a client.

import (
	"fmt"
	"strings"
)

// CheckSchema reports whether sdl is a GraphQL schema a client can build
// against: it parses; no type, field, argument or enum value is defined twice;
// every type it names is defined or built in; a field answers an output type and
// an argument or an input field takes an input type; an object, interface or
// input has fields and an enum or union has members; a union's members are
// objects and what a type implements is an interface; and the root types — the
// schema block's, or Query and Mutation by name — are objects, with a query
// root present.
func CheckSchema(sdl string) error {
	s, err := readSchema(sdl)
	if err != nil {
		return err
	}
	return s.check()
}

// sdlType is one named type as the schema defines it.
type sdlType struct {
	kind    string // scalar, type, interface, union, enum, input
	fields  []sdlField
	members []string // a union's members, an enum's values
	ifaces  []string // what an object or interface implements
}

type sdlField struct {
	name string
	typ  string // the named type at the bottom of any list or non-null
	args []sdlField
}

type sdlSchema struct {
	types map[string]*sdlType
	roots map[string]string // operation → type, from a schema block
}

var builtinScalars = []string{"String", "Int", "Float", "Boolean", "ID"}

func readSchema(src string) (*sdlSchema, error) {
	p := &gqlParser{lex: gqlLexer{src: strings.TrimPrefix(src, "\ufeff")}}
	p.advance()
	s := &sdlSchema{types: map[string]*sdlType{}}
	for _, n := range builtinScalars {
		s.types[n] = &sdlType{kind: "scalar"}
	}
	for p.tok.kind != 0 {
		if p.err != nil {
			return nil, p.err
		}
		p.description()
		word, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("unexpected %q where a definition was expected", p.tok.text)
		}
		var err error
		switch word {
		case "schema":
			err = p.schemaBlock(s)
		case "scalar", "type", "interface", "union", "enum", "input":
			err = p.typeDef(s, word)
		case "directive":
			err = p.directiveDef()
		case "extend":
			err = fmt.Errorf("`extend` is not read here; a published schema states each type once")
		default:
			err = fmt.Errorf("unknown definition %q", word)
		}
		if err != nil {
			return nil, err
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return s, nil
}

// description passes over the string a definition, a field or an argument may
// carry before it.
func (p *gqlParser) description() {
	if p.tok.kind == 's' {
		p.advance()
	}
}

// constDirectives passes over the directives a definition may carry; in a
// schema they annotate (@deprecated) rather than change what is asked.
func (p *gqlParser) constDirectives() error {
	for p.is("@") {
		p.advance()
		if _, ok := p.name(); !ok {
			return fmt.Errorf("expected a directive name after @")
		}
		if p.is("(") {
			if _, err := p.args(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *gqlParser) schemaBlock(s *sdlSchema) error {
	if s.roots != nil {
		return fmt.Errorf("the schema block is defined twice")
	}
	if err := p.constDirectives(); err != nil {
		return err
	}
	if !p.is("{") {
		return fmt.Errorf("expected { after schema")
	}
	p.advance()
	s.roots = map[string]string{}
	for !p.is("}") {
		op, ok := p.name()
		if !ok {
			return fmt.Errorf("unterminated schema block")
		}
		if op != "query" && op != "mutation" && op != "subscription" {
			return fmt.Errorf("schema names an operation %q; there are query, mutation and subscription", op)
		}
		if _, dup := s.roots[op]; dup {
			return fmt.Errorf("schema names its %s type twice", op)
		}
		if !p.is(":") {
			return fmt.Errorf("expected : after %s in the schema block", op)
		}
		p.advance()
		t, ok := p.name()
		if !ok {
			return fmt.Errorf("expected a type for %s in the schema block", op)
		}
		s.roots[op] = t
	}
	p.advance()
	return nil
}

func (p *gqlParser) typeDef(s *sdlSchema, kind string) error {
	name, ok := p.name()
	if !ok {
		return fmt.Errorf("expected a name after %s", kind)
	}
	if _, dup := s.types[name]; dup {
		return fmt.Errorf("type %q is defined twice", name)
	}
	t := &sdlType{kind: kind}
	s.types[name] = t
	if kind == "type" || kind == "interface" {
		if p.tok.kind == 'n' && p.tok.text == "implements" {
			p.advance()
			if p.is("&") {
				p.advance()
			}
			for {
				i, ok := p.name()
				if !ok {
					return fmt.Errorf("expected an interface after implements in %q", name)
				}
				t.ifaces = append(t.ifaces, i)
				if !p.is("&") {
					break
				}
				p.advance()
			}
		}
	}
	if err := p.constDirectives(); err != nil {
		return err
	}
	switch kind {
	case "scalar":
		return nil
	case "union":
		if !p.is("=") {
			return fmt.Errorf("expected = after union %q", name)
		}
		p.advance()
		if p.is("|") {
			p.advance()
		}
		for {
			m, ok := p.name()
			if !ok {
				return fmt.Errorf("expected a member of union %q", name)
			}
			t.members = append(t.members, m)
			if !p.is("|") {
				return nil
			}
			p.advance()
		}
	case "enum":
		return p.enumValues(name, t)
	}
	fields, err := p.fieldDefs(name, kind == "input")
	if err != nil {
		return err
	}
	t.fields = fields
	return nil
}

func (p *gqlParser) enumValues(name string, t *sdlType) error {
	if !p.is("{") {
		return fmt.Errorf("expected { after enum %q", name)
	}
	p.advance()
	seen := map[string]bool{}
	for !p.is("}") {
		p.description()
		v, ok := p.name()
		if !ok {
			return fmt.Errorf("unterminated enum %q", name)
		}
		if seen[v] {
			return fmt.Errorf("enum %q lists %s twice", name, v)
		}
		seen[v] = true
		t.members = append(t.members, v)
		if err := p.constDirectives(); err != nil {
			return err
		}
	}
	p.advance()
	return nil
}

// fieldDefs reads a braced list of fields. An input's fields are values, so they
// may carry a default and take no arguments; an object's take arguments.
func (p *gqlParser) fieldDefs(owner string, input bool) ([]sdlField, error) {
	if !p.is("{") {
		return nil, fmt.Errorf("expected { opening the fields of %q", owner)
	}
	p.advance()
	var out []sdlField
	seen := map[string]bool{}
	for !p.is("}") {
		if p.tok.kind == 0 {
			if p.err != nil {
				return nil, p.err
			}
			return nil, fmt.Errorf("unterminated fields of %q", owner)
		}
		p.description()
		name, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("expected a field name in %q, got %q", owner, p.tok.text)
		}
		if seen[name] {
			return nil, fmt.Errorf("%q has the field %s twice", owner, name)
		}
		seen[name] = true
		f := sdlField{name: name}
		if p.is("(") {
			if input {
				return nil, fmt.Errorf("%s.%s: an input field takes no arguments", owner, name)
			}
			args, err := p.argDefs(owner + "." + name)
			if err != nil {
				return nil, err
			}
			f.args = args
		}
		typ, err := p.sdlTypeRef(owner + "." + name)
		if err != nil {
			return nil, err
		}
		f.typ = typ
		if input && p.is("=") {
			p.advance()
			if _, err := p.value(); err != nil {
				return nil, err
			}
		}
		if err := p.constDirectives(); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	p.advance()
	return out, nil
}

func (p *gqlParser) argDefs(at string) ([]sdlField, error) {
	p.advance() // "("
	var out []sdlField
	seen := map[string]bool{}
	for !p.is(")") {
		if p.tok.kind == 0 {
			return nil, fmt.Errorf("unterminated arguments of %s", at)
		}
		p.description()
		name, ok := p.name()
		if !ok {
			return nil, fmt.Errorf("expected an argument name in %s, got %q", at, p.tok.text)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s takes the argument %s twice", at, name)
		}
		seen[name] = true
		typ, err := p.sdlTypeRef(at + "(" + name + ")")
		if err != nil {
			return nil, err
		}
		if p.is("=") {
			p.advance()
			if _, err := p.value(); err != nil {
				return nil, err
			}
		}
		if err := p.constDirectives(); err != nil {
			return nil, err
		}
		out = append(out, sdlField{name: name, typ: typ})
	}
	p.advance()
	return out, nil
}

// sdlTypeRef reads `: T`, `: [T]` or `: T!` and answers the named type at its
// bottom.
func (p *gqlParser) sdlTypeRef(at string) (string, error) {
	if !p.is(":") {
		return "", fmt.Errorf("expected : and a type after %s", at)
	}
	p.advance()
	var inner func() (string, error)
	inner = func() (string, error) {
		var name string
		if p.is("[") {
			p.advance()
			n, err := inner()
			if err != nil {
				return "", err
			}
			if !p.is("]") {
				return "", fmt.Errorf("expected ] closing a list type at %s", at)
			}
			p.advance()
			name = n
		} else {
			n, ok := p.name()
			if !ok {
				return "", fmt.Errorf("expected a type at %s, got %q", at, p.tok.text)
			}
			name = n
		}
		if p.is("!") {
			p.advance()
		}
		return name, nil
	}
	return inner()
}

// directiveDef passes over a directive definition; the schema's own directives
// annotate and are not checked here.
func (p *gqlParser) directiveDef() error {
	if !p.is("@") {
		return fmt.Errorf("expected @ after directive")
	}
	p.advance()
	name, ok := p.name()
	if !ok {
		return fmt.Errorf("expected a directive name")
	}
	if p.is("(") {
		if _, err := p.argDefs("@" + name); err != nil {
			return err
		}
	}
	if p.tok.kind == 'n' && p.tok.text == "repeatable" {
		p.advance()
	}
	if on, ok := p.name(); !ok || on != "on" {
		return fmt.Errorf("expected on after directive @%s", name)
	}
	if p.is("|") {
		p.advance()
	}
	for {
		if _, ok := p.name(); !ok {
			return fmt.Errorf("expected a location for directive @%s", name)
		}
		if !p.is("|") {
			return nil
		}
		p.advance()
	}
}

func (s *sdlSchema) check() error {
	kindOf := func(name string) string {
		if t, ok := s.types[name]; ok {
			return t.kind
		}
		return ""
	}
	input := func(k string) bool { return k == "scalar" || k == "enum" || k == "input" }
	for name, t := range s.types {
		switch t.kind {
		case "type", "interface", "input":
			if len(t.fields) == 0 {
				return fmt.Errorf("%s %q has no fields", t.kind, name)
			}
		case "union", "enum":
			if len(t.members) == 0 {
				return fmt.Errorf("%s %q has no members", t.kind, name)
			}
		}
		for _, m := range t.members {
			if t.kind == "union" && kindOf(m) != "type" {
				return fmt.Errorf("union %q includes %q, which is not an object type", name, m)
			}
		}
		for _, i := range t.ifaces {
			if kindOf(i) != "interface" {
				return fmt.Errorf("%q implements %q, which is not an interface", name, i)
			}
		}
		for _, f := range t.fields {
			k := kindOf(f.typ)
			switch {
			case k == "":
				return fmt.Errorf("%s.%s is of type %q, which the schema does not define", name, f.name, f.typ)
			case t.kind == "input" && !input(k):
				return fmt.Errorf("%s.%s is of type %q, a %s; an input field takes a scalar, an enum or an input", name, f.name, f.typ, k)
			case t.kind != "input" && k == "input":
				return fmt.Errorf("%s.%s answers %q, which is an input type", name, f.name, f.typ)
			}
			for _, a := range f.args {
				ak := kindOf(a.typ)
				if ak == "" {
					return fmt.Errorf("%s.%s(%s) is of type %q, which the schema does not define", name, f.name, a.name, a.typ)
				}
				if !input(ak) {
					return fmt.Errorf("%s.%s(%s) takes %q, a %s; an argument takes a scalar, an enum or an input", name, f.name, a.name, a.typ, ak)
				}
			}
		}
	}
	roots := s.roots
	if roots == nil {
		roots = map[string]string{}
		for op, t := range map[string]string{"query": "Query", "mutation": "Mutation", "subscription": "Subscription"} {
			if _, ok := s.types[t]; ok {
				roots[op] = t
			}
		}
	}
	if _, ok := roots["query"]; !ok {
		return fmt.Errorf("the schema has no query type")
	}
	for op, t := range roots {
		if kindOf(t) != "type" {
			return fmt.Errorf("the %s root %q is not an object type the schema defines", op, t)
		}
	}
	return nil
}
