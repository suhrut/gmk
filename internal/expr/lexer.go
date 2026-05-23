package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// TokenType enumerates the kinds of tokens the inner-expression lexer
// emits. The outer template scanner (in parser.go) operates at character
// level over literal text and ${...} regions, not via tokens.
type TokenType int

const (
	tokEOF TokenType = iota
	tokIdent
	tokString  // "..." literal
	tokInt     // decimal integer
	tokFloat   // decimal with .
	tokLParen  // (
	tokRParen  // )
	tokLBrack  // [   (Stage 3b: index access)
	tokRBrack  // ]
	tokColon   // :
	tokDot     // .
	tokPipe    // |
	tokEq      // ==
	tokNeq     // !=
	tokAssign  // =   (Stage 3b: named call args, e.g. fn(key=val))
	tokComma   // ,
	tokModDef  // :-   (default modifier)
	tokModReq  // :?   (required modifier)
	tokModAlt  // :+   (alternate modifier)
	tokTrue    // true
	tokFalse   // false
	tokSplat   // ...   (Stage 3c.1.1: splat in named-call args, e.g. fn(...mapvar))
)

func (t TokenType) String() string {
	switch t {
	case tokEOF:
		return "EOF"
	case tokIdent:
		return "ident"
	case tokString:
		return "string"
	case tokInt:
		return "int"
	case tokFloat:
		return "float"
	case tokLParen:
		return "("
	case tokRParen:
		return ")"
	case tokLBrack:
		return "["
	case tokRBrack:
		return "]"
	case tokColon:
		return ":"
	case tokDot:
		return "."
	case tokPipe:
		return "|"
	case tokEq:
		return "=="
	case tokNeq:
		return "!="
	case tokAssign:
		return "="
	case tokComma:
		return ","
	case tokModDef:
		return ":-"
	case tokModReq:
		return ":?"
	case tokModAlt:
		return ":+"
	case tokTrue:
		return "true"
	case tokFalse:
		return "false"
	case tokSplat:
		return "..."
	default:
		return "?"
	}
}

// Token is a single lexed unit. Value carries the original text (or, for
// strings, the unescaped contents). Pos points at the token's start.
type Token struct {
	Type  TokenType
	Value string
	Pos   Position
}

// Lexer tokenizes the body of a single ${...} expression (no template-level
// literal text — the parser handles that at character level).
//
// Position tracking: the lexer is given a starting Position (where the
// expression body begins in the source YAML) and tracks line/col offsets
// from there. The expression body itself is single-line in 99% of cases;
// multi-line bodies (rare, only via folded/literal block scalars) get
// approximate positions that point at the start of the value.
type Lexer struct {
	src    string
	pos    int      // byte index into src
	start  Position // where src[0] is anchored in the source file
	tokens []Token  // emitted tokens (lazy); built up by Next/Peek
	cursor int      // index into tokens for next Peek/Next
	eof    bool     // set when we've tokenized through end of src
}

// NewLexer constructs a Lexer for the given expression body.
// start describes where src[0] lives in the underlying YAML file (so
// produced tokens carry correct positions for error messages).
func NewLexer(src string, start Position) *Lexer {
	return &Lexer{src: src, start: start}
}

// currentPos returns the Position of the current byte index in src.
// Tracks newlines through the bytes seen so far.
func (l *Lexer) currentPos() Position {
	pos := l.start
	for i := 0; i < l.pos; i++ {
		if l.src[i] == '\n' {
			pos.Line++
			pos.Col = 1
		} else {
			pos.Col++
		}
	}
	return pos
}

// Peek returns the next token without consuming it.
func (l *Lexer) Peek() (Token, error) {
	if l.cursor >= len(l.tokens) {
		t, err := l.advance()
		if err != nil {
			return Token{}, err
		}
		l.tokens = append(l.tokens, t)
	}
	return l.tokens[l.cursor], nil
}

