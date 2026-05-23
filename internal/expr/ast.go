package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Position identifies where a node was parsed from, used in diagnostics.
// File is the absolute path to the YAML file; Line and Col are 1-indexed
// positions of the start of the relevant text.
type Position struct {
	File string
	Line int
	Col  int
}

// String returns a "file:line:col" form for log lines.
func (p Position) String() string {
	if p.File == "" && p.Line == 0 {
		return "<unknown>"
	}
	if p.Line == 0 {
		return p.File
	}
	if p.Col == 0 {
		return p.File + ":" + strconv.Itoa(p.Line)
	}
	return p.File + ":" + strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Col)
}

// Node is the AST interface. All expression nodes implement Pos and
// String. String returns the canonical text form (round-trippable for
// `gmk explain` output and Stage 6's IR cache).
type Node interface {
	Pos() Position
	String() string
}

// -----------------------------------------------------------------------------
// Stage 3a node types
// -----------------------------------------------------------------------------

// Literal is a constant value embedded in the expression.
//
// Examples (in YAML):
//
//	x: "hello"          -> Literal{Value: String("hello")}
//	y: "${42}"          -> Literal{Value: Int(42)}, wrapped in a top-level Concat
//	z: "${true}"        -> Literal{Value: Bool(true)}
type Literal struct {
	P     Position
	Value Value
}

func (l *Literal) Pos() Position { return l.P }
func (l *Literal) String() string {
	switch l.Value.Kind {
	case StringKind:
		return strconv.Quote(l.Value.Str)
	default:
		return l.Value.AsString()
	}
}

// VarRef is a bare reference to a var visible in scope.
//
// Example:
//
//	"${greeting}" -> VarRef{Name: "greeting"}
type VarRef struct {
	P    Position
	Name string
}

func (v *VarRef) Pos() Position  { return v.P }
func (v *VarRef) String() string { return "${" + v.Name + "}" }

// TypedRef is a kind-prefixed reference: ${kind:path}.
// Stage 3a kinds: "env", "ctx", "var".
// Stage 7 adds "probe". Stage 12 adds plugin-defined kinds.
//
// Examples:
//
//	"${env:HOME}"             -> TypedRef{Kind: "env", Path: "HOME"}
//	"${ctx:JWT}"              -> TypedRef{Kind: "ctx", Path: "JWT"}
//	"${probe:has_tool.docker}" -> TypedRef{Kind: "probe", Path: "has_tool.docker"} (S7)
type TypedRef struct {
	P    Position
	Kind string
	Path string
}

func (t *TypedRef) Pos() Position  { return t.P }
func (t *TypedRef) String() string { return "${" + t.Kind + ":" + t.Path + "}" }

// FuncCall is an explicit function invocation.
//
// Example:
//
//	"${upper(greeting)}" -> FuncCall{Name: "upper", Args: [VarRef{greeting}]}
type FuncCall struct {
	P    Position
	Name string
	Args []Node
}

func (f *FuncCall) Pos() Position { return f.P }
func (f *FuncCall) String() string {
	parts := make([]string, len(f.Args))
	for i, a := range f.Args {
		parts[i] = a.String()
	}
	return f.Name + "(" + strings.Join(parts, ", ") + ")"
}

// Pipeline composes a source value through a chain of unary-ish function
// stages. Each stage receives the prior stage's output as its first arg;
// remaining args are taken from the stage's own arg list.
//
// "${x | upper | replace(\"A\", \"B\")}" desugars to:
//
//	Pipeline{
//	  Source: VarRef{x},
//	  Stages: [
//	    FuncCall{Name: "upper", Args: nil},                  // receives x
//	    FuncCall{Name: "replace", Args: ["A", "B"]},         // receives upper(x)
//	  ],
//	}
//
// Stage execution order is left-to-right, matching shell pipelines.
type Pipeline struct {
	P      Position
	Source Node
	Stages []*FuncCall
}

