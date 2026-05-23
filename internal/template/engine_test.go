package template

import (
	"strings"
	"testing"
)

// --- Registry mechanics ---

type stubEngine struct{ name string }

func (s *stubEngine) Name() string                              { return s.name }
func (s *stubEngine) Render(string, map[string]any) (string, error) { return s.name + ":ok", nil }

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubEngine{name: "alpha"})
	r.Register(&stubEngine{name: "beta"})

	if e, ok := r.Get("alpha"); !ok || e.Name() != "alpha" {
		t.Errorf("Get(alpha): got (%v, %v), want (alpha, true)", e, ok)
	}
	if e, ok := r.Get("nonexistent"); ok || e != nil {
		t.Errorf("Get(nonexistent): got (%v, %v), want (nil, false)", e, ok)
	}
}

func TestRegistry_RegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubEngine{name: "x"})
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on duplicate registration, got none")
		}
	}()
	r.Register(&stubEngine{name: "x"})
}

func TestRegistry_Default(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Default(); err == nil {
		t.Errorf("Default with no engines/no setting should error")
	}
	r.SetDefault("foo")
	if _, err := r.Default(); err == nil {
		t.Errorf("Default pointing at unregistered engine should error")
	}
	r.Register(&stubEngine{name: "foo"})
	e, err := r.Default()
	if err != nil {
		t.Fatalf("Default after registration: %v", err)
	}
	if e.Name() != "foo" {
		t.Errorf("Default returned %q, want foo", e.Name())
	}
}

func TestRegistry_DefaultErrorMentionsBuildTag(t *testing.T) {
	// When the default is set but the named engine isn't registered,
	// the error should hint at build tags — this is the realistic
	// scenario (compiled with -tags nogonja but default still "jinja").
	r := NewRegistry()
	r.SetDefault("jinja")
	_, err := r.Default()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nogonja") {
		t.Errorf("error should mention nogonja build tag for discoverability, got: %v", err)
	}
}

func TestRegistry_NamesIsSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zeta", "alpha", "mike"} {
		r.Register(&stubEngine{name: n})
	}
	got := r.Names()
	want := []string{"alpha", "mike", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("len(Names)=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

// --- Go (text/template) engine ---

func TestGoEngine_Name(t *testing.T) {
	e := &goEngine{}
	if e.Name() != "go" {
		t.Errorf("Name=%q, want go", e.Name())
	}
}

func TestGoEngine_SimpleSubstitution(t *testing.T) {
	e := &goEngine{}
	out, err := e.Render("Hello, {{.name}}!", map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "Hello, world!" {
		t.Errorf("got %q, want %q", out, "Hello, world!")
	}
}

func TestGoEngine_Conditional(t *testing.T) {
	src := `{{if eq .lang "go"}}go binary{{else}}other{{end}}`
	e := &goEngine{}
	got, err := e.Render(src, map[string]any{"lang": "go"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "go binary" {
		t.Errorf("got %q, want go binary", got)
	}
}

func TestGoEngine_NestedData(t *testing.T) {
	src := `{{.cfg.image}}:{{.cfg.tag}}`
	out, err := (&goEngine{}).Render(src, map[string]any{
		"cfg": map[string]any{"image": "myorg/api", "tag": "v2"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "myorg/api:v2" {
		t.Errorf("got %q", out)
	}
}

func TestGoEngine_MissingKeyIsError(t *testing.T) {
	// missingkey=error: the whole point is to surface broken templates
	// instead of producing "<no value>" silently. This test locks in
	// that behavior so future refactors don't accidentally restore
	// stdlib defaults.
	_, err := (&goEngine{}).Render("{{.x}}", map[string]any{})
	if err == nil {
		t.Errorf("expected error on missing map key, got nil (text/template default leaks 'no value')")
	}
}

func TestGoEngine_ParseError(t *testing.T) {
	_, err := (&goEngine{}).Render("{{ unterminated", map[string]any{})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "engine go: parse:") {
		t.Errorf("error should be prefixed with engine name, got: %v", err)
	}
}

func TestGoEngine_RegistersInDefaultRegistry(t *testing.T) {
	// The init() in go_engine.go must register into the package
	// Default registry. Without this the binary boots with no engines
	// at all.
	e, ok := Default.Get("go")
	if !ok {
		t.Fatal("go engine should be registered in Default at startup")
	}
	if e.Name() != "go" {
		t.Errorf("Default.Get(go) returned engine named %q", e.Name())
	}
}