// Next consumes and returns the next token.
func (l *Lexer) Next() (Token, error) {
	t, err := l.Peek()
	if err != nil {
		return Token{}, err
	}
	l.cursor++
	return t, nil
}

// SavePos returns an opaque cursor position that can be passed to
// RestorePos to rewind the lexer. Used by the parser when it needs
// multi-token lookahead — e.g. distinguishing "ident = expr" (named
// arg) from "expr" (positional arg) in a NamedCall arg list.
//
// The implementation is cheap (just the cursor index) because tokens
// are buffered in l.tokens; rewinding the cursor doesn't lose them.
func (l *Lexer) SavePos() int { return l.cursor }

// RestorePos rewinds the lexer to a previously-saved position. Tokens
// past the new cursor stay buffered and will be re-emitted by Peek/Next.
func (l *Lexer) RestorePos(c int) { l.cursor = c }

// ReadRawToEnd returns whatever raw text remains in the input, advancing
// the lexer to EOF. Used by the parser to capture modifier arguments
// (:-default, :?msg, :+alt) as literal text up to the closing brace
// (which the template scanner already trimmed off).
func (l *Lexer) ReadRawToEnd() string {
	// Note: this also invalidates any cached tokens past the cursor.
	rest := l.src[l.pos:]
	l.pos = len(l.src)
	l.eof = true
	// Drop any lookahead so subsequent Peek/Next yield EOF.
	l.tokens = l.tokens[:l.cursor]
	return rest
}

// advance is the inner tokenizer: produces the next token from src starting
// at l.pos.
func (l *Lexer) advance() (Token, error) {
	l.skipWhitespace()
	if l.pos >= len(l.src) {
		return Token{Type: tokEOF, Pos: l.currentPos()}, nil
	}

	startPos := l.currentPos()
	c := l.src[l.pos]

	switch {
	case c == '(':
		l.pos++
		return Token{Type: tokLParen, Value: "(", Pos: startPos}, nil
	case c == ')':
		l.pos++
		return Token{Type: tokRParen, Value: ")", Pos: startPos}, nil
	case c == '[':
		l.pos++
		return Token{Type: tokLBrack, Value: "[", Pos: startPos}, nil
	case c == ']':
		l.pos++
		return Token{Type: tokRBrack, Value: "]", Pos: startPos}, nil
	case c == ',':
		l.pos++
		return Token{Type: tokComma, Value: ",", Pos: startPos}, nil
	case c == '.':
		// Three-dot splat (...mapvar in named-call args) vs single dot
		// (field access like obj.field). The longer match wins.
		if l.pos+2 < len(l.src) && l.src[l.pos+1] == '.' && l.src[l.pos+2] == '.' {
			l.pos += 3
			return Token{Type: tokSplat, Value: "...", Pos: startPos}, nil
		}
		l.pos++
		return Token{Type: tokDot, Value: ".", Pos: startPos}, nil
	case c == '|':
		l.pos++
		return Token{Type: tokPipe, Value: "|", Pos: startPos}, nil
	case c == ':':
		// :- :? :+ or bare :
		if l.pos+1 < len(l.src) {
			switch l.src[l.pos+1] {
			case '-':
				l.pos += 2
				return Token{Type: tokModDef, Value: ":-", Pos: startPos}, nil
			case '?':
				l.pos += 2
				return Token{Type: tokModReq, Value: ":?", Pos: startPos}, nil
			case '+':
				l.pos += 2
				return Token{Type: tokModAlt, Value: ":+", Pos: startPos}, nil
			}
		}
		l.pos++
		return Token{Type: tokColon, Value: ":", Pos: startPos}, nil
	case c == '=':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: tokEq, Value: "==", Pos: startPos}, nil
		}
		// Single '=' is used for named call args: fn(key=value).
		l.pos++
		return Token{Type: tokAssign, Value: "=", Pos: startPos}, nil
	case c == '!':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: tokNeq, Value: "!=", Pos: startPos}, nil
		}
		return Token{}, NewParseError(startPos, "unexpected '!'; did you mean '!='?")
	case c == '"':
		return l.readString(startPos)
	case c == '-' || (c >= '0' && c <= '9'):
		return l.readNumber(startPos)
	case isIdentStart(c):
		return l.readIdent(startPos)
	}

	return Token{}, NewParseError(startPos, "unexpected character %q", c)
}

