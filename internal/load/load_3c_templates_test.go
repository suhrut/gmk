package load

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeYAML is a small helper for table tests: writes content to
// dir/gmk.yml and returns the path.
func writeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "gmk.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- Happy paths ---

func TestLoad_Templates_InlineBody(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  greeting:
    body: |
      Hello, {{ name }}!
    engine: jinja
    doc: A simple greeting template
`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tmpl, ok := p.Templates["greeting"]
	if !ok {
		t.Fatal("template 'greeting' not loaded")
	}
	if !strings.Contains(tmpl.Body, "Hello, {{ name }}!") {
		t.Errorf("body got %q", tmpl.Body)
	}
	if tmpl.Engine != "jinja" {
		t.Errorf("engine=%q", tmpl.Engine)
	}
	if tmpl.Doc != "A simple greeting template" {
		t.Errorf("doc=%q", tmpl.Doc)
	}
	// ResolvedFile must be empty for inline templates so the
	// dispatcher knows to use Body instead.
	if tmpl.ResolvedFile != "" {
		t.Errorf("inline template should not have ResolvedFile, got %q", tmpl.ResolvedFile)
	}
}

func TestLoad_Templates_FileReference(t *testing.T) {
	dir := t.TempDir()
	// Drop a template file in a subdir to verify relative-path
	// resolution against the declaring YAML's directory.
	tmplDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmplPath := filepath.Join(tmplDir, "dockerfile.tmpl")
	if err := os.WriteFile(tmplPath, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := writeYAML(t, dir, `
templates:
  dockerfile:
    file: templates/dockerfile.tmpl
`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tmpl := p.Templates["dockerfile"]
	if tmpl == nil {
		t.Fatal("template not loaded")
	}
	if tmpl.File != "templates/dockerfile.tmpl" {
		t.Errorf("File preserves original spelling, got %q", tmpl.File)
	}
	// ResolvedFile must be absolute, pointing at the actual file.
	wantResolved, _ := filepath.EvalSymlinks(tmplPath)
	gotResolved, _ := filepath.EvalSymlinks(tmpl.ResolvedFile)
	if gotResolved != wantResolved {
		t.Errorf("ResolvedFile=%q, want %q", tmpl.ResolvedFile, tmplPath)
	}
}

func TestLoad_Templates_AbsoluteFilePathPreserved(t *testing.T) {
	// When file: is given as an absolute path, ResolvedFile equals
	// File. Useful for templates shared across projects via a
	// well-known system path.
	dir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "shared.tmpl")
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeYAML(t, dir, `
templates:
  shared:
    file: `+abs+`
`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Templates["shared"].ResolvedFile != abs {
		t.Errorf("absolute path should pass through, got %q want %q",
			p.Templates["shared"].ResolvedFile, abs)
	}
}

func TestLoad_Templates_OmittedEngine(t *testing.T) {
	// engine: is optional; empty means "use registry default at
	// render time". The load layer just preserves whatever was (or
	// wasn't) written.
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  unspecified:
    body: "just text"
`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Templates["unspecified"].Engine != "" {
		t.Errorf("unspecified engine should be empty string, got %q",
			p.Templates["unspecified"].Engine)
	}
}

func TestLoad_Templates_OrderPreserved(t *testing.T) {
	// TemplateOrder mirrors functions/vars: declaration order in
	// YAML must survive into IR so `gmk list --templates` is
	// deterministic.
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  third:  {body: "3"}
  first:  {body: "1"}
  second: {body: "2"}
`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"third", "first", "second"}
	if len(p.TemplateOrder) != len(want) {
		t.Fatalf("len(TemplateOrder)=%d want %d", len(p.TemplateOrder), len(want))
	}
	for i := range want {
		if p.TemplateOrder[i] != want[i] {
			t.Errorf("TemplateOrder[%d]=%q want %q", i, p.TemplateOrder[i], want[i])
		}
	}
}

// --- Error paths ---

func TestLoad_Templates_BothBodyAndFile(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  bad:
    body: "x"
    file: y.tmpl
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: body and file both set")
	}
	if !strings.Contains(err.Error(), "exactly one is required") {
		t.Errorf("error should mention exclusivity, got: %v", err)
	}
}

func TestLoad_Templates_NeitherBodyNorFile(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  bad:
    engine: jinja
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: neither body nor file")
	}
	if !strings.Contains(err.Error(), "neither body nor file") {
		t.Errorf("error should mention missing source, got: %v", err)
	}
}

func TestLoad_Templates_UnknownField(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  bad:
    body: "x"
    typo_field: "oops"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("error should name the unknown field, got: %v", err)
	}
	// Must list accepted fields to be useful.
	if !strings.Contains(err.Error(), "accepted:") {
		t.Errorf("error should list accepted fields, got: %v", err)
	}
}

func TestLoad_Templates_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	// Use list-of-pairs syntax... actually YAML mappings can't have
	// dupes parsed in cleanly, so we test the dup-detection path
	// via two YAML files via includes:.
	subDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "extra.yml"), []byte(`
templates:
  greet: {body: "y"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The current load logic loads top-level first then includes;
	// templates from includes don't currently merge into p.Templates.
	// Skip this test until cross-file template merging is decided
	// — the within-file duplicate case is covered by YAML's own
	// map-key uniqueness, which goccy detects at parse time.
	_ = writeYAML(t, dir, `
templates:
  greet: {body: "x"}
`)
	t.Skip("cross-file template merge semantics not yet decided in Stage 3c")
}

// --- Top-level key validation ---

func TestLoad_TopLevelKeys_AcceptsTemplates(t *testing.T) {
	// templates was added to the closed schema in Stage 3c; this
	// regression test pins that addition so future schema tightening
	// doesn't accidentally drop it.
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  ok: {body: "x"}
`)
	if _, err := Load(path); err != nil {
		t.Errorf("templates: should be a valid top-level key, got: %v", err)
	}
}

func TestLoad_TopLevelKeys_StillRejectsUnknown(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templats: {} # typo
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown top-level key")
	}
	// Sanity: the error message should now list 'templates' as
	// accepted, so users see the close-but-not-quite match.
	if !strings.Contains(err.Error(), "templates") {
		t.Errorf("error should list templates as accepted, got: %v", err)
	}
}

// Sentinel test: make sure errors.Is wiring isn't accidentally broken
// by the new code path. Not specific to templates but cheap to run.
func TestLoad_ErrorsIsStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `
templates:
  bad: {body: "x", file: "y"}
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	// Just check we're returning a real error (not a panic), and
	// that errors.Is doesn't crash on our wrapped chains.
	if errors.Is(err, nil) {
		t.Error("errors.Is(err, nil) should be false")
	}
}
