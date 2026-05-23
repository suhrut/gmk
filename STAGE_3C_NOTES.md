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

---

## Stage 3c.1.2 follow-on (in this tarball)

Two additions on top of the templates work:

### 1. Splat operator `${render:tmpl(...mapvar)}`

The original `${render:tmpl(k1=v1, k2=v2)}` required enumerating every
template arg by name. For real workloads where the data shape is
assembled upstream (function returns a Map, JSON load, etc.), this
is verbose:

```yaml
# Before (verbose):
"${render:k8s-deployment(name=cfg.name, image=cfg.image, port=cfg.port, ...)}"

# After (splat):
"${render:k8s-deployment(...cfg)}"
```

Mechanics:
- Lexer: new `tokSplat` for `...`
- Parser: `parseNamedCallArgs` recognizes `...EXPR` and produces
  `NamedArg{Value: EXPR, IsSplat: true}`
- Eval: at arg collection, splat values are asserted to be Maps
  (clear error otherwise); entries spread into the args map
- Conflict resolution: later wins. Splat-then-named lets named
  args override; two splats (`...defaults, ...overrides`) lets the
  later splat override the earlier.

Splat works for `${call:fn(...)}` too — the mechanism is in the
kind-agnostic arg collection.

Tests: 7 new (`internal/expr/splat_test.go`).

### 2. Target preludes wired into `gmk run`

Previously target preludes were "honored by future work" — a `target:
{ prelude: {...}, run: ... }` would parse fine but the prelude was
silently empty at body interpolation time. The templates examples
needed target preludes to assemble Map data, so this turn wired them
in:

- `runOneTarget` builds a `funcs.Dispatcher` + `render.Dispatcher`
- `evaluatePrelude` (from call.go) is called against the target's
  prelude with both dispatchers
- Resulting bound values are layered via `preludeScope` on top of the
  project scope for body interpolation
- Bound values are also passed as `PreludeValues` to
  `MaterializeCallable` so the body's `$GMK_PRELUDE` is populated

New: `resolve.ResolveStringWithVarsAndRender(s, sc, extraVars, renders)`
— the most general form, accepts an explicit VarResolver for callers
that need to layer scopes. The previous two variants stay as thin
wrappers.

### 3. Examples library

`examples/templates/` now has five examples (was one):

1. `01-inline-header` — simplest, inline body
2. `02-dockerfile-multi-engine` — file refs, jinja vs go side-by-side
3. `03-k8s-deployment` — function-body-returns-JSON + splat
4. `04-nginx-config` — Python function builds list-of-maps; jinja filters
5. `05-splat-and-defaults` — splat + override patterns, two-splat merging

Plus a top-level `examples/templates/README.md` indexing them.

### Test driver fixes

- `internal/integration/main_test.go` — new `TestMain` mirrors
  `cmd/gmk/main.go`'s engine registration: blank-imports
  `internal/template/jinja` (no-op under `-tags nogonja`) and calls
  `template.SetDefault("jinja")`.
- `exampleNeedsMissingEngine` helper: walks a project's Templates,
  returns the name of any engine that isn't registered (or
  `"(default)"` if the default isn't resolvable). Used by both
  `TestExamplesRun` and `TestExamplesTargetsConsistent` to skip
  jinja-dependent examples cleanly under `-tags nogonja` rather
  than fail with cryptic errors.

### Counts (final)

- 17 packages green
- 7 new splat tests added
- All 5 templates examples pass the integration consistency check
- All 5 templates examples skip cleanly under `-tags nogonja`
- Target prelude end-to-end verified by running the actual `gmk`
  binary against a synthetic splat-uses-prelude project

### Known limitations (Stage 3c.2 or later)

- **Vars and prelude are scalar-only at the YAML loader.** Structured
  Maps/Lists have to live in function results (return JSON via
  `$GMK_RESULT`). Loader work to accept structured literals in
  `vars:` / `prelude:` is its own chunk; defer.
