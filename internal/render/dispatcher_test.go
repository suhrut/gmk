package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/template"
)

// newRegistryWith builds a fresh registry with a fake engine that
// echoes back what it was rendered with — handy for asserting that
// the dispatcher passed the right source and data through.
type echoEngine struct {
	name       string
	lastSource string
	lastData   map[string]any
	out        string
	err        error
}

func (e *echoEngine) Name() string { return e.name }
func (e *echoEngine) Render(source string, data map[string]any) (string, error) {
	e.lastSource = source
	e.lastData = data
	if e.err != nil {
		return "", e.err
	}
	if e.out != "" {
		return e.out, nil
	}
	return "rendered: " + source, nil
}

func newRegistry(t *testing.T, engines ...*echoEngine) *template.Registry {
	t.Helper()
	r := template.NewRegistry()
	for _, e := range engines {
		r.Register(e)
	}
	return r
}

// --- Happy paths ---

func TestDispatcher_InlineBodyRenders(t *testing.T) {
	eng := &echoEngine{name: "test"}
	reg := newRegistry(t, eng)
	reg.SetDefault("test")

	tmpls := map[string]*ir.Template{
		"hello": {Name: "hello", Body: "Hi {{.who}}"},
	}
	d := New(tmpls, reg)

	out, err := d.ResolveRender("hello", map[string]expr.Value{
		"who": expr.NewString("world"),
	})
	if err != nil {
		t.Fatalf("ResolveRender: %v", err)
	}
	if out.AsString() != "rendered: Hi {{.who}}" {
		t.Errorf("got %q", out.AsString())
	}
	if eng.lastSource != "Hi {{.who}}" {
		t.Errorf("engine saw source %q", eng.lastSource)
	}
	if eng.lastData["who"] != "world" {
		t.Errorf("engine saw data[who]=%v, want world", eng.lastData["who"])
	}
}