func (p *Pipeline) Pos() Position { return p.P }
func (p *Pipeline) String() string {
	out := p.Source.String()
	for _, s := range p.Stages {
		out += " | " + s.String()
	}
	return out
}

// ModifierKind enumerates the supported bash-style modifier forms.
type ModifierKind int

const (
	ModDefault   ModifierKind = iota // :-default     use Arg if Inner is empty
	ModRequired                      // :?msg         error with Arg if Inner is empty
	ModAlternate                     // :+alt         use Arg if Inner is non-empty (otherwise empty)
)

// String returns the modifier's literal syntax.
func (k ModifierKind) String() string {
	switch k {
	case ModDefault:
		return ":-"
	case ModRequired:
		return ":?"
	case ModAlternate:
		return ":+"
	default:
		return "?"
	}
}

// Modifier wraps a ref (VarRef or TypedRef) with bash-compatible default /
// required / alternate behaviour.
//
// Examples:
//
//	"${user:-anonymous}"       -> Modifier{Kind: ModDefault,   Inner: VarRef{user}, Arg: "anonymous"}
//	"${env:TOKEN:?required}"   -> Modifier{Kind: ModRequired,  Inner: TypedRef{env, TOKEN}, Arg: "required"}
//	"${flag:+--debug}"         -> Modifier{Kind: ModAlternate, Inner: VarRef{flag}, Arg: "--debug"}
//
// Arg is captured as a raw literal string (no nested ${} substitution).
// Use functional forms (default(), coalesce()) for computed alternatives.
type Modifier struct {
	P     Position
	Inner Node // VarRef or TypedRef (validated at parse time)
	Kind  ModifierKind
	Arg   string
}

func (m *Modifier) Pos() Position { return m.P }
func (m *Modifier) String() string {
	inner := m.Inner.String()
	// Strip leading "${" and trailing "}" so we can rewrap cleanly.
	if strings.HasPrefix(inner, "${") && strings.HasSuffix(inner, "}") {
		inner = inner[2 : len(inner)-1]
	}
	return "${" + inner + m.Kind.String() + m.Arg + "}"
}

// BinaryOp is a two-operand operation.
//
// Stage 3a operators: "==" "!=".
// Stage 4 will add: && || < > <= >= =~
type BinaryOp struct {
	P   Position
	Op  string
	Lhs Node
	Rhs Node
}

func (b *BinaryOp) Pos() Position  { return b.P }
func (b *BinaryOp) String() string { return b.Lhs.String() + " " + b.Op + " " + b.Rhs.String() }

// Concat is a sequence of nodes whose evaluated values are joined as
// strings. This is the top-level shape of any template that mixes
// literal text and ${...} substitutions.
//
// "hello, ${user}!" parses to:
//
//	Concat{Parts: [Literal{"hello, "}, VarRef{user}, Literal{"!"}]}
//
// If a template has no substitutions, ParseTemplate returns the bare
// Literal instead of a Concat-of-one, simplifying both downstream code
// and the load-time fast path for pure literals.
type Concat struct {
	P     Position
	Parts []Node
}

func (c *Concat) Pos() Position { return c.P }
func (c *Concat) String() string {
	var b strings.Builder
	b.WriteByte('"')
	for _, p := range c.Parts {
		switch n := p.(type) {
		case *Literal:
			if n.Value.Kind == StringKind {
				b.WriteString(n.Value.Str)
			} else {
				b.WriteString(n.Value.AsString())
			}
		default:
			b.WriteString(n.String())
		}
	}
	b.WriteByte('"')
	return b.String()
}

// -----------------------------------------------------------------------------
// Helpers shared across nodes
// -----------------------------------------------------------------------------

// IsLiteral reports whether a Node is a single Literal (no refs, no ops).
// load.Load uses this to set Var.Kind = VarLiteral for pure-text values,
// allowing the resolve path to skip evaluation entirely.
func IsLiteral(n Node) bool {
	_, ok := n.(*Literal)
	return ok
}

// quote wraps a string with quotes, useful in error messages.
func quote(s string) string { return fmt.Sprintf("%q", s) }

