package load

// Stage 3b loader extensions: functions:, languages:, prelude:.
//
// These share infrastructure with the existing target loader — same YAML
// AST walker, same expression parser, same error idiom. The new shapes:
//
//   functions:
//     git-version:
//       doc: "Return the current git tag and short SHA."
//       params:
//         - name: dir
//           type: string
//           default: "."
//           doc: "Repository directory."
//       result:
//         type: map
//         doc: "{tag, sha, dirty}"
//       prelude:
//         dir_abs: "${call:abs-path(${dir})}"
//       script: |
//         cd "$(jq -r .dir_abs "$GMK_PRELUDE")" ...
//       lang: bash
//
//   languages:
//     zsh:
//       interpreter: zsh
//       args: ["-e"]
//       ext: ".zsh"

import (
	"fmt"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
)

// loadFunctions walks the top-level `functions:` block and adds each
// entry to the project. Functions are kept in declaration order via
// Project.FunctionOrder for deterministic listing.
func loadFunctions(p *ir.Project, entry yamlMapEntry, file string) error {
	m, err := entry.Value.AsMapping()
	if err != nil {
		return fmt.Errorf("load %s: functions: %w", file, err)
	}
	for _, kv := range m.Entries {
		// Function names share the target namespace so users can't
		// accidentally shadow a target with a function or vice versa.
		// (gmk call <name> would otherwise be ambiguous.)
		if _, exists := p.Targets[kv.Key]; exists {
			return fmt.Errorf("load %s:%d:%d: function %q collides with target of same name",
				file, kv.KeyLine, kv.KeyCol, kv.Key)
		}
		if _, exists := p.Functions[kv.Key]; exists {
			return fmt.Errorf("load %s:%d:%d: function %q declared more than once",
				file, kv.KeyLine, kv.KeyCol, kv.Key)
		}
		fn, err := loadFunction(kv, file)
		if err != nil {
			return err
		}
		p.Functions[fn.Name] = fn
		p.FunctionOrder = append(p.FunctionOrder, fn.Name)
	}
	return nil
}

// loadFunction parses one function entry into ir.Function. Closed schema:
// only the documented keys are accepted; unknown keys produce a clear
// error so typos in `paramz` or `defualt` surface immediately.
func loadFunction(kv yamlMapEntry, file string) (*ir.Function, error) {
	name := kv.Key
	body, err := kv.Value.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: function %q: %w", file, name, err)
	}
	fn := &ir.Function{
		Name:   name,
		Lang:   "bash",
		Source: ir.SourceLoc{File: file, Line: kv.Value.Line, Column: kv.Value.Col},
	}
	allowed := map[string]bool{
		"doc":     true,
		"params":  true,
		"result":  true,
		"prelude": true,
		"run":     true, // alias for script
		"script":  true,
		"lang":    true,
		"env":     true,
		"cwd":     true,
	}
	for _, e := range body.Entries {
		if !allowed[e.Key] {
			return nil, fmt.Errorf("load %s:%d:%d: function %q has unknown field %q",
				file, e.KeyLine, e.KeyCol, name, e.Key)
		}
		switch e.Key {
		case "doc":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.doc: %w", file, name, err)
			}
			fn.Doc = s
		case "params":
			ps, err := loadFunctionParams(e.Value, file, name)
			if err != nil {
				return nil, err
			}
			fn.Params = ps
		case "result":
			r, err := loadFunctionResult(e.Value, file, name)
			if err != nil {
				return nil, err
			}
			fn.Result = r
		case "prelude":
			pre, err := loadPrelude(e.Value, file, "function "+name)
			if err != nil {
				return nil, err
			}
			fn.Prelude = pre
		case "run", "script":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.%s: %w", file, name, e.Key, err)
			}
			if fn.Run != "" {
				return nil, fmt.Errorf("load %s:%d:%d: function %q: "+
					"cannot set both 'run' and 'script' — pick one",
					file, e.KeyLine, e.KeyCol, name)
			}
			fn.Run = s
		case "lang":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.lang: %w", file, name, err)
			}
			if s != "" {
				fn.Lang = s
			}
		case "env":
			em, err := e.Value.AsMapping()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.env: %w", file, name, err)
			}
			if fn.Env == nil {
				fn.Env = make(map[string]string, len(em.Entries))
			}
			for _, envKV := range em.Entries {
				s, err := envKV.Value.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: function %q.env.%s: %w", file, name, envKV.Key, err)
				}
				fn.Env[envKV.Key] = s
			}
		case "cwd":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.cwd: %w", file, name, err)
			}
			fn.Cwd = s
		}
	}
	// A function with no body and no prelude is meaningless.
	if fn.Run == "" && len(fn.Prelude) == 0 {
		return nil, fmt.Errorf("validate %s: function %q must have either 'prelude' or 'script'/'run'",
			file, name)
	}
	return fn, nil
}

