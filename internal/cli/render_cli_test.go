package cli

// End-to-end CLI exercise of Stage 3c templates: a target body uses
// ${render:tmpl(args)} which expands during body interpolation, the
// rendered script runs, and the captured output proves the
// substitution happened.
//
// These tests use the "go" (text/template) engine throughout — it
// has no external deps so they run in both the default build and
// the -tags nogonja build. The jinja engine has its own coverage
// under internal/template/jinja/jinja_test.go.

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_TemplateInlineBody: target body contains ${render:greeting(...)}
// where greeting is an inline-body template. The script should echo
// the rendered string.
func TestRun_TemplateInlineBody(t *testing.T) {
	dir := t.TempDir()
	gmkYML := filepath.Join(dir, "gmk.yml")
	content := `
templates:
  greeting:
    engine: go
    body: "Hello, {{.who}}!"
targets:
  say-hi:
    run: |
      echo '${render:greeting(who="world")}'
`
	if err := os.WriteFile(gmkYML, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "say-hi", "-f", gmkYML})
	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v (output: %s)", err, out.String())
	}

	// Find the materialized script and verify the rendered text
	// landed in it. (We check the script, not stdout, because cobra
	// runs the body process with its own pipe and stdout capture in
	// tests is fiddly.)
	bodiesDir := filepath.Join(dir, ".gmk-cache", "bodies")
	var script string
	_ = filepath.WalkDir(bodiesDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, "/body.sh") {
			b, _ := os.ReadFile(p)
			script = string(b)
		}
		return nil
	})
	if script == "" {
		t.Fatal("no body.sh found under .gmk-cache/bodies/")
	}
	if !strings.Contains(script, "Hello, world!") {
		t.Errorf("script should contain rendered text 'Hello, world!', got:\n%s", script)
	}
}

// TestRun_TemplateFileReference: same as above but the template
// source lives in an external file resolved relative to the
// declaring YAML.
func TestRun_TemplateFileReference(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmplPath := filepath.Join(tmplDir, "greet.tmpl")
	if err := os.WriteFile(tmplPath, []byte("Salutations, {{.name}}!"), 0o644); err != nil {
		t.Fatal(err)
	}

	gmkYML := filepath.Join(dir, "gmk.yml")
	content := `
templates:
  greet:
    engine: go
    file: templates/greet.tmpl
targets:
  hi:
    run: |
      echo '${render:greet(name="reader")}'
`
	if err := os.WriteFile(gmkYML, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "hi", "-f", gmkYML})
	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v (output: %s)", err, out.String())
	}

	bodiesDir := filepath.Join(dir, ".gmk-cache", "bodies")
	var script string
	_ = filepath.WalkDir(bodiesDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, "/body.sh") {
			b, _ := os.ReadFile(p)
			script = string(b)
		}
		return nil
	})
	if !strings.Contains(script, "Salutations, reader!") {
		t.Errorf("script should contain rendered text, got:\n%s", script)
	}
}

// TestRun_TemplateUnknown: the target body refers to a template that
// doesn't exist. The error message should list declared candidates so
// the user can find their typo.
func TestRun_TemplateUnknown(t *testing.T) {
	dir := t.TempDir()
	gmkYML := filepath.Join(dir, "gmk.yml")
	content := `
templates:
  greeting:
    engine: go
    body: "x"
targets:
  bad:
    run: |
      echo '${render:typo()}'
`
	if err := os.WriteFile(gmkYML, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "bad", "-f", gmkYML})
	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)
	err := r.Execute()
	if err == nil {
		t.Fatal("expected error: template not found")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error should name the missing template, got: %v", err)
	}
	if !strings.Contains(err.Error(), "greeting") {
		t.Errorf("error should hint at declared template 'greeting', got: %v", err)
	}
}