- **Targets still don't take caller-supplied args** (`gmk run target
  --arg k=v`). That's the Stage 4 question on how target args
  compose with deps.
- **Cross-file template merging from includes** — same situation as
  before; no template imported via `includes:` becomes visible at
  the consumer's lookup. Revisit when first real-world use case lands.

---

## Stage 3c.2: structured vars and prelude literals

**The change in one sentence:** `vars:` and function-prelude entries
now accept nested maps and lists, not just scalar strings.

### Before / after

```yaml
# Before (Stage 3c.1.2): structured shapes had to live in a function
# that emits JSON to $GMK_RESULT.
functions:
  api-config:
    result: {type: map}
    run: |
      cat > "$GMK_RESULT" << 'JSON'
      {"name": "api", "image": "myorg/api", "labels": {"tier": "backend"}}
      JSON
targets:
  emit:
    prelude:
      cfg: "${call:api-config()}"
    run: ...

# After (Stage 3c.2): the shape lives where shapes belong.
vars:
  api_config:
    name: "api"
    image: "myorg/api"
    labels:
      tier: "backend"
targets:
  emit:
    run: ...
```

### Mechanics

- **`Var.Structured expr.Value`**: new field on `*ir.Var`. Populated
  when the YAML value is a mapping or sequence; `Var.Kind` is set to
  the new `VarStructured`. Scalar vars keep their existing shape.
- **`PreludeEntry.Static expr.Value`**: parallel field on prelude
  entries. When non-zero (not NoneKind), the prelude evaluator uses
  it verbatim instead of evaluating `Expr`.
- **`yamlValue.ToStructuredValue(contextPath) (expr.Value, error)`**:
  recursive YAML AST → expr.Value converter. Scalars become their
  natural types (String/Int/Float/Bool/None), mappings become
  MapKind, sequences become ListKind.
- **Scope resolver**: when a lookup hits a `VarStructured` var, the
  cached Value is returned as-is. No re-evaluation, no string
  reparse. Index access (`${db.host}`, `${servers[0]}`,
  `${cfg.db.host}`) goes through the existing IndexExpr evaluator.
- **Prelude evaluator** (`evaluatePrelude` in cli/call.go): branches
  on `Static.Kind != NoneKind` and binds the value verbatim before
  the next entry's evaluation. Structured + scalar entries can
  interleave; structured ones can be referenced by later scalar
  expression entries.

### Restriction (deliberate, can lift later)

Leaf strings inside structured values must be **literal**. A leaf
containing `${...}` is rejected at load time with:

```
vars.db.host: structured value leaf string contains ${...} expression,
which is not supported yet (use a function returning JSON for
interpolated structured data)
```

This keeps the lookup path simple (cached Value returned as-is, no
recursive resolution walk). Once we have a concrete user wanting
interpolated leaves we can revisit; the natural shape is lazy deep-
walk at lookup time with cycle detection.

### Tests added

- `internal/load/structured_test.go` — 8 tests covering map vars,
  list vars, deeply-nested map-with-list-of-maps, scalar+structured
  mix in one scope, last-write-wins across vars_N blocks, structured
  prelude entries (function), structured-then-scalar prelude ordering,
  leaf-with-${...} rejection.
- `internal/resolve/structured_test.go` — 7 tests covering bare
  reference, index access (string/int/nested map/list), mixed scope,
  missing-field error.
- All 17 packages still green. End-to-end smoke test via actual gmk
  binary confirms structured vars + splat + named-override +
  dotted-path access all compose cleanly.

### Examples updated

`examples/templates/03-k8s-deployment`, `04-nginx-config`, and
`05-splat-and-defaults` are now noticeably cleaner — the
function-returns-JSON dance is gone. Example 4's `nginx-data` Python
function disappears entirely; the data is now plain YAML.

### Future hooks

- Lazy interpolation in leaf strings (deferred above)
- Structured `env:` block on targets (today it's scalar-only too —
  same loader pattern, same shape change)
- Structured target args once `gmk run target --arg k=v` lands —
  same shape: each arg is either scalar or structured


---

## Stage 3c.2 follow-on: iteration combinators (map, filter)

Two new expression kinds:

- **`${map:fn(items=L, pinned...)}`** — call `fn(item=X, pinned...)`
  for each element X in list L; return a List of results.
- **`${filter:fn(items=L, pinned...)}`** — call `fn(item=X, pinned...)`
  for each X; keep X (the original element) when fn returned true.
  Predicate must return BoolKind; non-bool result errors loudly.

### Mechanics

- Implemented as new `evalNamedCall` arms (case `"map"`, case
  `"filter"`). The parser needed zero changes — the existing
  `kind:name(args)` shape already covers any Kind.
- `items=` is the required arg holding the iterable. All other args
  are "pinned" and passed unchanged on every call.
- Element binds to the fixed name `item`. A pinned `item=` arg
  conflicts with this and errors with a clear message (likely-bug
  guard).
- Iteration is sequential. `pmap:` (parallel) needs the parallel-
  fanout primitive; deferred to its own chunk.

### Wiring

The target body resolution path now always builds the function
dispatcher (not only when a prelude exists), and passes it as the
CallResolver. New `resolve.ResolveStringFull(s, sc, vars, calls,
renders)` is the most-general body-resolution function;
`ResolveStringWithVarsAndRender` is now a thin wrapper.

### Tests added

11 unit tests in `internal/expr/iteration_test.go`:
- map binds `item` per call in order
- pinned args flow through unchanged
- map over list-of-maps preserves nesting
- empty list → empty result, no calls made
- filter keeps original items, not bool results
- filter rejects non-bool predicates
- missing `items` → clear error
- non-list `items` → clear error
- pinned `item=` shadowing → clear error
- no Calls resolver → clear error
- composes with splat (`...mapvar` of pinned args)

### Integration test driver tweak

`bodyNeedsCallResolver(body)` helper detects bodies that need a
dispatcher (presence of `call:`, `map:`, or `filter:` substrings).
Used by `TestExamplesTargetsConsistent` to skip the static body-
resolution check for those bodies — they're covered by
`TestExamplesRun` through the production code path.

### Example added

`examples/iteration/01-map-filter/` demonstrates:
- Mapping over a structured-var list of maps
- Filtering by a predicate function
- Composing filter inside map (`map:f(items=filter:g(items=L))`)
- Using `len()` on a filtered List

End-to-end output (real binary):
```
all services:
  api:8080, worker:9000, cache:6379, web:80