func TestDispatcher_FileBodyRenders(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "dockerfile.tmpl")
	if err := os.WriteFile(tmplPath, []byte("FROM scratch\n{{.cmd}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := &echoEngine{name: "test", out: "FROM scratch\necho hi\n"}
	reg := newRegistry(t, eng)
	reg.SetDefault("test")

	tmpls := map[string]*ir.Template{
		"dockerfile": {Name: "dockerfile", File: "dockerfile.tmpl", ResolvedFile: tmplPath},
	}
	d := New(tmpls, reg)

	out, err := d.ResolveRender("dockerfile", map[string]expr.Value{
		"cmd": expr.NewString("echo hi"),
	})
	if err != nil {
		t.Fatalf("ResolveRender: %v", err)
	}
	if out.AsString() != "FROM scratch\necho hi\n" {
		t.Errorf("got %q", out.AsString())
	}
	if !strings.Contains(eng.lastSource, "FROM scratch") {
		t.Errorf("engine should have seen file contents, got %q", eng.lastSource)
	}
}

func TestDispatcher_PerTemplateEngineOverride(t *testing.T) {
	defaultEng := &echoEngine{name: "default-eng", out: "DEFAULT"}
	otherEng := &echoEngine{name: "other-eng", out: "OTHER"}
	reg := newRegistry(t, defaultEng, otherEng)
	reg.SetDefault("default-eng")

	tmpls := map[string]*ir.Template{
		"uses-other": {Name: "uses-other", Body: "x", Engine: "other-eng"},
	}
	d := New(tmpls, reg)

	out, err := d.ResolveRender("uses-other", nil)
	if err != nil {
		t.Fatalf("ResolveRender: %v", err)
	}
	if out.AsString() != "OTHER" {
		t.Errorf("got %q, want OTHER (per-template engine should win over default)", out.AsString())
	}
}

func TestDispatcher_NestedMapArgsConvertCleanly(t *testing.T) {
	// Nested Map and List Values should round-trip to map[string]any
	// and []any. Verifying with a Map arg that contains a Map.
	eng := &echoEngine{name: "test"}
	reg := newRegistry(t, eng)
	reg.SetDefault("test")

	tmpls := map[string]*ir.Template{"t": {Name: "t", Body: "x"}}
	d := New(tmpls, reg)

	inner := expr.NewMap(map[string]expr.Value{
		"host": expr.NewString("db.example.com"),
		"port": expr.NewInt(5432),
	})
	_, err := d.ResolveRender("t", map[string]expr.Value{"cfg": inner})
	if err != nil {
		t.Fatalf("ResolveRender: %v", err)
	}

	cfg, ok := eng.lastData["cfg"].(map[string]any)
	if !ok {
		t.Fatalf("cfg arg arrived as %T, want map[string]any", eng.lastData["cfg"])
	}
	if cfg["host"] != "db.example.com" {
		t.Errorf("cfg.host=%v", cfg["host"])
	}
	if cfg["port"] != int64(5432) {
		t.Errorf("cfg.port=%v (%T)", cfg["port"], cfg["port"])
	}
}

func TestDispatcher_FileCacheReadsOnce(t *testing.T) {
	// Per-dispatcher file cache: two renders of the same file-backed
	// template should not result in two reads. We verify by deleting
	// the file after the first render and confirming the second
	// render still succeeds.
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "cached.tmpl")
	if err := os.WriteFile(tmplPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := &echoEngine{name: "test"}
	reg := newRegistry(t, eng)
	reg.SetDefault("test")
	tmpls := map[string]*ir.Template{
		"t": {Name: "t", File: "cached.tmpl", ResolvedFile: tmplPath},
	}
	d := New(tmpls, reg)

	if _, err := d.ResolveRender("t", nil); err != nil {
		t.Fatalf("first render: %v", err)
	}
	// Remove the file — the cache should still satisfy the next render.
	if err := os.Remove(tmplPath); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolveRender("t", nil); err != nil {
		t.Errorf("second render should hit cache, got error: %v", err)
	}
}

// --- Error paths ---

func TestDispatcher_UnknownTemplateNameMentionsCandidates(t *testing.T) {
	reg := newRegistry(t, &echoEngine{name: "x"})
	reg.SetDefault("x")
	tmpls := map[string]*ir.Template{
		"alpha": {Name: "alpha", Body: "a"},
		"beta":  {Name: "beta", Body: "b"},
	}
	d := New(tmpls, reg)

	_, err := d.ResolveRender("gamma", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "alpha") || !strings.Contains(msg, "beta") {
		t.Errorf("error should list declared templates as hints, got: %v", err)
	}
	if !strings.Contains(msg, "gamma") {
		t.Errorf("error should name the missing template, got: %v", err)
	}
}

func TestDispatcher_UnknownTemplateHintWhenNoneDeclared(t *testing.T) {
	reg := newRegistry(t, &echoEngine{name: "x"})
	reg.SetDefault("x")
	d := New(map[string]*ir.Template{}, reg)

	_, err := d.ResolveRender("anything", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "(none declared)") {
		t.Errorf("expected (none declared) hint, got: %v", err)
	}
}

func TestDispatcher_UnknownEngineErrors(t *testing.T) {
	reg := newRegistry(t, &echoEngine{name: "jinja"})
	reg.SetDefault("jinja")
	tmpls := map[string]*ir.Template{
		"t": {Name: "t", Body: "x", Engine: "mustache"},
	}
	d := New(tmpls, reg)

	_, err := d.ResolveRender("t", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "mustache") {
		t.Errorf("error should name the unknown engine, got: %v", err)
	}
}

func TestDispatcher_FileReadFailureErrors(t *testing.T) {
	reg := newRegistry(t, &echoEngine{name: "test"})
	reg.SetDefault("test")
	tmpls := map[string]*ir.Template{
		"t": {Name: "t", File: "missing.tmpl", ResolvedFile: "/nonexistent/missing.tmpl"},
	}
	d := New(tmpls, reg)

	_, err := d.ResolveRender("t", nil)
	if err == nil {
		t.Fatal("expected error reading missing file")
	}
	if !strings.Contains(err.Error(), "missing.tmpl") {
		t.Errorf("error should name the path, got: %v", err)
	}
}

func TestDispatcher_EngineErrorPropagates(t *testing.T) {
	eng := &echoEngine{name: "broken", err: os.ErrInvalid}
	reg := newRegistry(t, eng)
	reg.SetDefault("broken")
	tmpls := map[string]*ir.Template{"t": {Name: "t", Body: "x"}}
	d := New(tmpls, reg)

	_, err := d.ResolveRender("t", nil)
	if err == nil {
		t.Fatal("expected engine error to propagate")
	}
	if !strings.Contains(err.Error(), `template "t"`) {
		t.Errorf("error should contextualize with template name, got: %v", err)
	}
}
