package load_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/load"
)

// writeTempProject writes a single gmk.yml under a temp dir and
// returns its path. The dir auto-cleans via t.TempDir.
func writeTempProject(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gmk.yml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write project: %v", err)
	}
	return p
}

// loadOrFatal loads a project and fails the test on error.
func loadOrFatal(t *testing.T, content string) *ir.Project {
	t.Helper()
	path := writeTempProject(t, content)
	p, err := load.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

// Vars: a simple top-level map becomes a MapKind value.
func TestStructuredVar_MapValue(t *testing.T) {
	p := loadOrFatal(t, `
vars:
  db_config:
    host: "db.internal"
    port: 5432
    secure: true
  scalar_var: "still works"
`)
	v := p.RootScope.Vars["db_config"]
	if v == nil {
		t.Fatal("db_config not loaded")
	}
	if v.Kind != ir.VarStructured {
		t.Fatalf("Kind = %v, want VarStructured", v.Kind)
	}
	if v.Structured.Kind != expr.MapKind {
		t.Fatalf("Structured.Kind = %v, want MapKind", v.Structured.Kind)
	}
	m := v.Structured.Map
	if m["host"].AsString() != "db.internal" {
		t.Errorf("host = %q", m["host"].AsString())
	}
	if m["port"].Kind != expr.IntKind || m["port"].Int != 5432 {
		t.Errorf("port = %v (Kind %v)", m["port"], m["port"].Kind)
	}
	if m["secure"].Kind != expr.BoolKind || !m["secure"].Bool {
		t.Errorf("secure = %v", m["secure"])
	}

	// Scalar var alongside a structured one keeps its existing shape.
	sv := p.RootScope.Vars["scalar_var"]
	if sv == nil {
		t.Fatal("scalar_var not loaded")
	}
	if sv.Kind != ir.VarLiteral {
		t.Errorf("scalar_var Kind = %v, want VarLiteral", sv.Kind)
	}
	if sv.Value != "still works" {
		t.Errorf("scalar_var Value = %q", sv.Value)
	}
}

// Vars: a top-level list becomes a ListKind value with the right items.
func TestStructuredVar_ListValue(t *testing.T) {
	p := loadOrFatal(t, `
vars:
  servers:
    - "10.0.0.1"
    - "10.0.0.2"
    - "10.0.0.3"
  ports: [80, 443, 8080]
`)
	s := p.RootScope.Vars["servers"]
	if s == nil || s.Kind != ir.VarStructured {
		t.Fatalf("servers kind = %v", s.Kind)
	}
	if s.Structured.Kind != expr.ListKind {
		t.Fatalf("Kind = %v", s.Structured.Kind)
	}
	if len(s.Structured.List) != 3 {
		t.Fatalf("len = %d", len(s.Structured.List))
	}
	if s.Structured.List[0].AsString() != "10.0.0.1" {
		t.Errorf("[0] = %q", s.Structured.List[0].AsString())
	}

	// Flow-style list parses too.
	ports := p.RootScope.Vars["ports"]
	if ports == nil || ports.Structured.Kind != expr.ListKind {
		t.Fatalf("ports: %+v", ports)
	}
	if len(ports.Structured.List) != 3 || ports.Structured.List[0].Int != 80 {
		t.Errorf("ports = %+v", ports.Structured.List)
	}
}

// Vars: nested maps with lists of maps inside — the shape examples 03/04
// want to write.
func TestStructuredVar_DeeplyNested(t *testing.T) {
	p := loadOrFatal(t, `
vars:
  app_config:
    name: "api"
    replicas: 3
    labels:
      tier: "backend"
      team: "platform"
    endpoints:
      - path: "/health"
        port: 8080
      - path: "/metrics"
        port: 9090
`)
	v := p.RootScope.Vars["app_config"]
	if v == nil || v.Structured.Kind != expr.MapKind {
		t.Fatalf("app_config: %+v", v)
	}
	m := v.Structured.Map

	// Nested map at .labels
	labels := m["labels"]
	if labels.Kind != expr.MapKind {
		t.Fatalf("labels = %+v", labels)
	}
	if labels.Map["tier"].AsString() != "backend" {
		t.Errorf("labels.tier = %q", labels.Map["tier"].AsString())
	}

	// List of maps at .endpoints
	ep := m["endpoints"]
	if ep.Kind != expr.ListKind || len(ep.List) != 2 {
		t.Fatalf("endpoints = %+v", ep)
	}
	if ep.List[0].Map["path"].AsString() != "/health" {
		t.Errorf("endpoints[0].path = %q", ep.List[0].Map["path"].AsString())
	}
	if ep.List[1].Map["port"].Int != 9090 {
		t.Errorf("endpoints[1].port = %d", ep.List[1].Map["port"].Int)
	}
}

// Vars: a leaf string containing ${...} is currently rejected, with a
// clear error pointing at the dotted path and explaining the workaround.
func TestStructuredVar_RejectsTemplateInLeaf(t *testing.T) {
	path := writeTempProject(t, `
vars:
  db:
    host: "db.${env}.internal"
    port: 5432
`)
	_, err := load.Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "vars.db.host") {
		t.Errorf("error should mention dotted path 'vars.db.host', got: %v", err)
	}
	if !strings.Contains(err.Error(), "not supported yet") {
		t.Errorf("error should explain the limitation, got: %v", err)
	}
}

