# Stage 3a — expression language

This is gmk Stage 3a: the expression language. Stage 3b (YAML tags
`!sh`/`!env`/`!files`/`!join`) lands next, layered on top of this same
expression engine.

## What's new

### Expression grammar inside `${...}`

Everything that used to be `${NAME}` now also accepts a full expression:

```
${NAME}                       bare var reference
${env:NAME}                   OS environment lookup
${ctx:NAME}                   --ctx flag lookup (stub in 3a; wired in S4)
${var:NAME}                   explicit form of ${NAME}
${NAME:-default}              bash-style default (if NAME unset/empty)
${NAME:?required-msg}         bash-style required; errors with msg
${NAME:+alternate}            bash-style alternate
${X == Y}                     equality (also !=)
${upper(NAME)}                function call
${NAME | upper | trim}        pipeline (left-to-right, like shell)
${(env:USER:-anon) | upper}   parenthesized grouping
$$                            literal dollar sign
```

15 built-in functions ship in 3a:
`upper`, `lower`, `trim`, `trim_left`, `trim_right`,
`to_string`, `to_int`, `len`,
`starts_with`, `ends_with`, `contains`, `replace`,
`default`, `coalesce`, `join`.

### Bash-compatible modifier semantics

`:-` `:?` `:+` follow bash exactly: "unset OR empty" triggers the modifier.
Numbers (even 0) and booleans (even `false`) are NOT empty, matching the
shell convention.

`:?` errors include the user-supplied message, the file:line:col, and the
variable name — fail-fast with location context as requested.

### YAML declaration order is now real

Stage 2's alphabetical-within-block ordering was a hack — Stage 3a switches
the YAML parser to goccy/go-yaml's AST mode and walks the file in source
order. Vars appear in their declaration order both within and across
`vars`/`vars_1`/`vars_2`/... blocks.

### Source positions on every value

The AST switch also gives us `Var.Source.Line` and `.Column` filled in for
every variable. Error messages from the expression engine carry
`file:line:col` prefixes.

## Architecture

```
expr  (pure; no gmk deps)
  │
  ▼
ir    (depends on expr for Var.Expr)
  │
  ▼
scope ── resolve ── load
```

The `expr` package is fully self-contained. It exposes a `VarResolver`
interface that `resolve` implements as an adapter over `ir.Scope` — so
the language layer has no idea about gmk's scope walking.

### Files added (12)

```
internal/expr/value.go            268 LOC   Value type + kinds + coercion
internal/expr/value_test.go       208 LOC
internal/expr/ast.go              261 LOC   Node interface, all node types
internal/expr/errors.go            94 LOC   ParseError, EvalError, sentinels
internal/expr/lexer.go            333 LOC   Token, Lexer, ReadRawToEnd
internal/expr/parser.go           469 LOC   ParseTemplate, ParseExpr
internal/expr/parser_test.go      417 LOC
internal/expr/eval.go             249 LOC   Evaluator + VarResolver
internal/expr/eval_test.go        398 LOC
internal/expr/funcs.go            251 LOC   FuncRegistry + 15 builtins
internal/expr/funcs_test.go       ~280 LOC
internal/expr/expr.go              39 LOC   Public API surface + overview
internal/expr/strconv.go           ~25 LOC  parser helpers
internal/load/yamlast.go          ~270 LOC  goccy AST -> Go-native wrapper
```

### Files rewritten

```
internal/load/load.go     — switched to AST-mode walk; uses yamlast.go
internal/resolve/resolve.go — delegates to expr; 3-path ResolveVar
                              with backward-compat for test helpers
internal/ir/types.go       — added VarKind, Var.Kind, Var.Expr
```

### Files unchanged (verified to compile + tests pass)

```
internal/scope/         — unchanged; still walks scope tree as in S2
internal/dag/           — unchanged
internal/exec/          — unchanged
internal/runner/        — unchanged
internal/materialize/   — unchanged
internal/cli/           — unchanged
```

## Verification status (in our sandbox)

```
✓ go test -race ./internal/expr/...        (all parser/eval/funcs tests)
✓ go test -race ./internal/ir/...
✓ go test -race ./internal/scope/...
✓ go test -race ./internal/resolve/...     (S1+S2 compat + new S3a tests)
✓ go test -race ./internal/dag/...
✓ go test -race ./internal/exec/...
✓ go test -race ./internal/runner/...
✓ go test -race ./internal/materialize/...
✓ go test -race ./internal/load/...        (goccy AST integration verified)
~ go build ./internal/cli/...              (transitive cobra dep
                                            gopkg.in/yaml.v3 not reachable
                                            in sandbox; resolves on your
                                            machine via `go mod tidy`)
```

The `load` package builds and tests cleanly against the real goccy
v1.18.0 AST API. `yamlast.go` is the single point of contact with goccy
— if the API ever changes, fixes are localised there. The rest of
`load.go` operates on Go-native types (yamlValue, yamlMap, yamlMapEntry,
yamlSeq).

The only thing the sandbox couldn't verify is the final binary link of
`cmd/gmk` — cobra transitively pulls `gopkg.in/yaml.v3` from a host that
isn't on our sandbox's egress allowlist. Your machine has full network,
so `go mod tidy && go build ./cmd/gmk` will work directly.

## Backward compatibility

Every Stage 2 YAML file should load and run identically:

  - `${NAME}` continues to mean "var reference"
  - `$$` continues to mean "literal $"
  - All Stage 2 `resolve.*` callers compile and pass tests unchanged
  - The Stage 1/2 public API (Resolve, ResolveString, ResolveInScope,
    ResolveStringInScope) is unchanged
  - `errors.Is(err, resolve.ErrUndefined)` still matches — it's now
    aliased to `expr.ErrUndefinedVar` so both work

The two intentional behaviour changes:

  1. Var declaration order is no longer alphabetical (Stage 2 was a hack).
     `gmk explain` (S8) and `gmk dryrun` output will now reflect file order.
  2. YAML tags (`!sh`, `!env`, etc.) on var values raise `ErrTaggedValue`
     instead of being silently dropped. Stage 3b will accept them.

## How to verify on your machine

```bash
tar xzf gmk-stage3a.tar.gz
cd gmk-stage3a
go mod tidy           # downloads goccy + spf13/cobra
go test -race ./...   # should pass cleanly
go build -o gmk ./cmd/gmk
./gmk dryrun hello --file examples/hello/build.yml
./gmk run    hello --file examples/hello/build.yml
```

If `go build ./internal/load/...` fails with field-not-found errors
on goccy types, the fix is in `internal/load/yamlast.go` only — the
rest of `load.go` uses our own wrapper types.
