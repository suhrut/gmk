package load

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBytes_Minimal(t *testing.T) {
	src := []byte(`
vars:
  greeting: "hello"
  target: "world"

targets:
  hello:
    run: |
      echo "${greeting} ${target}"
`)

	p, err := parseBytes("/tmp/test/build.yml", src)
	if err != nil {
		t.Fatalf("parseBytes: %v", err)
	}

	if got, want := p.SourcePath, "/tmp/test/build.yml"; got != want {
		t.Errorf("SourcePath = %q, want %q", got, want)
	}
	if got, want := p.Root, "/tmp/test"; got != want {
		t.Errorf("Root = %q, want %q", got, want)
	}

	if got, want := len(p.Vars), 2; got != want {
		t.Fatalf("len(Vars) = %d, want %d", got, want)
	}
	if got, want := p.Vars["greeting"].Value, "hello"; got != want {
		t.Errorf("Vars[greeting].Value = %q, want %q", got, want)
	}
	if got, want := p.Vars["target"].Value, "world"; got != want {
		t.Errorf("Vars[target].Value = %q, want %q", got, want)
	}

	// VarOrder should be deterministic (alphabetical in Stage 1).
	if got, want := p.VarOrder, []string{"greeting", "target"}; !equalSlices(got, want) {
		t.Errorf("VarOrder = %v, want %v", got, want)
	}

	if got, want := len(p.Targets), 1; got != want {
		t.Fatalf("len(Targets) = %d, want %d", got, want)
	}
	hello := p.Targets["hello"]
	if hello == nil {
		t.Fatal("Targets[hello] is nil")
	}
	if got, want := strings.TrimSpace(hello.Run), `echo "${greeting} ${target}"`; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
	if got, want := hello.Lang, "bash"; got != want {
		t.Errorf("Lang = %q, want %q (default)", got, want)
	}
	if got, want := hello.Source.File, "/tmp/test/build.yml"; got != want {
		t.Errorf("Source.File = %q, want %q", got, want)
	}
}

func TestParseBytes_LangOverride(t *testing.T) {
	src := []byte(`
targets:
  py_task:
    lang: python
    run: |
      print("hi")
`)
	p, err := parseBytes("/tmp/build.yml", src)
	if err != nil {
		t.Fatalf("parseBytes: %v", err)
	}
	if got, want := p.Targets["py_task"].Lang, "python"; got != want {
		t.Errorf("Lang = %q, want %q", got, want)
	}
}

func TestParseBytes_MissingRun(t *testing.T) {
	src := []byte(`
targets:
  broken:
    lang: bash
`)
	_, err := parseBytes("/tmp/build.yml", src)
	if err == nil {
		t.Fatal("expected error for missing 'run' field, got nil")
	}
	if !strings.Contains(err.Error(), "missing required field 'run'") {
		t.Errorf("error %q should mention missing 'run' field", err)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error %q should mention target name", err)
	}
}

func TestParseBytes_InvalidYAML(t *testing.T) {
	src := []byte("vars:\n  - not a map\n")
	_, err := parseBytes("/tmp/build.yml", src)
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestLoad_File(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "build.yml")
	content := `
vars:
  x: "value"
targets:
  t:
    run: "echo ${x}"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Vars["x"].Value != "value" {
		t.Errorf("Vars[x].Value = %q, want %q", p.Vars["x"].Value, "value")
	}
	if p.Targets["t"].Run != "echo ${x}" {
		t.Errorf("Run = %q, want %q", p.Targets["t"].Run, "echo ${x}")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/build.yml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestSortedKeys(t *testing.T) {
	in := map[string]string{"banana": "", "apple": "", "cherry": ""}
	got := sortedKeys(in)
	want := []string{"apple", "banana", "cherry"}
	if !equalSlices(got, want) {
		t.Errorf("sortedKeys = %v, want %v", got, want)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
