// YAML AST → expr.Value adapter.
//
// Stage 3c.2 introduces structured values for vars: and prelude: blocks,
// so a project can write
//
//   vars:
//     db_config:
//       host: "db.internal"
//       port: 5432
//
// and have ${db_config.host} / ${db_config.port} resolve correctly. The
// converter walks any YAML node and returns the corresponding expr.Value:
//
//   scalar string   → StringKind
//   scalar int      → IntKind
//   scalar float    → FloatKind
//   scalar bool     → BoolKind
//   null            → NoneKind
//   mapping         → MapKind (recursive)
//   sequence        → ListKind (recursive)
//
// Restriction (Stage 3c.2): leaf strings must be LITERAL — no ${...}
// interpolation. If a leaf string contains ${...}, the converter returns
// a clear error. The intent is to keep the lookup path simple (return
// the cached Value as-is) without introducing lazy/deep template
// resolution semantics. Users who need interpolation inside structured
// data should keep using a function returning JSON — that path is well-
// supported and the templates examples already showcase it.
//
// Tagged values (!sh, !env, etc.) are rejected with ErrTaggedValue at
// the call site, same as today's scalar var loading. The converter only
// sees untagged nodes (callers should Untag first if needed).
package load

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/suhrut/gmk/internal/expr"
)

// IsMapping reports whether the value is a YAML mapping. Used by callers
// (loadVarsBlock, loadPrelude) to branch between scalar-string and
// structured-value handling.
func (v yamlValue) IsMapping() bool {
	u := v.Untag()
	switch u.Node.(type) {
	case *ast.MappingNode, *ast.MappingValueNode:
		return true
	}
	return false
}

// IsSequence reports whether the value is a YAML sequence.
func (v yamlValue) IsSequence() bool {
	u := v.Untag()
	_, ok := u.Node.(*ast.SequenceNode)
	return ok
}

// IsStructured reports whether the value is either a mapping or a
// sequence (the two composite forms structured-value loading handles).
func (v yamlValue) IsStructured() bool {
	return v.IsMapping() || v.IsSequence()
}

// ToStructuredValue converts a yamlValue to an expr.Value, recursively.
// Returns an error if a leaf string contains a ${...} expression (the
// Stage 3c.2 deferral noted in the package doc).
//
// contextPath is a dotted path used for error messages
// (e.g. "vars.db_config.host"). The caller seeds it with the top-level
// context; recursion appends keys and indices.
func (v yamlValue) ToStructuredValue(contextPath string) (expr.Value, error) {
	if v.IsNull() {
		return expr.NewNone(), nil
	}
	u := v.Untag()
	switch n := u.Node.(type) {

	case *ast.StringNode:
		if err := rejectTemplateExpr(n.Value, contextPath, v.File, v.Line, v.Col); err != nil {
			return expr.NewNone(), err
		}
		return expr.NewString(n.Value), nil

	case *ast.LiteralNode:
		// | and > block scalars are also strings.
		raw := ""
		if n.Value != nil {
			raw = n.Value.Value
		}
		if err := rejectTemplateExpr(raw, contextPath, v.File, v.Line, v.Col); err != nil {
			return expr.NewNone(), err
		}
		return expr.NewString(raw), nil

	case *ast.IntegerNode:
		// goccy/go-yaml's IntegerNode.Value is `any` — usually int64
		// or uint64. Coerce via type switch; fall back to a string
		// representation if a future yaml lib upgrade adds shapes.
		switch x := n.Value.(type) {
		case int64:
			return expr.NewInt(x), nil
		case uint64:
			return expr.NewInt(int64(x)), nil
		case int:
			return expr.NewInt(int64(x)), nil
		}
		return expr.NewString(fmt.Sprintf("%v", n.Value)), nil

	case *ast.FloatNode:
		// FloatNode.Value in goccy/go-yaml is a concrete float64
		// (not `any`). Wrap directly.
		return expr.NewFloat(n.Value), nil

	case *ast.BoolNode:
		return expr.NewBool(n.Value), nil

	case *ast.NullNode:
		return expr.NewNone(), nil

	case *ast.MappingNode, *ast.MappingValueNode:
		m, err := v.AsMapping()
		if err != nil {
			return expr.NewNone(), err
		}
		out := make(map[string]expr.Value, len(m.Entries))
		for _, kv := range m.Entries {
			child := contextPath
			if child != "" {
				child += "."
			}
			child += kv.Key
			val, cerr := kv.Value.ToStructuredValue(child)
			if cerr != nil {
				return expr.NewNone(), cerr
			}
			out[kv.Key] = val
		}
		return expr.NewMap(out), nil

	case *ast.SequenceNode:
		seq, err := v.AsSequence()
		if err != nil {
			return expr.NewNone(), err
		}
		out := make([]expr.Value, 0, len(seq.Items))
		for i, item := range seq.Items {
			child := fmt.Sprintf("%s[%d]", contextPath, i)
			val, cerr := item.ToStructuredValue(child)
			if cerr != nil {
				return expr.NewNone(), cerr
			}
			out = append(out, val)
		}
		return expr.NewList(out), nil
	}

	return expr.NewNone(), fmt.Errorf("%s:%d:%d: %s: unsupported YAML node kind %s for structured value",
		v.File, v.Line, v.Col, contextPath, nodeKindName(u.Node))
}

// rejectTemplateExpr returns an error if the string contains a ${...}
// substitution. Stage 3c.2 doesn't support template expressions in leaf
// strings of structured values; users should use a function returning
// JSON for that pattern. This restriction is conservative — a future
// stage may lift it once we settle on resolution-order semantics for
// nested var-to-var references inside structured data.
func rejectTemplateExpr(s, contextPath, file string, line, col int) error {
	if !strings.Contains(s, "${") {
		return nil
	}
	return fmt.Errorf("%s:%d:%d: %s: structured value leaf string contains ${...} expression, which is not supported yet (use a function returning JSON for interpolated structured data)",
		file, line, col, contextPath)
}