// skipWhitespace advances past spaces, tabs, and newlines.
func (l *Lexer) skipWhitespace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			l.pos++
			continue
		}
		return
	}
}

// readString reads a "..." literal, handling \\, \", \n, \t, \r escapes.
func (l *Lexer) readString(startPos Position) (Token, error) {
	l.pos++ // consume opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '"' {
			l.pos++
			return Token{Type: tokString, Value: b.String(), Pos: startPos}, nil
		}
		if c == '\\' && l.pos+1 < len(l.src) {
			next := l.src[l.pos+1]
			switch next {
			case '"', '\\':
				b.WriteByte(next)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				return Token{}, NewParseError(l.currentPos(), "unknown escape \\%c", next)
			}
			l.pos += 2
			continue
		}
		b.WriteByte(c)
		l.pos++
	}
	return Token{}, NewParseError(startPos, "unterminated string literal")
}

// readNumber reads an integer or float. Tokens for negative numbers begin
// with '-'. We don't try to disambiguate "subtraction" vs "negative number"
// at lex time because Stage 3a has no subtraction operator (Stage 4 adds it
// and may revisit).
func (l *Lexer) readNumber(startPos Position) (Token, error) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
		if l.pos >= len(l.src) || l.src[l.pos] < '0' || l.src[l.pos] > '9' {
			return Token{}, NewParseError(startPos, "unexpected '-'")
		}
	}
	isFloat := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c >= '0' && c <= '9' {
			l.pos++
			continue
		}
		if c == '.' && !isFloat && l.pos+1 < len(l.src) && l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9' {
			isFloat = true
			l.pos++
			continue
		}
		break
	}
	text := l.src[start:l.pos]
	if isFloat {
		return Token{Type: tokFloat, Value: text, Pos: startPos}, nil
	}
	return Token{Type: tokInt, Value: text, Pos: startPos}, nil
}

// readIdent reads an identifier or a keyword (true, false). Identifiers
// allow internal hyphens (e.g. "git-version") but a hyphen must be
// followed by an identifier character; "x-" alone reads only "x" and
// leaves the "-" for the next lexer call.
func (l *Lexer) readIdent(startPos Position) (Token, error) {
	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '-' {
			// Lookahead: only consume the '-' if followed by an ident char.
			if l.pos+1 >= len(l.src) || !isIdentStartOrDigit(l.src[l.pos+1]) {
				break
			}
			l.pos++
			continue
		}
		if !isIdentCont(c) {
			break
		}
		l.pos++
	}
	text := l.src[start:l.pos]
	switch text {
	case "true":
		return Token{Type: tokTrue, Value: text, Pos: startPos}, nil
	case "false":
		return Token{Type: tokFalse, Value: text, Pos: startPos}, nil
	}
	return Token{Type: tokIdent, Value: text, Pos: startPos}, nil
}

// isIdentStartOrDigit is the set of chars that may follow a hyphen
// inside an identifier (alpha/underscore or digit). Used by readIdent
// to decide whether a `-` is part of the current ident or terminates it.
func isIdentStartOrDigit(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isIdentCont allows hyphens AFTER the first character so identifiers
// like "git-version", "render-tpl", and target names can be lexed as
// single tokens. Identifiers cannot START with '-' (which would be
// ambiguous with negative numbers). Within an identifier, a hyphen
// must be followed by another identifier char — we enforce this in
// readIdent so "x-1" still lexes as "x-1" (legal) but "x-" alone
// terminates after "x".
func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}

// Used in error messages from callers; kept here so the package stays
// self-contained and we don't accidentally pull in fmt all over.
var _ = fmt.Sprintf
var _ = strconv.Atoi