// -----------------------------------------------------------------------------
// Stage 3b node types
// -----------------------------------------------------------------------------

// FieldAccess is a `.field` postfix on an expression that yields a map.
//
// Example:
//
//	"${config.host}" -> FieldAccess{Inner: VarRef{config}, Field: "host"}
//	"${user.address.city}" -> FieldAccess{
//	  Inner: FieldAccess{Inner: VarRef{user}, Field: "address"},
//	  Field: "city",
//	}
//
// Field access is right-associative for parsing but left-evaluated:
// the inner subtree is evaluated to a map, then Field is looked up.
type FieldAccess struct {
	P     Position
	Inner Node
	Field string
}

func (f *FieldAccess) Pos() Position  { return f.P }
func (f *FieldAccess) String() string { return f.Inner.String() + "." + f.Field }

// IndexAccess is a `[expr]` postfix on an expression that yields a
// list or map.
//
// Examples:
//
//	"${hosts[0]}"     -> IndexAccess{Inner: VarRef{hosts}, Key: Literal(0)}
//	"${cfg[key]}"     -> IndexAccess{Inner: VarRef{cfg},   Key: VarRef{key}}
//	"${cfg[\"host\"]}" -> IndexAccess{Inner: VarRef{cfg},  Key: Literal("host")}
//
// At evaluation time, the Key expression is evaluated and used to index
// into the Inner value via Value.Index.
type IndexAccess struct {
	P     Position
	Inner Node
	Key   Node
}

func (i *IndexAccess) Pos() Position  { return i.P }
func (i *IndexAccess) String() string { return i.Inner.String() + "[" + i.Key.String() + "]" }

// NamedCall is a function call with explicit kind prefix and (optionally)
// named arguments. Used for callable functions (locally declared and
// plugin-provided) where positional args alone would be ambiguous.
//
// Examples:
//
//	"${call:git-version()}"
//	  -> NamedCall{Kind: "call", Name: "git-version", Args: nil}
//
//	"${call:render-tpl(src='Dockerfile.tmpl', vars=config)}"
//	  -> NamedCall{
//	       Kind: "call", Name: "render-tpl",
//	       Args: [{Name: "src", Value: Literal{"Dockerfile.tmpl"}},
//	              {Name: "vars", Value: VarRef{"config"}}],
//	     }
//
// Note: the kind is captured for future extensibility (e.g. "tmpl:" or
// "secret:" prefixes), but currently only "call" is recognized.
// FuncCall remains the AST node for built-in function invocations like
// upper(x), starts_with(s, prefix) — those are positional and namespaced
// to the package's builtin set.
type NamedCall struct {
	P    Position
	Kind string // typically "call"
	Name string
	Args []NamedArg
}

// NamedArg is one entry in a NamedCall argument list. Three shapes:
//
//   - Bare positional:  Name == "",   IsSplat == false  -> args["0"], args["1"], ...
//   - Named:            Name != "",   IsSplat == false  -> args[Name]
//   - Splat:            Name == "",   IsSplat == true   -> Value must be a Map;
//                       evaluation spreads each (k, v) into the args map.
//
// Splat lets callers pass a pre-assembled Map of args, common in
// template rendering where the data shape is built once (perhaps by
// loading JSON or composing several upstream calls) and then handed
// to a template wholesale. Syntax: `${render:tmpl(...mapvar)}`.
type NamedArg struct {
	Name    string
	Value   Node
	IsSplat bool
}

func (n *NamedCall) Pos() Position { return n.P }
func (n *NamedCall) String() string {
	parts := make([]string, len(n.Args))
	for i, a := range n.Args {
		switch {
		case a.IsSplat:
			parts[i] = "..." + a.Value.String()
		case a.Name != "":
			parts[i] = a.Name + "=" + a.Value.String()
		default:
			parts[i] = a.Value.String()
		}
	}
	return n.Kind + ":" + n.Name + "(" + strings.Join(parts, ", ") + ")"
}