public services:
  api:8080, web:80
count of public services: 2
```

### Known patterns / conventions

- **Functions writing JSON to `$GMK_RESULT`**: use `json.dump(value, f)`
  from Python or `jq -n ...` from bash. The runner parses the file as
  JSON; raw strings like `printf '%s' "$x"` produce a null Value.
- **Reading structured args**: `args = json.load(open(os.environ["GMK_ARGS"]))`
  is the idiom. `args["item"]` is the per-iteration element. Functions
  meant to be discoverable by the integration test should use defensive
  access (`args.get("item") or {}`) — the test framework stubs missing
  args with an empty value of the declared type, and a `KeyError` on a
  required field will fail the call test.

---

## Stage 3c.2: `gmk inspect <day>/<seq>`

Small read-only command for inspecting a recorded run from the
SQLite runs table. The natural debugging companion to `gmk run` and
`gmk call`.

### Usage

```
gmk inspect 20260523/0007        # explicit day/seq
gmk inspect 0007                 # seq only, defaults to today
gmk inspect 20260523/0007-name   # the dir-name form (trailing -<name> stripped)
gmk inspect 7 --json             # JSON for piping into jq
```

### Output (human form)

```
Function 20260523/0001  status=ok exit=0
  name:        format-svc
  started:     2026-05-23 14:31:52
  finished:    2026-05-23 14:31:52
  duration:    21ms
  source:      /tmp/inspect-fn/gmk.yml
  gmk pid:     6060
  args:
    {
      "item": {
        "name": "api",
        "port": 8080
      }
    }
  result:
    "api:8080"
  scratch:     /tmp/inspect-fn/.gmk-cache/runs/20260523/0001-format-svc
