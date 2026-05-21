// Package-internal: goccy/go-yaml AST adapter.
//
// All direct usage of github.com/goccy/go-yaml/ast and /parser is confined
// to this file. The rest of the load package operates on the simple
// Go-native types defined here (yamlValue, yamlMap, yamlMapEntry,
// yamlSeq). The motivation:
//
//   1. Swapping YAML libraries (or upgrading goccy across breaking changes)
//      only touches this file.
//   2. The load logic stays readable — no goccy AST type-switches scattered
//      around the IR-building code.
//   3. Stage 3b can extend yamlValue with tag-detection helpers without
//      load.go needing structural changes.

package load

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
)

// yamlValue represents a parsed YAML node along with its source position.
// It wraps the raw ast.Node so tag detection (Stage 3b) and structural
// inspection can both work off the same handle.
type yamlValue struct {
	Node ast.Node // raw node; nil for "missing value"
	File string
	Line int
	Col  int
}

// yamlMap is a YAML mapping with entries in declaration order.
type yamlMap struct {
	Entries []yamlMapEntry
	File    string
	Line    int
	Col     int
}

// yamlMapEntry is a single key/value pair from a mapping.
type yamlMapEntry struct {
	Key     string
	KeyLine int
	KeyCol  int
	Value   yamlValue
}

// yamlSeq is a YAML sequence.
type yamlSeq struct {
	Items []yamlValue
	File  string
	Line  int
	Col   int
}