// loadFunctionParams parses the params: sequence into FunctionParam entries.
// Schema per entry:
//
//	- name: <ident>      required
//	  type: <type>       required (string|int|float|bool|list|map)
//	  default: <value>   optional (expression evaluated at call time)
//	  doc: <string>      optional
func loadFunctionParams(node yamlValue, file, funcName string) ([]ir.FunctionParam, error) {
	seq, err := node.AsSequence()
	if err != nil {
		return nil, fmt.Errorf("load %s: function %q.params must be a sequence: %w", file, funcName, err)
	}
	out := make([]ir.FunctionParam, 0, len(seq.Items))
	seenNames := make(map[string]bool, len(seq.Items))
	for i, item := range seq.Items {
		pm, err := item.AsMapping()
		if err != nil {
			return nil, fmt.Errorf("load %s: function %q.params[%d]: %w", file, funcName, i, err)
		}
		var p ir.FunctionParam
		paramAllowed := map[string]bool{"name": true, "type": true, "default": true, "doc": true}
		for _, kv := range pm.Entries {
			if !paramAllowed[kv.Key] {
				return nil, fmt.Errorf("load %s:%d:%d: function %q.params[%d]: unknown field %q",
					file, kv.KeyLine, kv.KeyCol, funcName, i, kv.Key)
			}
			switch kv.Key {
			case "name":
				s, err := kv.Value.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: function %q.params[%d].name: %w", file, funcName, i, err)
				}
				p.Name = s
			case "type":
				s, err := kv.Value.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: function %q.params[%d].type: %w", file, funcName, i, err)
				}
				if !isValidParamType(s) {
					return nil, fmt.Errorf("load %s:%d:%d: function %q.params[%d].type: "+
						"invalid type %q (want string|int|float|bool|list|map)",
						file, kv.KeyLine, kv.KeyCol, funcName, i, s)
				}
				p.Type = s
			case "default":
				raw, err := kv.Value.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: function %q.params[%d].default: %w", file, funcName, i, err)
				}
				// Defaults parse as expression templates so they can
				// reference other prelude bindings (or static literals).
				node, perr := expr.ParseTemplate(raw, expr.Position{
					File: file, Line: kv.Value.Line, Col: kv.Value.Col,
				})
				if perr != nil {
					return nil, fmt.Errorf("load %s: function %q.params[%d].default: %w", file, funcName, i, perr)
				}
				p.Default = &node
			case "doc":
				s, err := kv.Value.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: function %q.params[%d].doc: %w", file, funcName, i, err)
				}
				p.Doc = s
			}
		}
		if p.Name == "" {
			return nil, fmt.Errorf("load %s: function %q.params[%d]: missing required field 'name'",
				file, funcName, i)
		}
		if p.Type == "" {
			return nil, fmt.Errorf("load %s: function %q.params[%d]: missing required field 'type'",
				file, funcName, i)
		}
		if seenNames[p.Name] {
			return nil, fmt.Errorf("load %s: function %q.params: duplicate parameter %q",
				file, funcName, p.Name)
		}
		seenNames[p.Name] = true
		out = append(out, p)
	}
	return out, nil
}

// loadFunctionResult parses the result: block. Both fields optional;
// at minimum either type or doc should be present, but we don't enforce —
// a result: block with neither is just an empty annotation.
func loadFunctionResult(node yamlValue, file, funcName string) (*ir.FunctionResult, error) {
	m, err := node.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: function %q.result: %w", file, funcName, err)
	}
	r := &ir.FunctionResult{}
	for _, kv := range m.Entries {
		switch kv.Key {
		case "type":
			s, err := kv.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.result.type: %w", file, funcName, err)
			}
			if s != "" && !isValidResultType(s) {
				return nil, fmt.Errorf("load %s:%d:%d: function %q.result.type: invalid type %q",
					file, kv.KeyLine, kv.KeyCol, funcName, s)
			}
			r.Type = s
		case "doc":
			s, err := kv.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: function %q.result.doc: %w", file, funcName, err)
			}
			r.Doc = s
		default:
			return nil, fmt.Errorf("load %s:%d:%d: function %q.result: unknown field %q",
				file, kv.KeyLine, kv.KeyCol, funcName, kv.Key)
		}
	}
	return r, nil
}

