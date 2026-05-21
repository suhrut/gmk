package expr

// Parsing entry points:
//
//   ParseTemplate(src, start) — for YAML var values: a string that may
//   contain ${...} substitutions mixed with literal text. Returns either
//   a *Literal (if no substitutions) or a *Concat of mixed parts.
//
//   ParseExpr(src, start) — for pure expression bodies (no surrounding
//   literal text), like Stage 4's when:-condition strings. Same grammar
//   as what appears inside a ${...}.
//
// Both return error wrapping ErrParse with file:line:col position info.
//
// The grammar is intentionally small for Stage 3a. Higher-precedence
// operators (&&, ||, comparison) get added in Stage 4 within this same
// package; the parser is hand-written recursive descent so extending it
// means adding a level to the precedence climb. No external grammar tool.

// ParseTemplate parses a string that may contain literal text interspersed
// with ${...} substitutions. The returned node evaluates to a single
// string at runtime.
//
// Examples:
//
//	"hello world"          -> *Literal{Value: String("hello world")}
//	"hello, ${name}!"      -> *Concat{Parts: [Literal, VarRef, Literal]}
//	"$$ is literal"        -> *Literal{Value: String("$ is literal")}
//	""                     -> *Literal{Value: String("")}
//
// $$ is an escape for a literal $. This matches Stage 2 behavior so all
// pre-existing YAML files keep working byte-identically.
func ParseTemplate(src string, start Position) (Node, error) {
	var parts []Node
	var lit []byte // accumulating literal bytes
	pos := start
	flushLiteral := func() {
		if len(lit) > 0 {
			parts = append(parts, &Literal{Value: NewString(string(lit))})
			lit = lit[:0]
		}
	}

	i := 0
	for i < len(src) {
		c := src[i]

		// $$ escape -> literal $
		if c == '$' && i+1 < len(src) && src[i+1] == '$' {
			lit = append(lit, '$')
			i += 2
			continue
		}

		// ${ begins a substitution
		if c == '$' && i+1 < len(src) && src[i+1] == '{' {
			flushLiteral()
			// Find matching '}', tracking braces inside string literals.
			end, err := findMatchingBrace(src, i+2)
			if err != nil {
				return nil, NewParseError(advancePos(start, src[:i]),
					"unterminated ${...}")
			}
			inner := src[i+2 : end]
			innerStart := advancePos(start, src[:i+2])
			node, perr := parseInner(inner, innerStart)
			if perr != nil {
				return nil, perr
			}
			parts = append(parts, node)
			i = end + 1
			continue
		}

		// Default: literal byte
		lit = append(lit, c)
		i++
	}
	flushLiteral()

	switch len(parts) {
	case 0:
		// Empty input -> empty literal.
		return &Literal{P: pos, Value: NewString("")}, nil
	case 1:
		// Pure literal OR a single ${...} (return the node itself, not
		// wrapped in Concat — preserves the substitution's own type when
		// it would otherwise be coerced to string by Concat).
		return parts[0], nil
	}
	return &Concat{P: pos, Parts: parts}, nil
}

// ParseExpr parses a pure expression body — what appears inside the
// braces of a ${...}. Returns nodes of the inner-expression grammar
// (VarRef, TypedRef, FuncCall, Pipeline, BinaryOp, Modifier, Literal).
func ParseExpr(src string, start Position) (Node, error) {
	return parseInner(src, start)
}

// findMatchingBrace returns the index of the } that closes a ${...}
// expression that begins at i (i.e. src[i-2:i] was "${"). Handles strings
// (which may contain '}' as literal) and nested braces.
func findMatchingBrace(src string, i int) (int, error) {
	depth := 1
	for i < len(src) {
		c := src[i]
		switch c {
		case '"':
			// Skip over string content with escape handling.
			i++
			for i < len(src) {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				i++
			}
		case '{':
			depth++
			i++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
			i++
		default:
			i++
		}
	}
	return 0, NewParseError(Position{}, "unterminated ${...}")
}

// parseInner is the inner-grammar parser. Owns a Lexer and consumes
// tokens until EOF.
func parseInner(src string, start Position) (Node, error) {
	lex := NewLexer(src, start)
	p := &parser{lex: lex, src: src, start: start}
	node, err := p.parsePipeline()
	if err != nil {
		return nil, err
	}
	// After a successful expression parse, we must be at EOF.
	tok, err := p.lex.Peek()
	if err != nil {
		return nil, err
	}
	if tok.Type != tokEOF {
		return nil, NewParseError(tok.Pos, "unexpected %s after expression", tok.Type)
	}
	return node, nil
}