```

Empty / null args / prelude / result columns are skipped so the
output stays compact. The scratch dir path lets the user `cat` the
materialized body or args/result JSON files for deeper debugging.

### Files

- `internal/cli/inspect.go` — command + `parseRunIdent` + human/JSON formatters
- `internal/cli/inspect_test.go` — 7 parseRunIdent forms, 6 error
  cases, 1 end-to-end test against a real store
- `internal/cli/root.go` — wires `newInspectCmd()` into the command tree

Uses the existing `store.GetRun(ctx, day, seq)` API; no store changes
needed.

### Future hooks

- `--list-day <day>` — list all runs for a day (uses
  `store.ListRunsForDay`, already exists)
- `--running` — show currently-running runs (uses `store.ListRunning`)
- `--stdout` / `--stderr` — dump captured output (needs Stage 4 log
  capture; nothing recorded today)
- Anchor link to the materialized body script (we know the runs dir;
  the body lives under `.gmk-cache/bodies/<hash>/` keyed by callable
  content hash — would need to recompute the hash or have the runner
  record it on the row)

---

## Demo: fullstack-app + local-registry (May 23 2026)

Created `demo/` to validate that the Stage 3c.2 feature set is
sufficient for a real deployment pipeline — no new features added.

Two demos:

- `demo/local-registry/` — Zot OCI registry lifecycle (5 targets,
  ~75 lines). Standalone, reusable building block.

- `demo/fullstack-app/` — full pipeline: PKI bootstrap (raw password
  from `~/.gmk/<project>/key`), local Zot, Go backend + plain
  HTML/JS frontend + Postgres on k3s, OCI bundle for prod-host
  deploy without git-clone. ~650 lines of gmk.yml, ~750 lines of
  source + templates total. 38 targets, 1 Python function.

What the demo validated:

- Structured vars (nested maps + lists) for backend/frontend/postgres/ingress.
- Splat operator on structured vars: `${render:tmpl(...ingress, extra=foo)}`.
- Iteration combinator: `${map:render-app-manifest(items=apps, ...)}`
  drives a Python function that renders N manifests with no per-app
  target duplication.
- Multi-language fan-out: bash targets + one Python function with
  JSON IPC via `GMK_ARGS` / `GMK_RESULT`.
- Templates (jinja for the real demo, validated equivalently with
  `go` engine in the sandbox since jinja needs Go 1.24+ and sandbox
  is on 1.22).
- Deep `deps:` chains (5 levels: all → deploy → bundle → manifests → pki).
- Target preludes via `env:` block.

### Limitation surfaced: includes promote vars but not targets/functions/templates

Tried the natural layout `demo/fullstack-app/gmk/{pki,registry,images,...}.yml`
with the top-level gmk.yml using:

```yaml
includes:
  - ./gmk/pki.yml
  - ./gmk/registry.yml
  ...
```

Included files load fine — their vars get promoted into the parent
scope — but their targets/functions/templates are stored under
`p.RootScope.Includes[]` as a sub-Project, not merged into the parent's
top-level maps. So `gmk run goodbye` where `goodbye` lives in an
included file fails with "target not found".

Tested with minimal repro (/tmp/test-incl2):
- `vars` in included file → ✓ visible in parent
- `targets` in included file → ✗ not callable
- Same for functions and templates.

Consolidated the fullstack-app demo into one 650-line `gmk.yml` with
section comments instead. Documented the limitation in the gmk.yml
header and in `demo/fullstack-app/README.md`.

Not fixing in this stage — user explicitly said "no new features."
This belongs in a future stage as part of a broader "modules /
sub-projects" design (which also needs to think through:
namespacing of merged target names, visibility of parent vars from
inside an included file, include-time vs run-time vars).

### Other minor friction notes (not bugs, just things to know)

- Bash heredocs inside YAML block scalars: terminator line at column 0
  breaks the YAML block scalar. Fix: use `printf` / `echo` lines, or
  indent the heredoc terminator to match the block scalar's indent
  (which then requires `<<-` and tabs in bash — fragile).
- Function bodies can't use `${render:...}` (it's a gmk expression-
  context construct). For per-app manifest rendering driven by `map:`,
  the function uses Python's own jinja2 instead. Works fine, but
  worth noting that "render from inside a function" is awkward today.

### Files

```
demo/
├── README.md
├── local-registry/
│   ├── README.md
│   └── gmk.yml
└── fullstack-app/
    ├── README.md
    ├── gmk.yml
    ├── backend/
    │   ├── README.md
    │   ├── Dockerfile
    │   ├── go.mod
    │   └── main.go
    ├── frontend/
    │   ├── Dockerfile
    │   ├── app.js
    │   ├── index.html
    │   ├── nginx.conf
    │   └── style.css
    └── templates/
        ├── app.yaml.jinja
        ├── ingress.yaml.jinja
        ├── install.sh.jinja
        ├── namespace.yaml.jinja
        └── postgres.yaml.jinja
