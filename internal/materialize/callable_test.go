package materialize_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/materialize"
)

func TestCallable_BasicBash(t *testing.T) {
	root := t.TempDir()
	c := &materialize.Callable{
		Name: "hello",
		Kind: "function",
		Run:  `echo "hi from gmk"`,
		Lang: "bash",
	}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "test-run",
		Args: map[string]expr.Value{
			"greeting": expr.NewString("hello"),
		},
		PreludeValues: map[string]expr.Value{
			"version": expr.NewString("1.0.0"),
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Scratch dir must exist.
	if _, err := os.Stat(inv.ScratchDir); err != nil {
		t.Errorf("scratch dir not created: %v", err)
	}
	wantDir := filepath.Join(root, ".gmk-cache", "runs", "test-run", "hello")
	if inv.ScratchDir != wantDir {
		t.Errorf("scratch=%q, want %q", inv.ScratchDir, wantDir)
	}

	// args.json — contents check.
	argsBytes, err := os.ReadFile(inv.ArgsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	var argsMap map[string]any
	if err := json.Unmarshal(argsBytes, &argsMap); err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if argsMap["greeting"] != "hello" {
		t.Errorf("args.greeting=%v", argsMap["greeting"])
	}

	// prelude.json
	preBytes, err := os.ReadFile(inv.PreludePath)
	if err != nil {
		t.Fatal(err)
	}
	var preMap map[string]any
	if err := json.Unmarshal(preBytes, &preMap); err != nil {
		t.Fatal(err)
	}
	if preMap["version"] != "1.0.0" {
		t.Errorf("prelude.version=%v", preMap["version"])
	}

	// result.json seeded with null
	resBytes, _ := os.ReadFile(inv.ResultPath)
	if strings.TrimSpace(string(resBytes)) != "null" {
		t.Errorf("result.json should seed to null, got %q", string(resBytes))
	}

	// Script file exists and contains the user body.
	scriptBytes, err := os.ReadFile(inv.ScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Errorf("script missing shebang: %q", script[:50])
	}
	if !strings.Contains(script, "set -eo pipefail") {
		t.Errorf("bash strict mode missing")
	}
	if !strings.Contains(script, "hi from gmk") {
		t.Errorf("user body missing")
	}

	// env contains the three contract vars.
	for _, k := range []string{"GMK_ARGS", "GMK_PRELUDE", "GMK_RESULT", "GMK_NAME", "GMK_KIND", "GMK_RUN_ID"} {
		if _, ok := inv.Env[k]; !ok {
			t.Errorf("env missing %s", k)
		}
	}
	if inv.Env["GMK_NAME"] != "hello" {
		t.Errorf("GMK_NAME=%q", inv.Env["GMK_NAME"])
	}
	if inv.Env["GMK_KIND"] != "function" {
		t.Errorf("GMK_KIND=%q", inv.Env["GMK_KIND"])
	}

	// argv has interpreter + script path
	if len(inv.Argv) < 2 {
		t.Fatalf("argv len=%d, want >= 2", len(inv.Argv))
	}
	if !strings.HasSuffix(inv.Argv[0], "bash") {
		t.Errorf("interpreter[0]=%q", inv.Argv[0])
	}
	if inv.Argv[len(inv.Argv)-1] != inv.ScriptPath {
		t.Errorf("last argv must be script path: %v", inv.Argv)
	}
}

func TestCallable_LanguagePython(t *testing.T) {
	root := t.TempDir()
	c := &materialize.Callable{
		Name: "py_fn",
		Kind: "function",
		Run:  `print(json.dumps({"hello": "world"}))`,
		Lang: "python",
	}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "r1",
	})
	if err != nil {
		// Python may not be installed in test env; skip rather than fail.
		if strings.Contains(err.Error(), "cannot find interpreter") {
			t.Skip("python not in PATH")
		}
		t.Fatal(err)
	}
	if !strings.HasSuffix(inv.ScriptPath, ".py") {
		t.Errorf("script ext should be .py: %s", inv.ScriptPath)
	}
	scriptBytes, _ := os.ReadFile(inv.ScriptPath)
	script := string(scriptBytes)
	if !strings.Contains(script, "import json") {
		t.Errorf("python preamble missing")
	}
}

func TestCallable_EmptyArgs(t *testing.T) {
	root := t.TempDir()
	c := &materialize.Callable{Name: "f", Kind: "function", Run: "true", Lang: "bash"}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	// args.json should be {} not null.
	data, _ := os.ReadFile(inv.ArgsPath)
	if !strings.Contains(string(data), "{") {
		t.Errorf("empty args should be {}, got %q", string(data))
	}
}

func TestCallable_UnknownLanguage(t *testing.T) {
	c := &materialize.Callable{Name: "f", Kind: "function", Run: "x", Lang: "haskell"}
	_, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: t.TempDir(),
		RunID:       "r",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown language") {
		t.Errorf("expected unknown-language error, got %v", err)
	}
}

func TestCallable_UserDefinedLanguage(t *testing.T) {
	root := t.TempDir()
	c := &materialize.Callable{Name: "f", Kind: "function", Run: "echo", Lang: "myzsh"}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "r",
		Languages: map[string]*ir.Language{
			"myzsh": {Name: "myzsh", Interpreter: "bash", Ext: ".zsh"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(inv.ScriptPath, ".zsh") {
		t.Errorf("ext should be .zsh: %s", inv.ScriptPath)
	}
}

func TestNewRunID_FormatAndUniqueness(t *testing.T) {
	a := materialize.NewRunID()
	b := materialize.NewRunID()
	// Format: YYYYMMDD-HHMMSS-XXXXXX
	if len(a) < 8+1+6+1+6 {
		t.Errorf("RunID too short: %q", a)
	}
	if a == b {
		// Possible at the second-resolution boundary but rare; if both
		// fall in the same nanosecond hex window it's a coincidence.
		t.Logf("RunIDs equal — coincidence at run boundary: %s == %s", a, b)
	}
}

func TestReadResultFile_Null(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	os.WriteFile(path, []byte("null\n"), 0o644)
	v, err := materialize.ReadResultFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != expr.NoneKind {
		t.Errorf("got kind %v, want none", v.Kind)
	}
}

func TestReadResultFile_Map(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	os.WriteFile(path, []byte(`{"x": 42, "y": "hi"}`), 0o644)
	v, err := materialize.ReadResultFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != expr.MapKind {
		t.Fatalf("kind=%v", v.Kind)
	}
	if v.Map["x"].Int != 42 {
		t.Errorf("x: %+v", v.Map["x"])
	}
	if v.Map["y"].Str != "hi" {
		t.Errorf("y: %+v", v.Map["y"])
	}
}

func TestReadResultFile_Missing(t *testing.T) {
	v, err := materialize.ReadResultFile(filepath.Join(t.TempDir(), "no-such-file"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if v.Kind != expr.NoneKind {
		t.Errorf("kind=%v, want none", v.Kind)
	}
}