// loadPrelude parses a prelude: block. It's an ordered mapping from name
// to expression, where each expression is parsed at load-time and stored
// for evaluation at run-time (when the callable is dispatched).
//
// contextLabel is a human-readable noun for error messages — typically
// "target NAME" or "function NAME".
func loadPrelude(node yamlValue, file, contextLabel string) ([]ir.PreludeEntry, error) {
	m, err := node.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: %s.prelude: %w", file, contextLabel, err)
	}
	out := make([]ir.PreludeEntry, 0, len(m.Entries))
	seen := make(map[string]bool, len(m.Entries))
	for _, kv := range m.Entries {
		if seen[kv.Key] {
			return nil, fmt.Errorf("load %s:%d:%d: %s.prelude: duplicate binding %q",
				file, kv.KeyLine, kv.KeyCol, contextLabel, kv.Key)
		}
		seen[kv.Key] = true

		// Each value is treated as an expression template so it may
		// contain ${...} substitutions, nested function calls, etc.
		raw, err := kv.Value.AsString()
		if err != nil {
			return nil, fmt.Errorf("load %s: %s.prelude.%s: %w", file, contextLabel, kv.Key, err)
		}
		exprNode, perr := expr.ParseTemplate(raw, expr.Position{
			File: file, Line: kv.Value.Line, Col: kv.Value.Col,
		})
		if perr != nil {
			return nil, fmt.Errorf("load %s: %s.prelude.%s: %w", file, contextLabel, kv.Key, perr)
		}
		out = append(out, ir.PreludeEntry{
			Name:   kv.Key,
			Expr:   exprNode,
			Source: ir.SourceLoc{File: file, Line: kv.Value.Line, Column: kv.Value.Col},
		})
	}
	return out, nil
}

// loadLanguages parses the languages: block — user-defined interpreters.
// The six built-ins (bash, sh, python, ruby, node, perl) are registered
// by materialize; this block lets a project add custom entries that
// override or extend the built-ins.
func loadLanguages(p *ir.Project, entry yamlMapEntry, file string) error {
	m, err := entry.Value.AsMapping()
	if err != nil {
		return fmt.Errorf("load %s: languages: %w", file, err)
	}
	for _, kv := range m.Entries {
		lang, err := loadLanguage(kv, file)
		if err != nil {
			return err
		}
		p.Languages[lang.Name] = lang
	}
	return nil
}

// loadLanguage parses one language entry.
func loadLanguage(kv yamlMapEntry, file string) (*ir.Language, error) {
	name := kv.Key
	body, err := kv.Value.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: language %q: %w", file, name, err)
	}
	lang := &ir.Language{
		Name:   name,
		Ext:    ".sh", // sensible default
		Source: ir.SourceLoc{File: file, Line: kv.Value.Line, Column: kv.Value.Col},
	}
	allowed := map[string]bool{"interpreter": true, "args": true, "ext": true}
	for _, e := range body.Entries {
		if !allowed[e.Key] {
			return nil, fmt.Errorf("load %s:%d:%d: language %q has unknown field %q",
				file, e.KeyLine, e.KeyCol, name, e.Key)
		}
		switch e.Key {
		case "interpreter":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: language %q.interpreter: %w", file, name, err)
			}
			lang.Interpreter = s
		case "args":
			seq, err := e.Value.AsSequence()
			if err != nil {
				return nil, fmt.Errorf("load %s: language %q.args: %w", file, name, err)
			}
			for _, item := range seq.Items {
				s, err := item.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: language %q.args: %w", file, name, err)
				}
				lang.Args = append(lang.Args, s)
			}
		case "ext":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: language %q.ext: %w", file, name, err)
			}
			lang.Ext = s
		}
	}
	if lang.Interpreter == "" {
		return nil, fmt.Errorf("validate %s: language %q is missing required field 'interpreter'",
			file, name)
	}
	return lang, nil
}

// isValidParamType reports whether s names one of the six supported
// parameter types. Param types map 1:1 to ValueKind.
func isValidParamType(s string) bool {
	switch s {
	case "string", "int", "float", "bool", "list", "map":
		return true
	}
	return false
}

// isValidResultType accepts the same set as param types plus "none"
// for functions that intentionally return nothing.
func isValidResultType(s string) bool {
	if s == "none" {
		return true
	}
	return isValidParamType(s)
}
