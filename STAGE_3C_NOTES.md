# Stage 3c.1 — Templates

Released alongside Stage 3c.0 (project-root walk-up + `gmk.yml` rename).
This document records what 3c.1 added, what's not yet in 3c, and what
the user must do at first build.

## What 3c.1 adds

A pluggable text-rendering subsystem invoked from expressions via
`${render:template-name(args)}`. Templates are a first-class top-
level concept alongside `targets:` / `functions:` / `languages:`.
Engines are registered via package init; new engines drop in via
blank import.

### New top-level YAML block

```yaml
templates:
  greeting:
    engine: jinja                  # default; could omit
    body: |                         # inline form
      Hello, {{ name }}!

  dockerfile:
    engine: go                      # opt into stdlib text/template
    file: templates/dockerfile.tmpl # file form, relative to declaring YAML
    doc: Renders an application Dockerfile.
```

Exactly one of `body` and `file` must be set. The `engine:` field is
optional — empty means "use the registry default" (set to `jinja` in
`cmd/gmk/main.go`).

### New expression operator

```yaml
functions:
  emit-dockerfile:
    params:
      - {name: image,   type: string}
      - {name: version, type: string}
    prelude:
      content: "${render:dockerfile(image=image, version=version)}"
    run: |
      echo "$content" > Dockerfile
```

The operator parses for free (the parser's NamedCall already accepted
arbitrary `kind:` prefixes since Stage 3b); the only addition was a
new branch in `evalNamedCall` that dispatches to `RenderResolver`.

### Engines shipped

| Name      | Source                              | Notes                                   |
|-----------|-------------------------------------|-----------------------------------------|
| `jinja`   | `nikolalohinski/gonja/v2`           | Default. Active Jinja2-aligned Go port. |
| `go`      | `text/template` (stdlib)            | Zero new deps; stable; verbose syntax.  |

Two real adapters, build-tag-guarded: jinja's import line lives behind
`//go:build !nogonja` so `-tags nogonja` produces a minimal binary
without pulling gonja's transitive deps.

### File layout

```
internal/template/
├── engine.go              # Engine interface + Registry + package Default
├── engine_test.go         # 12 tests
├── go_engine.go           # text/template adapter, auto-registers
└── jinja/
    ├── jinja.go           # //go:build !nogonja — gonja v2 adapter
    ├── jinja_stub.go      # //go:build nogonja — no-op stand-in
    └── jinja_test.go      # //go:build !nogonja — 6 tests

internal/render/
├── dispatcher.go          # RenderResolver impl, bridges expr ↔ template
└── dispatcher_test.go     # 10 tests

internal/expr/
├── eval.go                # RenderResolver interface, render kind dispatch
└── render_test.go         # 6 tests for the dispatch

internal/load/
├── load_3c_templates.go   # templates: block parser
└── load_3c_templates_test.go  # 10 load tests (+1 skip)

internal/cli/
└── render_cli_test.go     # 3 end-to-end CLI tests

examples/templates/
└── dockerfile/
    ├── gmk.yml
    └── templates/dockerfile.tmpl
```

### Plumbing changes

- `expr.Evaluator` gained a `Renders` field (alongside existing `Calls`)
- `resolve.ResolveStringInScopeWithRender(s, sc, renders)` added;
  old `ResolveStringInScope` becomes a thin nil-passthrough wrapper
- `cli/call.go` and `cli/run.go` construct a render dispatcher per
  invocation and thread it through
- `materialize.WriteScript` also constructs a render dispatcher (so
  the legacy WriteScript path stays in sync with the unified flow)
- `cmd/gmk/main.go` blank-imports the jinja subpackage and calls
  `template.SetDefault("jinja")` at startup

### Args contract

`${render:tmpl(args)}` args go through normal expression evaluation,
then convert to `map[string]any` via the existing `Value.ToJSON()`
helper (Go-native, not actual JSON marshaling). The engine receives
that map directly:

- gmk String  → Go string
- gmk Int     → Go int64
- gmk Float   → Go float64
- gmk Bool    → Go bool
- gmk None    → Go nil
- gmk List    → Go []any (recursive)
- gmk Map     → Go map[string]any (recursive)

