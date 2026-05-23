// Package jinja registers a Jinja2-compatible template engine with
// the shared template.Registry. Backed by github.com/nikolalohinski/gonja/v2
// — the actively-maintained pure-Go implementation that aims for the
// closest possible compatibility with Python's reference Jinja2.
//
// Blank-import this package from cmd/gmk/main.go to make the engine
// available; nothing else needs to change. SetDefault("jinja") is
// called separately at startup.
//
// Build tag: this file is excluded when building with -tags nogonja,
// in which case jinja_stub.go provides a no-op stand-in so the
// package still compiles. Useful for sandbox/minimal builds where
// pulling gonja's transitive deps isn't possible.

//go:build !nogonja

package jinja

import (
	"bytes"
	"fmt"

	"github.com/nikolalohinski/gonja/v2"
	"github.com/nikolalohinski/gonja/v2/exec"

	"github.com/suhrut/gmk/internal/template"
)

// engine wraps gonja v2 behind the template.Engine interface. Stateless;
// every Render call constructs a fresh Template (gonja templates aren't
// safe to share across renders that mutate context). Parse cost is
// dominated by the surrounding gmk machinery, not the template itself.
type engine struct{}

// Name returns the YAML identifier "jinja". We intentionally don't use
// "jinja2" — the "2" is a Python-side version marker that doesn't apply
// to a fresh Go implementation, and shorter names play better in YAML.
func (e *engine) Name() string { return "jinja" }

// Render compiles `source` as a Jinja2 template and executes it against
// `data`. The returned string is the rendered output; errors wrap
// gonja's own diagnostics with an "engine jinja:" prefix for the
// case where multiple engines are in play in a single project.
//
// Why FromString rather than loading from a file: by the time we reach
// here, the template body has already been resolved by gmk's load
// layer (inline body? read from file relative to the declaring YAML?
// either way it's a string in memory). Pushing the file-vs-inline
// distinction down into the engine would couple the engine to gmk's
// path conventions for no benefit.
//
// Why exec.NewContext: gonja's Template.Execute takes an *exec.Context,
// not a bare map. NewContext is the documented wrapper that adapts
// map[string]any to gonja's internal context representation. The data
// is read-only from gonja's perspective; we don't need to worry about
// it mutating the caller's map.
func (e *engine) Render(source string, data map[string]any) (string, error) {
	tmpl, err := gonja.FromString(source)
	if err != nil {
		return "", fmt.Errorf("engine jinja: parse: %w", err)
	}
	ctx := exec.NewContext(data)
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("engine jinja: execute: %w", err)
	}
	return buf.String(), nil
}

func init() {
	template.Register(&engine{})
}
