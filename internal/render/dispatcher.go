// Package render implements expr.RenderResolver by bridging the
// expression-layer ${render:name(args)} operator to the template-
// engine machinery in internal/template.
//
// Architecture mirror with internal/funcs (the call dispatcher):
//
//	expr.NamedCall{Kind: "call",   ...} -> funcs.Dispatcher  -> ir.Function execution
//	expr.NamedCall{Kind: "render", ...} -> render.Dispatcher -> template engine
//
// The dispatcher owns the conversion from the expression layer's
// typed Value lattice to the engines' map[string]any input shape,
// and the inverse (rendered string -> expr.Value). Engines stay pure
// (string -> string); the dispatcher handles every layer above that.
//
// File-vs-inline resolution also lives here. Inline bodies are passed
// straight through; file references are read at render time (not
// load time) so a template whose file is rewritten between
// invocations picks up the change without a project reload. This
// matches the "every gmk command is a fresh process" mental model
// users already have.

package render

import (
	"fmt"
	"os"
	"sync"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/template"
)

// Dispatcher implements expr.RenderResolver against a project's
// Templates table and an engine Registry. Safe for concurrent use:
// the templates map is read-only after construction; the registry's
// own concurrency model (RWMutex) handles parallel Get calls.
//
// Why take a *template.Registry rather than use the package-wide
// Default directly: tests can inject a fresh registry to isolate
// from any engines registered by other packages' init() funcs. In
// normal (non-test) use, callers pass template.Default.
type Dispatcher struct {
	templates map[string]*ir.Template
	registry  *template.Registry

	// fileCache memoizes file reads within a single Dispatcher's
	// lifetime. A typical "render N times during a build" workload
	// hits the same template file multiple times; reading it once
	// is the right default. If a file is touched between renders
	// within one gmk command, the user explicitly opts out by
	// rebuilding the Dispatcher.
	mu        sync.RWMutex
	fileCache map[string]string
}

// New constructs a Dispatcher.
func New(templates map[string]*ir.Template, registry *template.Registry) *Dispatcher {
	return &Dispatcher{
		templates: templates,
		registry:  registry,
		fileCache: make(map[string]string),
	}
}

// ResolveRender implements expr.RenderResolver. The contract: the
// returned Value is always a String holding the rendered output, or
// the error explains why rendering couldn't happen.
//
// Error categories, in the order they're checked:
//
//  1. Template name not found in the project — "template X not declared"
//  2. Engine name not registered — "engine Y not registered (suggest:
//     these are: jinja, go)"
//  3. File-template's file is unreadable — wrapped os.Open error
//  4. Engine's parse/execute failure — wrapped engine error
//
// Each carries enough context that the user can fix the issue
// without needing a stack trace.
func (d *Dispatcher) ResolveRender(name string, args map[string]expr.Value) (expr.Value, error) {
	tmpl, ok := d.templates[name]
	if !ok {
		return expr.NewNone(), fmt.Errorf("template %q not declared in project "+
			"(check the templates: block; did you mean one of: %s)",
			name, d.templateNamesForHint())
	}

	// Engine selection: explicit per-template engine wins; otherwise
	// use the registry default. Both lookups can fail; we want to
	// surface the engine name in the error either way.
	engineName := tmpl.Engine
	var engine template.Engine
	if engineName == "" {
		var err error
		engine, err = d.registry.Default()
		if err != nil {
			return expr.NewNone(), fmt.Errorf("template %q: %w", name, err)
		}
	} else {
		var ok bool
		engine, ok = d.registry.Get(engineName)
		if !ok {
			return expr.NewNone(), fmt.Errorf("template %q: engine %q not registered "+
				"(registered engines: %v)",
				name, engineName, d.registry.Names())
		}
	}

	// Resolve source: inline body wins (it's already in memory);
	// file form requires a (possibly cached) read.
	source := tmpl.Body
	if source == "" {
		var err error
		source, err = d.readFile(tmpl.ResolvedFile)
		if err != nil {
			return expr.NewNone(), fmt.Errorf("template %q: read file %s: %w",
				name, tmpl.ResolvedFile, err)
		}
	}

	// Convert args to map[string]any. ToJSON does exactly this — the
	// name is historical (it's the same converter we use for the JSON
	// IPC boundary), but the output is plain Go-native and the
	// engines accept it as-is.
	data := make(map[string]any, len(args))
	for k, v := range args {
		data[k] = v.ToJSON()
	}

	rendered, err := engine.Render(source, data)
	if err != nil {
		return expr.NewNone(), fmt.Errorf("template %q: %w", name, err)
	}
	return expr.NewString(rendered), nil
}

// readFile pulls a template file's contents through the cache. The
// cache is per-Dispatcher (so per-gmk-command), not process-wide —
// matching the lifetime contract callers expect.
func (d *Dispatcher) readFile(path string) (string, error) {
	d.mu.RLock()
	if cached, ok := d.fileCache[path]; ok {
		d.mu.RUnlock()
		return cached, nil
	}
	d.mu.RUnlock()

	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(bytes)

	d.mu.Lock()
	d.fileCache[path] = content
	d.mu.Unlock()
	return content, nil
}

// templateNamesForHint returns a comma-joined list of declared
// template names for inclusion in "did you mean" error messages.
// We don't do fuzzy matching — that's overkill — but listing what
// IS declared turns a frustrating "template not found" into an
// actionable one. Returns "(none declared)" for the empty case.
func (d *Dispatcher) templateNamesForHint() string {
	if len(d.templates) == 0 {
		return "(none declared)"
	}
	names := make([]string, 0, len(d.templates))
	for n := range d.templates {
		names = append(names, n)
	}
	// Insertion sort to keep output deterministic without pulling
	// sort just for this. Small slice; no perf concern.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	out := names[0]
	for i := 1; i < len(names); i++ {
		out += ", " + names[i]
	}
	return out
}
