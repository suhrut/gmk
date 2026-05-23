//go:build !nogonja

package jinja

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/template"
)

func TestJinjaEngine_RegistersAtImportTime(t *testing.T) {
	e, ok := template.Get("jinja")
	if !ok {
		t.Fatal("jinja engine should be registered by package init()")
	}
	if e.Name() != "jinja" {
		t.Errorf("Name=%q, want jinja", e.Name())
	}
}

func TestJinjaEngine_SimpleSubstitution(t *testing.T) {
	out, err := (&engine{}).Render("Hello, {{ name }}!", map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "Hello, world!" {
		t.Errorf("got %q, want Hello, world!", out)
	}
}

func TestJinjaEngine_Filter(t *testing.T) {
	// Jinja's killer feature vs Go text/template: pipe-style filters.
	// {{ name|upper }} reads exactly like the Unix pipeline mental model
	// most devops folks already use.
	out, err := (&engine{}).Render("{{ name|upper }}", map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "WORLD" {
		t.Errorf("got %q, want WORLD", out)
	}
}

func TestJinjaEngine_Conditional(t *testing.T) {
	src := `{% if lang == "go" %}go binary{% else %}other{% endif %}`
	out, err := (&engine{}).Render(src, map[string]any{"lang": "go"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "go binary" {
		t.Errorf("got %q, want go binary", out)
	}
}

func TestJinjaEngine_LoopOverList(t *testing.T) {
	// for-loops are Jinja's biggest advantage over logic-less engines.
	// This is the kind of thing that makes Dockerfile/k8s template
	// generation pleasant.
	src := `{% for s in services %}{{ s }}{% if not loop.last %},{% endif %}{% endfor %}`
	out, err := (&engine{}).Render(src, map[string]any{
		"services": []string{"api", "web", "worker"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "api,web,worker" {
		t.Errorf("got %q, want api,web,worker", out)
	}
}

func TestJinjaEngine_ParseError(t *testing.T) {
	_, err := (&engine{}).Render("{{ unterminated", map[string]any{})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "engine jinja:") {
		t.Errorf("error should be prefixed, got: %v", err)
	}
}
