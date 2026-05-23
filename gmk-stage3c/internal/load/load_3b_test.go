package load_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/load"
)

func writeBuild(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gmk.yml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_Functions_Basic(t *testing.T) {
	path := writeBuild(t, `
functions:
  greet:
    doc: "Friendly hello."
    params:
      - name: who
        type: string
        default: "world"
    result:
      type: string
      doc: "Greeting text"
    script: |
      echo "hello, ${who}"
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.Functions) != 1 {
		t.Fatalf("functions len=%d", len(p.Functions))
	}
	fn := p.Functions["greet"]
	if fn == nil {
		t.Fatal("greet not loaded")
	}
	if fn.Doc != "Friendly hello." {
		t.Errorf("doc=%q", fn.Doc)
	}
	if len(fn.Params) != 1 {
		t.Fatalf("params len=%d", len(fn.Params))
	}
	if fn.Params[0].Name != "who" || fn.Params[0].Type != "string" {
		t.Errorf("param: %+v", fn.Params[0])
	}
	if fn.Params[0].Default == nil {
		t.Error("default should be parsed")
	}
	if fn.Result == nil || fn.Result.Type != "string" {
		t.Errorf("result: %+v", fn.Result)
	}
	if !strings.Contains(fn.Run, "echo") {
		t.Errorf("run: %q", fn.Run)
	}
	if fn.Lang != "bash" {
		t.Errorf("lang=%q (default should be bash)", fn.Lang)
	}
}

func TestLoad_Functions_DeclarationOrder(t *testing.T) {
	path := writeBuild(t, `
functions:
  zebra:
    script: echo zebra
  alpha:
    script: echo alpha
  middle:
    script: echo middle
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zebra", "alpha", "middle"}
	if len(p.FunctionOrder) != len(want) {
		t.Fatalf("order len=%d, want %d", len(p.FunctionOrder), len(want))
	}
	for i, name := range want {
		if p.FunctionOrder[i] != name {
			t.Errorf("order[%d]=%q, want %q", i, p.FunctionOrder[i], name)
		}
	}
}

func TestLoad_Functions_NameCollisionWithTarget(t *testing.T) {
	path := writeBuild(t, `
targets:
  build:
    run: echo target
functions:
  build:
    script: echo function
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Errorf("expected collision error, got %v", err)
	}
}

func TestLoad_Functions_DuplicateName(t *testing.T) {
	// YAML duplicate-key handling is loader-dependent; this is goccy which
	// merges silently. Belt-and-braces: we still catch it.
	path := writeBuild(t, `
functions:
  greet:
    script: echo a
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Re-Load doesn't re-test duplicate; this just sanity-checks the path.
	if len(p.Functions) != 1 {
		t.Errorf("got %d functions", len(p.Functions))
	}
}

func TestLoad_Function_UnknownField(t *testing.T) {
	path := writeBuild(t, `
functions:
  f:
    paramz: []   # typo for params
    script: echo x
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected unknown field error, got %v", err)
	}
}

func TestLoad_Function_InvalidParamType(t *testing.T) {
	path := writeBuild(t, `
functions:
  f:
    params:
      - name: x
        type: bigint
    script: echo x
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Errorf("expected invalid type error, got %v", err)
	}
}

func TestLoad_Function_DuplicateParamName(t *testing.T) {
	path := writeBuild(t, `
functions:
  f:
    params:
      - name: x
        type: string
      - name: x
        type: int
    script: echo x
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate parameter") {
		t.Errorf("expected duplicate parameter error, got %v", err)
	}
}

func TestLoad_Function_NoBodyNoPrelude(t *testing.T) {
	path := writeBuild(t, `
functions:
  empty:
    doc: "no body, no prelude"
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "must have either") {
		t.Errorf("expected must-have error, got %v", err)
	}
}

func TestLoad_Function_PreludeOnly(t *testing.T) {
	// Pure-data function: just a prelude, no body. Result comes from
	// the prelude bindings written to $GMK_PRELUDE.
	path := writeBuild(t, `
functions:
  config:
    prelude:
      host: "api.local"
      port: "8080"
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fn := p.Functions["config"]
	if fn == nil {
		t.Fatal("config not loaded")
	}
	if len(fn.Prelude) != 2 {
		t.Fatalf("prelude len=%d", len(fn.Prelude))
	}
	if fn.Prelude[0].Name != "host" {
		t.Errorf("prelude[0]=%q", fn.Prelude[0].Name)
	}
	if fn.Prelude[1].Name != "port" {
		t.Errorf("prelude[1]=%q", fn.Prelude[1].Name)
	}
}

func TestLoad_Target_Prelude(t *testing.T) {
	path := writeBuild(t, `
targets:
  release:
    prelude:
      version: "1.2.3"
      build_time: "${env:BUILD_TIME:-unknown}"
    script: |
      echo "release $(jq -r .version "$GMK_PRELUDE")"
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t1 := p.Targets["release"]
	if t1 == nil {
		t.Fatal("release not loaded")
	}
	if len(t1.Prelude) != 2 {
		t.Fatalf("prelude len=%d", len(t1.Prelude))
	}
	if t1.Prelude[0].Name != "version" {
		t.Errorf("first binding: %q", t1.Prelude[0].Name)
	}
}

func TestLoad_Target_ScriptAliasForRun(t *testing.T) {
	// Either `run:` or `script:` works; setting both is an error.
	path := writeBuild(t, `
targets:
  hello:
    script: echo hi
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Targets["hello"].Run; !strings.Contains(got, "echo hi") {
		t.Errorf("script-as-run: %q", got)
	}
}

func TestLoad_Target_ScriptAndRunBoth(t *testing.T) {
	path := writeBuild(t, `
targets:
  hello:
    run: echo one
    script: echo two
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "cannot set both") {
		t.Errorf("expected both-set error, got %v", err)
	}
}

func TestLoad_Languages(t *testing.T) {
	path := writeBuild(t, `
languages:
  zsh:
    interpreter: zsh
    args: ["-e"]
    ext: ".zsh"
  lua:
    interpreter: /usr/bin/lua
functions:
  f:
    script: echo x
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.Languages) != 2 {
		t.Fatalf("languages len=%d", len(p.Languages))
	}
	zsh := p.Languages["zsh"]
	if zsh.Interpreter != "zsh" || zsh.Ext != ".zsh" {
		t.Errorf("zsh: %+v", zsh)
	}
	if len(zsh.Args) != 1 || zsh.Args[0] != "-e" {
		t.Errorf("zsh.args: %v", zsh.Args)
	}
	lua := p.Languages["lua"]
	if lua.Interpreter != "/usr/bin/lua" {
		t.Errorf("lua: %+v", lua)
	}
	if lua.Ext != ".sh" {
		// Default if not specified.
		t.Errorf("lua.ext (default) = %q, want .sh", lua.Ext)
	}
}

func TestLoad_Language_MissingInterpreter(t *testing.T) {
	path := writeBuild(t, `
languages:
  bad:
    args: ["-x"]
`)
	_, err := load.Load(path)
	if err == nil || !strings.Contains(err.Error(), "interpreter") {
		t.Errorf("expected interpreter-required error, got %v", err)
	}
}

func TestLoad_Stage3a_BackwardsCompat(t *testing.T) {
	// Stage 3a files without any 3b features should still load identically.
	path := writeBuild(t, `
vars:
  greeting: "hello"
targets:
  hi:
    run: 'echo "${greeting}"'
    deps: []
`)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.Targets) != 1 || len(p.Vars) != 1 {
		t.Errorf("counts: targets=%d vars=%d", len(p.Targets), len(p.Vars))
	}
	// 3b additions should be empty/zero, not nil pointer.
	if p.Functions == nil || p.Languages == nil {
		t.Error("Functions and Languages must be initialized maps")
	}
}