Templates can be called with named args (`render:tmpl(image=ref, ver=v)`)
or positional (`render:tmpl("alpha", "beta")` → keyed `"0"`, `"1"`).

## What's not in 3c.1 (deferred)

- **Iteration combinators** (`map:`, `pmap:`, `filter:`) — that's
  Stage 3c.2, a separate chunk. Templates already deliver value
  without iteration; iteration without templates would deliver less.

- **Body-side rendering** — bash bodies can't currently call
  `r_render TEMPLATE_NAME JSON_ARGS` to do dynamic rendering at
  runtime. That requires parent-socket IPC (already on the deferred
  list). For 3c.1, render is expression-level only — which is fine
  for ~95% of real use cases (prelude decides → body uses).

- **Cross-file template merging from includes** — currently the
  templates declared in an included YAML aren't visible at the
  including project's lookup. The test for "template declared
  twice across files" is skipped pending decision on whether
  template scoping mirrors function/var scoping (same project-wide
  flat namespace) or library-style (qualify with library prefix).
  Revisit when the first real-world cross-file use case arises.

- **`gmk list --templates` / `gmk doc <template-name>`** — `ir.Template`
  carries enough info to surface these, but the list/doc commands
  haven't been extended yet. Mechanical addition for a follow-up
  chunk.

## What the user must do after pulling

```bash
# First, delete the old build.yml files left over from Stage 3b:
find . -name build.yml -not -path './.git/*' -delete

# Then, since cmd/gmk/main.go now imports the jinja subpackage which
# imports gonja, fetch the new dep:
go mod tidy

# Then a clean build verifies everything:
make test

# For a minimal-deps build (no jinja, smaller binary):
go build -tags nogonja ./...
```

The `go mod tidy` call will add `github.com/nikolalohinski/gonja/v2`
to the require list along with its transitive deps (logrus,
golang.org/x/sys, gopkg.in/yaml.v3, gopkg.in/check.v1). All are
pure-Go, no CGO.

## Counts

374 baseline tests (3b) + 10 (3c.0 discovery) + 41 (3c.1 templates) = ~425
tests passing across 17 packages.

3c.1 test breakdown:
- internal/template: 12 (engine + go engine)
- internal/template/jinja: 6 (jinja engine, gated on !nogonja)
- internal/render: 10 (dispatcher)
- internal/expr: 6 (render kind dispatch)
- internal/load: 10 (templates: block parsing)
- internal/cli: 3 (end-to-end through gmk run)
- examples/templates/dockerfile/: 1 worked example, verified by
  running the actual gmk binary

## Files changed/added vs Stage 3c.0 baseline

Added:
- internal/template/engine.go
- internal/template/engine_test.go
- internal/template/go_engine.go
- internal/template/jinja/jinja.go
- internal/template/jinja/jinja_stub.go
- internal/template/jinja/jinja_test.go
- internal/render/dispatcher.go
- internal/render/dispatcher_test.go
- internal/expr/render_test.go
- internal/load/load_3c_templates.go
- internal/load/load_3c_templates_test.go
- internal/cli/render_cli_test.go
- examples/templates/dockerfile/gmk.yml
- examples/templates/dockerfile/templates/dockerfile.tmpl
- STAGE_3C_NOTES.md (this file)

Modified:
- internal/ir/types.go (added Template type; Project.Templates,
  Project.TemplateOrder)
- internal/load/load.go (templates: dispatch; allow templates as
  top-level key; init Templates map in emptyProject)
- internal/expr/eval.go (RenderResolver interface; render kind
  dispatch in evalNamedCall)
- internal/resolve/resolve.go (ResolveStringInScopeWithRender)
- internal/materialize/materialize.go (WriteScript uses render-aware
  resolution)
- internal/cli/call.go (renderDisp threading; evaluatePrelude
  signature gains renders)
- internal/cli/run.go (renderDisp construction; render-aware body
  resolution)
- internal/integration/examples_test.go (consistency test uses
  render-aware resolution to accept template-using examples)
- cmd/gmk/main.go (blank-import jinja; template.SetDefault("jinja"))
- README.md (Project layout section mentions templates)
- docs/LAYOUT.md (new Templates section)
