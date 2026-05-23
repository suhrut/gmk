package template

import (
	"bytes"
	"fmt"
	"text/template"
)

// goEngine adapts Go's stdlib text/template to the Engine interface.
//
// Why text/template and not html/template: gmk templates produce
// arbitrary text — Dockerfiles, k8s YAML, config files, source code.
// html/template performs context-aware HTML escaping that would corrupt
// non-HTML output (escaping & to &amp; in a JSON-fragment value would
// silently break it). Users who specifically want HTML escaping can
// either pipe through Jinja2's `|escape` filter or call html-specific
// helpers — but the default for "text" should be inert.
//
// Why a fresh template per Render: text/template's Parse-then-Execute
// flow keeps state on the *Template (associated templates, registered
// funcs, option toggles). A shared instance across renders would need
// careful locking and would still risk option drift. Parse cost is
// negligible compared to the surrounding gmk machinery (one syscall
// to spawn the body process dwarfs a template parse).
//
// missingkey=error: text/template's default for missing map keys is to
// print "<no value>", which silently produces broken output. We flip
// to error so missing data shows up as a render failure with a clear
// "map has no entry for key X" message.
type goEngine struct{}

func (g *goEngine) Name() string { return "go" }

func (g *goEngine) Render(source string, data map[string]any) (string, error) {
	tmpl, err := template.New("gmk-template").
		Option("missingkey=error").
		Parse(source)
	if err != nil {
		return "", fmt.Errorf("engine go: parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("engine go: execute: %w", err)
	}
	return buf.String(), nil
}

func init() {
	Register(&goEngine{})
}