// parser holds parsing state for one expression body.
type parser struct {
	lex   *Lexer
	src   string
	start Position
}

// parsePipeline parses comparison (| funccall_or_ident)*.
// Pipeline is the lowest precedence — left-associative.
func (p *parser) parsePipeline() (Node, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	var stages []*FuncCall
	for {
		tok, err := p.lex.Peek()
		if err != nil {
			return nil, err
		}
		if tok.Type != tokPipe {
			break
		}
		if _, err := p.lex.Next(); err != nil { // consume |
			return nil, err
		}
		// RHS must start with an identifier (function name).
		rhsTok, err := p.lex.Peek()
		if err != nil {
			return nil, err
		}
		if rhsTok.Type != tokIdent {
			return nil, NewParseError(rhsTok.Pos,
				"pipeline | must be followed by a function name, got %s", rhsTok.Type)
		}
		stage, err := p.parsePipelineStage()
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	if len(stages) == 0 {
		return left, nil
	}
	return &Pipeline{P: left.Pos(), Source: left, Stages: stages}, nil
}

// parsePipelineStage parses the RHS of a |: either a bare function name
// (called with implicit single arg from pipe) or an explicit fn(a, b)
// where the piped value prepends to the explicit args.
func (p *parser) parsePipelineStage() (*FuncCall, error) {
	tok, err := p.lex.Next()
	if err != nil {
		return nil, err
	}
	if tok.Type != tokIdent {
		return nil, NewParseError(tok.Pos, "expected function name, got %s", tok.Type)
	}
	name := tok.Value
	nameTok := tok
	next, err := p.lex.Peek()
	if err != nil {
		return nil, err
	}
	if next.Type != tokLParen {
		// Bare name: no explicit args (piped value is sole arg).
		return &FuncCall{P: nameTok.Pos, Name: name, Args: nil}, nil
	}
	// Explicit (a, b, ...) — piped value prepends to these at eval time.
	args, err := p.parseCallArgs()
	if err != nil {
		return nil, err
	}
	return &FuncCall{P: nameTok.Pos, Name: name, Args: args}, nil
}

// parseComparison parses primary (("==" | "!=") primary)?.
// Stage 3a only allows a single comparison per expression — Stage 4 will
// generalize to chains with && / || precedence.
func (p *parser) parseComparison() (Node, error) {
	left, err := p.parsePrimaryWithModifier()
	if err != nil {
		return nil, err
	}
	tok, err := p.lex.Peek()
	if err != nil {
		return nil, err
	}
	if tok.Type != tokEq && tok.Type != tokNeq {
		return left, nil
	}
	opTok, _ := p.lex.Next()
	right, err := p.parsePrimaryWithModifier()
	if err != nil {
		return nil, err
	}
	return &BinaryOp{P: opTok.Pos, Op: opTok.Value, Lhs: left, Rhs: right}, nil
}

// parsePrimaryWithModifier parses a primary and, if it's a ref, optionally
// wraps it in a Modifier. Modifiers only apply to refs (VarRef/TypedRef).
func (p *parser) parsePrimaryWithModifier() (Node, error) {
	prim, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	// Peek for modifier tokens.
	tok, err := p.lex.Peek()
	if err != nil {
		return nil, err
	}
	switch tok.Type {
	case tokModDef, tokModReq, tokModAlt:
	default:
		return prim, nil
	}
	// Validate target type.
	switch prim.(type) {
	case *VarRef, *TypedRef:
		// ok
	default:
		return nil, NewParseError(tok.Pos,
			"modifier %s can only be applied to a variable reference", tok.Type)
	}
	modTok, _ := p.lex.Next()
	// Read raw text until end of expression as the modifier arg.
	arg := trimSpace(p.lex.ReadRawToEnd())
	var kind ModifierKind
	switch modTok.Type {
	case tokModDef:
		kind = ModDefault
	case tokModReq:
		kind = ModRequired
	case tokModAlt:
		kind = ModAlternate
	}
	return &Modifier{P: prim.Pos(), Inner: prim, Kind: kind, Arg: arg}, nil
}

// parsePrimary parses a primary: literal, ref, typed ref, function call,
// or parenthesized expression.
func (p *parser) parsePrimary() (Node, error) {
	tok, err := p.lex.Next()
	if err != nil {
		return nil, err
	}
	switch tok.Type {
	case tokString:
		return &Literal{P: tok.Pos, Value: NewString(tok.Value)}, nil
	case tokInt:
		n, perr := strconvParseInt(tok.Value)
		if perr != nil {
			return nil, NewParseError(tok.Pos, "bad integer %q: %v", tok.Value, perr)
		}
		return &Literal{P: tok.Pos, Value: NewInt(n)}, nil
	case tokFloat:
		f, perr := strconvParseFloat(tok.Value)
		if perr != nil {
			return nil, NewParseError(tok.Pos, "bad float %q: %v", tok.Value, perr)
		}
		return &Literal{P: tok.Pos, Value: NewFloat(f)}, nil
	case tokTrue:
		return &Literal{P: tok.Pos, Value: NewBool(true)}, nil
	case tokFalse:
		return &Literal{P: tok.Pos, Value: NewBool(false)}, nil
	case tokLParen:
		// Parenthesized expression.
		inner, err := p.parsePipeline()
		if err != nil {
			return nil, err
		}
		closer, err := p.lex.Next()
		if err != nil {
			return nil, err
		}
		if closer.Type != tokRParen {
			return nil, NewParseError(closer.Pos, "expected ')' to close grouping, got %s", closer.Type)
		}
		return inner, nil
	case tokIdent:
		return p.parseIdentExpr(tok)
	}
	return nil, NewParseError(tok.Pos, "unexpected %s", tok.Type)
}

// parseIdentExpr handles whatever follows an identifier:
//
//	IDENT             -> VarRef
//	IDENT ":" path    -> TypedRef
//	IDENT "(" args ")"-> FuncCall
//	IDENT (bare keyword false in some places handled by caller)
func (p *parser) parseIdentExpr(idTok Token) (Node, error) {
	next, err := p.lex.Peek()
	if err != nil {
		return nil, err
	}
	switch next.Type {
	case tokColon:
		// Typed ref. Consume the colon then read a dotted path.
		if _, err := p.lex.Next(); err != nil {
			return nil, err
		}
		path, err := p.readPath()
		if err != nil {
			return nil, err
		}
		return &TypedRef{P: idTok.Pos, Kind: idTok.Value, Path: path}, nil
	case tokLParen:
		// Function call.
		args, err := p.parseCallArgs()
		if err != nil {
			return nil, err
		}
		return &FuncCall{P: idTok.Pos, Name: idTok.Value, Args: args}, nil
	}
	// Plain VarRef.
	return &VarRef{P: idTok.Pos, Name: idTok.Value}, nil
}

// readPath reads IDENT ("." IDENT)* and returns the joined form.
// Used for typed-ref paths like "has_tool.docker".
func (p *parser) readPath() (string, error) {
	tok, err := p.lex.Next()
	if err != nil {
		return "", err
	}
	if tok.Type != tokIdent {
		return "", NewParseError(tok.Pos, "expected identifier after ':', got %s", tok.Type)
	}
	path := tok.Value
	for {
		next, err := p.lex.Peek()
		if err != nil {
			return "", err
		}
		if next.Type != tokDot {
			break
		}
		if _, err := p.lex.Next(); err != nil {
			return "", err
		}
		seg, err := p.lex.Next()
		if err != nil {
			return "", err
		}
		if seg.Type != tokIdent {
			return "", NewParseError(seg.Pos, "expected identifier after '.', got %s", seg.Type)
		}
		path += "." + seg.Value
	}
	return path, nil
}

// parseCallArgs consumes "(" args ")" where args is a comma-separated list
// of expressions. The opening "(" must be the next token.
func (p *parser) parseCallArgs() ([]Node, error) {
	openTok, err := p.lex.Next()
	if err != nil {
		return nil, err
	}
	if openTok.Type != tokLParen {
		return nil, NewParseError(openTok.Pos, "expected '(' for function call, got %s", openTok.Type)
	}
	var args []Node
	// Allow empty: ()
	peek, err := p.lex.Peek()
	if err != nil {
		return nil, err
	}
	if peek.Type == tokRParen {
		if _, err := p.lex.Next(); err != nil {
			return nil, err
		}
		return args, nil
	}
	for {
		arg, err := p.parsePipeline()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		sep, err := p.lex.Next()
		if err != nil {
			return nil, err
		}
		switch sep.Type {
		case tokRParen:
			return args, nil
		case tokComma:
			continue
		default:
			return nil, NewParseError(sep.Pos, "expected ',' or ')', got %s", sep.Type)
		}
	}
}

// advancePos returns a Position that is start advanced by walking through
// the bytes of consumed. Used to attribute positions to nodes deep inside
// templates.
func advancePos(start Position, consumed string) Position {
	pos := start
	for i := 0; i < len(consumed); i++ {
		if consumed[i] == '\n' {
			pos.Line++
			pos.Col = 1
		} else {
			pos.Col++
		}
	}
	return pos
}