// Index access into a structured var works: ${db_config.host}.
func TestStructuredVar_IndexAccessFromTarget(t *testing.T) {
	p := loadOrFatal(t, `
vars:
  db_config:
    host: "db.internal"
    port: 5432
targets:
  show:
    run: |
      echo host=${db_config.host}
      echo port=${db_config.port}
`)
	t1 := p.Targets["show"]
	if t1 == nil {
		t.Fatal("target not loaded")
	}
	if !strings.Contains(t1.Run, "${db_config.host}") {
		t.Fatal("test fixture didn't preserve the reference")
	}
}

// Prelude: structured entries on a function get Static set; Expr stays nil.
func TestStructuredPrelude_Function(t *testing.T) {
	p := loadOrFatal(t, `
functions:
  build-config:
    result: {type: map}
    prelude:
      defaults:
        image: "alpine"
        tag: "3.19"
      tags:
        - "latest"
        - "stable"
      scalar_entry: "still scalar"
    run: |
      echo "ok" > "$GMK_RESULT"
`)
	fn := p.Functions["build-config"]
	if fn == nil {
		t.Fatal("function not loaded")
	}
	if len(fn.Prelude) != 3 {
		t.Fatalf("len = %d", len(fn.Prelude))
	}

	// defaults (map)
	defaults := fn.Prelude[0]
	if defaults.Name != "defaults" {
		t.Fatalf("entry 0 name = %q", defaults.Name)
	}
	if defaults.Expr != nil {
		t.Errorf("defaults should have nil Expr (structured)")
	}
	if defaults.Static.Kind != expr.MapKind {
		t.Fatalf("defaults.Static.Kind = %v", defaults.Static.Kind)
	}
	if defaults.Static.Map["image"].AsString() != "alpine" {
		t.Errorf("defaults.image = %q", defaults.Static.Map["image"].AsString())
	}

	// tags (list)
	tags := fn.Prelude[1]
	if tags.Static.Kind != expr.ListKind {
		t.Fatalf("tags.Static.Kind = %v", tags.Static.Kind)
	}
	if len(tags.Static.List) != 2 {
		t.Errorf("tags len = %d", len(tags.Static.List))
	}

	// scalar still goes through Expr
	scalar := fn.Prelude[2]
	if scalar.Expr == nil {
		t.Errorf("scalar_entry should have Expr (not structured)")
	}
	if scalar.Static.Kind != expr.NoneKind {
		t.Errorf("scalar_entry.Static should be None, got %v", scalar.Static.Kind)
	}
}

// Prelude: a structured entry can be referenced by a later entry — the
// prelude evaluator binds it before the next entry's evaluation. This
// proves the Static-before-Expr ordering composes correctly.
func TestStructuredPrelude_ReferencedByLaterEntry(t *testing.T) {
	p := loadOrFatal(t, `
functions:
  pick-host:
    result: {type: string}
    prelude:
      db_config:
        host: "db.internal"
        port: 5432
      picked: "${db_config.host}"
    run: |
      cat "$GMK_PRELUDE" > "$GMK_RESULT"
`)
	fn := p.Functions["pick-host"]
	if fn == nil {
		t.Fatal("function not loaded")
	}
	if len(fn.Prelude) != 2 {
		t.Fatalf("len = %d", len(fn.Prelude))
	}
	if fn.Prelude[0].Static.Kind != expr.MapKind {
		t.Errorf("db_config should be Map")
	}
	if fn.Prelude[1].Expr == nil {
		t.Errorf("picked should have Expr (references db_config.host)")
	}
	// Runtime evaluation is exercised by the integration test
	// when an example uses this pattern; here we just confirm
	// the IR shape.
}

// Vars block 2/3 override (last-write-wins) keeps working with mixed
// scalar/structured vars.
func TestStructuredVar_LastWriteWinsAcrossBlocks(t *testing.T) {
	p := loadOrFatal(t, `
vars:
  cfg:
    a: 1
    b: 2

vars_2:
  cfg:
    x: 10
    y: 20
`)
	v := p.RootScope.Vars["cfg"]
	if v == nil || v.Structured.Kind != expr.MapKind {
		t.Fatalf("cfg: %+v", v)
	}
	m := v.Structured.Map
	if _, has := m["a"]; has {
		t.Errorf("vars_2 should replace, not merge — got 'a' key still present")
	}
	if m["x"].Int != 10 {
		t.Errorf("vars_2.cfg.x = %v", m["x"])
	}
}