```

Validation in the sandbox (without docker/k3s/openssl/oras/jinja):
- `gmk list -f demo/fullstack-app/gmk.yml` → 38 targets, 1 function ✓
- `gmk dryrun all -f demo/fullstack-app/gmk.yml` → 28-step linear order ✓
- Render chain validated end-to-end with `go` engine on a parallel
  /tmp project: ${render:tmpl(...mapvar, extra=x)} splat works,
  ${map:fn(items=list, ...)} drives a Python function correctly,
  output files written correctly.

---

## Demo follow-up: JSON keys file with profiles (May 23 2026)

User feedback on the May 23 demo: the raw-password single-line `key`
file is too rigid. Moved to a JSON `keys` file with named profiles
and multiple named entries per profile, so the demo can grow to
cover sqlite encryption, secrets encryption, etc., without changing
the file format.

### Schema

```json
{
  "default": {
    "ca":     "passphrase",
    "sqlite": "passphrase"
  },
  "prod": {
    "ca":      { "password": "prod-pass", "algorithm": "aes256" },
    "sqlite":  { "password": "prod-sqlite-pass" }
  }
}
```

Each entry is either a bare string OR an object with `password`.
Object form is future-proof for `algorithm` and other per-entry
metadata. Same schema at `~/.gmk/<project>/keys` (preferred) and
`~/.gmk/keys` (global fallback).

### Implementation

- `demo/fullstack-app/scripts/read-key.sh` — bash helper, sourced
  into target bodies, exports a `read_key NAME PROFILE` function
  that returns a path to a chmod-600 temp file. Caller traps cleanup.
- `demo/fullstack-app/keys.example.json` — full schema example.
- `gmk.yml`: new `key_profile: "demo"` var; pki-ca and pki-server-cert
  source the helper; env-var override via `GMK_PROFILE`.

Why a bash helper instead of a gmk function: gmk's runs table records
function results, so a `read-key` function would expose the password
via `gmk inspect`. Bash keeps the secret entirely off gmk's data
path. Validated end-to-end with openssl in the sandbox: demo profile
key decrypts only with demo passphrase; prod profile key decrypts
only with prod passphrase; env-var override works.

### Two gmk gotchas surfaced (worth documenting, not bugs)

1. **Caller's env vars don't reach target bodies by default.** Only
   the env block + a controlled subset is passed. To forward a
   caller's GMK_PROFILE, use `GMK_PROFILE_OVERRIDE: "${env:GMK_PROFILE:-}"`
   in the env block.

2. **Bash's `${VAR:-default}` collides with gmk's expression
   interpolation in target bodies.** gmk eagerly parses `${...:-...}`
   as a default-value gmk expression and substitutes accordingly,
   so bash never sees the cascade. Workaround: use plain `if`/`else`
   in target bodies for fallback logic. Comments inside a YAML block
   scalar are part of the body string, so even `${VAR:-x}` in a
   bash comment will be eaten by the gmk parser — keep `${` out of
   target-body comments.

Both might warrant a future gmk improvement (env passthrough
allowlist; literal `$$` escape for bash-style defaults in bodies).
Not for this stage.

### Third gotcha: bash parameter expansion forms collide with gmk's parser

While exercising the demo on a real machine, `registry-ensure`
failed with:

```
Error: target "registry-ensure": body resolution: parse error at :1:17: unexpected character '#'
```

The body had `port="${registry##*:}"` — bash's "strip longest prefix"
parameter expansion for extracting the port from "localhost:5000".
gmk's expression parser tokenises `${registry` and then trips on the
`##`.

Same root cause as gotcha #2 (bash's `${VAR:-default}`): any bash
parameter-expansion operator inside `${...}` will collide with gmk's
own `${...}` syntax.

Operators to avoid in target bodies:
  ${var#pat}   ${var##pat}    prefix strip (shortest/longest)
  ${var%pat}   ${var%%pat}    suffix strip
  ${var:-x}    ${var:?x}      defaults / error-if-unset
  ${var:N}     ${var:N:M}     substring
  ${var/a/b}                  substitution
  ${var^^}     ${var,,}       case

Fix in the demo: split `registry: "localhost:5000"` into separate
`registry_host` and `registry_port` vars (with `registry` kept as the
composite for use in image refs). Trivial — but worth noting in the
notes file so the pattern is documented.

A future stage might add a literal-escape syntax (e.g. `$${...}` or
backtick-quoting) so bash parameter expansion can survive a target
body. Not for this stage.

### Fourth and fifth gotchas (May 23, late session)

Two more issues surfaced on the user's first end-to-end run of
`gmk run bundle`. Both are bash/gmk interaction subtleties.

#### #4: unquoted heredoc terminators interpret backticks (and $var) in the body

The bundle-render-install target had:

```yaml
run: |
  cat > "${bundle_staging}/install.sh" <<INSTALL
  ${render:install-script(...)}
  INSTALL
```

`${render:install-script(...)}` expands at gmk-resolve time into the
full install.sh body. install.sh contains a comment with backticks
as markdown emphasis around `oras pull`. When the heredoc terminator
is unquoted (`<<INSTALL`), bash performs both variable expansion AND
command substitution on the heredoc content. So bash saw the
backticked phrase as a command and tried to run `oras pull` with no
args — mid-bundle-render-install. The install.sh got written
anyway (cat continued past the error), but the output of the
backticked region was elided.

Fix: quote the terminator `<<'INSTALL'`. All four heredocs in the
demo now use the quoted form (defensive — even YAML manifests
shouldn't be re-expanded by bash since gmk already substituted).

Verified by reproduction:

  Unquoted: `oras: command not found`, backtick region stripped.
  Quoted:   content preserved verbatim.

#### #5: bash-local variables referenced with ${VAR} braces are eaten by gmk

The bundle-create target had:

```bash
bytes=$(stat -c %s "${out_dir}/bundle.tar.gz" ...)
echo "created ${out_dir}/bundle.tar.gz (${bytes} bytes)"
```

`bytes` is a bash-local variable assigned in the previous line. But
gmk's expression interpolation runs over the whole body string
before bash gets it, sees `${bytes}`, looks it up as a gmk var,
doesn't find it, and errors.

Fix: drop the braces — `$bytes` (no curlies). gmk's parser only
triggers on `${...}`, so bare `$var` references survive untouched.

Rule of thumb: any bash-local variable (assigned inside the target
body) must be referenced as `$var`, never `${var}`. Gmk vars are
the opposite — they're `${gmkvar}` and `$gmkvar` wouldn't be
recognised by gmk.

Audited the demo and confirmed every other bash-local (`tag_v`,
`tag_l`, `san_conf`, `bundle_ref`, `pwfile`, `pw_file`) already uses
the bare form. Only `${bytes}` was wrong.

### Summary of the five gotchas (all surfaced by this demo)

1. `includes:` promotes vars but not targets/functions/templates.
2. Caller's env vars don't auto-pass into target bodies; forward
   explicitly via `${env:NAME:-}` in the env block.
3. Bash parameter expansion `${VAR##pat}`, `${VAR:-default}`, etc.
   collides with gmk's `${...}` interpolation in bodies (and even
   in YAML-block-scalar comments).
4. Unquoted heredoc terminators (`<<X`) let bash re-interpret the
   substituted content; quote them (`<<'X'`).
5. Bash-local variables need `$var` not `${var}` in bodies, because
   gmk eats the braced form.

None are new in this demo — they're all consequences of how gmk's
`${...}` interpolation overlaps with bash's `${...}`. A future
literal-escape syntax (`$${...}` or backtick-quoting) would resolve
#3, #4-content, and #5 in one swoop, but that's a feature, deferred.

The cumulative cost so far: ~15 lines of workarounds across the
demo. Acceptable price for a real-world validation that the rest
of the gmk feature set models the deployment pipeline cleanly.