// parseYAMLFile reads a YAML file from disk and returns its top-level
// value. For files with multiple documents, only the first is used.
func parseYAMLFile(path string) (yamlValue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return yamlValue{}, fmt.Errorf("read %s: %w", path, err)
	}
	f, err := parser.ParseBytes(data, 0)
	if err != nil {
		return yamlValue{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if f == nil || len(f.Docs) == 0 {
		// Empty file is valid; treated as null top-level.
		return yamlValue{File: path}, nil
	}
	doc := f.Docs[0]
	if doc == nil || doc.Body == nil {
		return yamlValue{File: path}, nil
	}
	return wrapNode(doc.Body, path), nil
}

// wrapNode builds a yamlValue from a raw ast.Node, extracting position info.
func wrapNode(n ast.Node, file string) yamlValue {
	v := yamlValue{Node: n, File: file}
	if n != nil {
		if tok := n.GetToken(); tok != nil && tok.Position != nil {
			v.Line = tok.Position.Line
			v.Col = tok.Position.Column
		}
	}
	return v
}

// IsNull reports whether the value is missing or YAML null.
func (v yamlValue) IsNull() bool {
	if v.Node == nil {
		return true
	}
	_, isNull := v.Node.(*ast.NullNode)
	return isNull
}

// Tag returns the YAML tag attached to this value (e.g. "!sh", "!env"),
// or "" if the value is untagged. Stage 3a returns the tag for error
// reporting only; Stage 3b will dispatch on it.
func (v yamlValue) Tag() string {
	if v.Node == nil {
		return ""
	}
	tn, ok := v.Node.(*ast.TagNode)
	if !ok {
		return ""
	}
	if tn.Start == nil {
		return ""
	}
	return tn.Start.Value
}

// Untag returns the value with any TagNode wrapper removed, so the
// caller can inspect the wrapped scalar/sequence.
func (v yamlValue) Untag() yamlValue {
	if v.Node == nil {
		return v
	}
	if tn, ok := v.Node.(*ast.TagNode); ok {
		return wrapNode(tn.Value, v.File)
	}
	return v
}

// AsMapping converts the value to a yamlMap. Returns an error if the
// value is not a mapping.
func (v yamlValue) AsMapping() (*yamlMap, error) {
	if v.IsNull() {
		return &yamlMap{File: v.File, Line: v.Line, Col: v.Col}, nil
	}
	u := v.Untag()
	if mn, ok := u.Node.(*ast.MappingNode); ok {
		entries := make([]yamlMapEntry, 0, len(mn.Values))
		for _, kv := range mn.Values {
			e, err := mapEntryFromKV(kv, v.File)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
		return &yamlMap{Entries: entries, File: v.File, Line: v.Line, Col: v.Col}, nil
	}
	// Single-entry mapping sometimes parses as MappingValueNode directly.
	if kv, ok := u.Node.(*ast.MappingValueNode); ok {
		e, err := mapEntryFromKV(kv, v.File)
		if err != nil {
			return nil, err
		}
		return &yamlMap{Entries: []yamlMapEntry{e}, File: v.File, Line: v.Line, Col: v.Col}, nil
	}
	return nil, fmt.Errorf("%s:%d:%d: expected mapping, got %s",
		v.File, v.Line, v.Col, nodeKindName(u.Node))
}

// AsSequence converts the value to a yamlSeq. Returns an error if not a
// sequence (or null, which yields an empty sequence).
func (v yamlValue) AsSequence() (*yamlSeq, error) {
	if v.IsNull() {
		return &yamlSeq{File: v.File, Line: v.Line, Col: v.Col}, nil
	}
	u := v.Untag()
	sn, ok := u.Node.(*ast.SequenceNode)
	if !ok {
		return nil, fmt.Errorf("%s:%d:%d: expected sequence, got %s",
			v.File, v.Line, v.Col, nodeKindName(u.Node))
	}
	items := make([]yamlValue, 0, len(sn.Values))
	for _, item := range sn.Values {
		items = append(items, wrapNode(item, v.File))
	}
	return &yamlSeq{Items: items, File: v.File, Line: v.Line, Col: v.Col}, nil
}

// AsString extracts a scalar string from the value. Coerces numbers,
// bools, and null to their YAML-canonical string form (matching the
// Stage 2 behavior of yaml.Unmarshal into string).
//
// Returns an error if the value is a mapping or sequence — those can't
// reasonably be flattened to a single string.
func (v yamlValue) AsString() (string, error) {
	if v.IsNull() {
		return "", nil
	}
	u := v.Untag()
	switch n := u.Node.(type) {
	case *ast.StringNode:
		return n.Value, nil
	case *ast.IntegerNode:
		return fmt.Sprintf("%v", n.Value), nil
	case *ast.FloatNode:
		return fmt.Sprintf("%v", n.Value), nil
	case *ast.BoolNode:
		if n.Value {
			return "true", nil
		}
		return "false", nil
	case *ast.LiteralNode:
		// | or > block scalars.
		if n.Value != nil {
			return n.Value.Value, nil
		}
		return "", nil
	case *ast.NullNode:
		return "", nil
	}
	return "", fmt.Errorf("%s:%d:%d: expected scalar, got %s",
		v.File, v.Line, v.Col, nodeKindName(u.Node))
}

// AsBool extracts a scalar bool. Accepts native YAML booleans and the
// common string forms "true"/"false" for compatibility.
func (v yamlValue) AsBool() (bool, error) {
	if v.IsNull() {
		return false, nil
	}
	u := v.Untag()
	switch n := u.Node.(type) {
	case *ast.BoolNode:
		return n.Value, nil
	case *ast.StringNode:
		switch n.Value {
		case "true", "True", "TRUE", "yes", "on":
			return true, nil
		case "false", "False", "FALSE", "no", "off":
			return false, nil
		}
		return false, fmt.Errorf("%s:%d:%d: cannot parse %q as bool", v.File, v.Line, v.Col, n.Value)
	}
	return false, fmt.Errorf("%s:%d:%d: expected bool, got %s", v.File, v.Line, v.Col, nodeKindName(u.Node))
}

// mapEntryFromKV converts a goccy MappingValueNode to a yamlMapEntry.
func mapEntryFromKV(kv *ast.MappingValueNode, file string) (yamlMapEntry, error) {
	if kv == nil {
		return yamlMapEntry{}, fmt.Errorf("nil mapping entry")
	}
	keyText, keyLine, keyCol, err := keyAsString(kv.Key, file)
	if err != nil {
		return yamlMapEntry{}, err
	}
	return yamlMapEntry{
		Key:     keyText,
		KeyLine: keyLine,
		KeyCol:  keyCol,
		Value:   wrapNode(kv.Value, file),
	}, nil
}

// keyAsString extracts the string form of a mapping key.
func keyAsString(k ast.MapKeyNode, file string) (string, int, int, error) {
	if k == nil {
		return "", 0, 0, fmt.Errorf("%s: missing mapping key", file)
	}
	line, col := tokenLineCol(k.GetToken())
	switch n := k.(type) {
	case *ast.StringNode:
		return n.Value, line, col, nil
	case *ast.IntegerNode:
		return fmt.Sprintf("%v", n.Value), line, col, nil
	case *ast.BoolNode:
		if n.Value {
			return "true", line, col, nil
		}
		return "false", line, col, nil
	}
	// Fall back to String() for less common key node types.
	return k.String(), line, col, nil
}

// tokenLineCol returns line/col for a token, defaulting to 0/0 if nil.
func tokenLineCol(t *token.Token) (int, int) {
	if t == nil || t.Position == nil {
		return 0, 0
	}
	return t.Position.Line, t.Position.Column
}

// nodeKindName returns a short name for an ast.Node's concrete type, used
// in error messages.
func nodeKindName(n ast.Node) string {
	if n == nil {
		return "null"
	}
	switch n.(type) {
	case *ast.MappingNode, *ast.MappingValueNode:
		return "mapping"
	case *ast.SequenceNode:
		return "sequence"
	case *ast.StringNode:
		return "string"
	case *ast.IntegerNode:
		return "integer"
	case *ast.FloatNode:
		return "float"
	case *ast.BoolNode:
		return "bool"
	case *ast.NullNode:
		return "null"
	case *ast.LiteralNode:
		return "literal-block"
	case *ast.TagNode:
		return "tagged-value"
	}
	return fmt.Sprintf("%T", n)
}
