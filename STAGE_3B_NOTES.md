# Stage 3b — Functions, Prelude, Multi-Language, JSON-IPC

Stage 3b promotes gmk from a target-orchestrator into a programmable polyglot
target system. The key shift: every callable (target or function) shares
the same execution shape — receive JSON, run a body in any supported
language, return JSON.

This document describes what was added, the design decisions behind each
piece, and what's intentionally deferred.

## TL;DR — what's new

1. **Functions** — declare reusable callables under a top-level `functions:`
   block; invoke via `gmk call <name>` or from expressions via
   `${call:name(args)}`.

2. **Prelude** — a `prelude:` block on any callable evaluates expressions
   at gmk-time, before the body runs. The merged result lands at
   `$GMK_PRELUDE` as JSON.

3. **JSON-IPC contract** — every body sees three env vars: `$GMK_ARGS`,
   `$GMK_PRELUDE`, `$GMK_RESULT`. Each points to a JSON file. The body
   writes its return value to `$GMK_RESULT`. Same contract across all
   languages.

4. **Multi-language** — built-in support for bash, sh, python, ruby, node,
   perl. User-defined languages via a top-level `languages:` block.

5. **Structured values** — the expression language gained first-class
   list and map types matching JSON exactly. Dot access (`${user.name}`),
   index access (`${hosts[0]}`), and `${call:fn(args)}` are first-class
   expressions.

6. **Hierarchical logger** — every gmk-internal subsystem and every user
   atom (target, function, plugin) logs under an addressable name in the
   `gmk.<subsystem>.<part>` tree. A JSON config file enables selective
   verbosity per logger.

7. **`gmk call` and `gmk list`** — new CLI commands for invoking
   functions and inventorying what a project declares.

## Architecture overview

```
                          ┌────────────────────┐
   build.yml ──────►      │   load (3b ext)    │  parses functions:,
                          │   load_3b.go       │  prelude:, languages:
                          └────────┬───────────┘
                                   │   ir.Project with Functions, etc.
                                   ▼
       ┌────────────────────────────────────────────┐
       │ resolve + expr (3b: FieldAccess, IndexAccess,
       │                    NamedCall, MapKind)
       │
       │   ${call:fn(args)} → CallResolver
       └─────────────┬──────────────────────────────┘
                     │
                     ▼
       ┌────────────────────────┐
       │  funcs.Dispatcher       │  param binding, type coercion,
       │  (CallResolver impl)    │  defaults, recursion guard
       └─────────────┬───────────┘
                     │  Runner closure
                     ▼
       ┌────────────────────────┐
       │  materialize (3b)       │  per-run scratch dir,
       │  callable.go            │  args.json/prelude.json/result.json,
       │                         │  multi-lang script render,
       │                         │  lib.sh sourced for bash/sh
       └─────────────┬───────────┘
                     │  CallableInvocation
                     ▼
       ┌────────────────────────┐
       │  runner (3b)            │  exec.Command(interp, args...),
       │  callable.go            │  read result.json,
       │                         │  return CallableResult
       └─────────────────────────┘
```

The `Dispatcher → Runner closure → materialize → runner → result.json` flow
is the call cycle. The closure is what makes nested `${call:...}` work:
when a function's prelude or body calls another function, dispatch routes
back into the same closure rather than spawning a fresh `gmk` process.

## Design decisions

### Why JSON files, not pipes or env vars

Three reasons:
1. **Debuggable** — after a failure the scratch dir contains every file
   that was input/output. `cat` works. `jq` works. No replay required.
2. **Language-neutral** — every language ships JSON support in its stdlib.
   No protocol library to install or version.
3. **Same shape as plugin RPC** — plugins (Stage 3f) speak the same
   protocol. Functions and plugins are unified at the boundary.

The cost is one disk write per call. At gmk's expected scale (tens of
calls per build, not thousands per second) this is invisible.

### Why prelude separate from body

The prelude/body split mirrors the gmk-time / run-time split:

| Phase | Language | What runs | When |
|-------|----------|-----------|------|
| prelude | gmk expressions | declarative bindings | before body |
| body | bash/python/etc | arbitrary code | when caller invokes |

This lets a function be "configured" through expressions (which gmk can
inspect, cache, and reason about) and "implemented" through a script
(which gmk doesn't peer into). Pure-data functions skip the body entirely
and return the prelude as the result.

### Why typed parameters

Every function declares `params:` with explicit types. Calls are bound
strictly: extra args error, missing required args error, type coercion
happens at the boundary.

The cost is verbosity. The benefits:
- `gmk list -v` shows real signatures.
- Calls fail at the right time (boundary, not deep inside a body).
- Editor tooling (Stage 11 LSP) gets information to autocomplete.
- The schema is the docs.

A function that wants free-form input declares a single `data: map`
parameter and treats it as a bag — no special syntax needed.

### Why a hierarchical logger upfront

The Pareto observation: in any system, the 20% that's misbehaving is
where you want detail. The 80% that's stable should stay quiet. Tools
that flip a global `-v` drown the user.

Hierarchical names (`gmk.target.release`, `gmk.plugin.git`, etc.) plus
prefix-wildcard policies let the user say "trace for this one
function, warn for everything else" — and the design works because
every log site has already declared which atom it belongs to. Adding
this to a code base after it's grown is painful; doing it at 3b is
cheap.

## File layout (Stage 3b additions)

```
internal/
  logger/                  hierarchical slog-based logger
    logger.go              core types, Get(name), context binding
    policy.go              prefix-wildcard policy with exact-beats-wildcard
    handlers.go            policy gate per sink, terminal+file routes
    config.go              JSON config file, --log CLI flags, GMK_LOG env
    logger_test.go         18 unit tests

  expr/
    value.go               extended with MapKind, ToJSON, Index, Field
    value_json_test.go     17 unit tests for structured values
    ast.go                 + FieldAccess, IndexAccess, NamedCall
    lexer.go               + [, ], = tokens; hyphens in identifiers
    parser.go              + postfix .field/[key]; kind:name(args)
    parser_stage3b_test.go 20+ unit tests for new syntax
    eval.go                + Calls field on Evaluator, NamedCall dispatch
    eval_stage3b_test.go   18 unit tests for new eval paths
    funcs.go               + keys, values, first, last, to_json, from_json
    json_helpers.go        JSON encode/decode helpers

  ir/
    types.go               + Function, FunctionParam, FunctionResult,
                            PreludeEntry, Language; Project.Functions,
                            Project.Languages; Target.Prelude, Target.Doc

  load/
    load.go                + functions:, languages: top-level keys;
                            prelude:, script:, doc: target fields
    load_3b.go             loadFunction, loadFunctionParams,
                            loadFunctionResult, loadPrelude, loadLanguage
    load_3b_test.go        15 unit tests

  funcs/                   (new package)
    dispatch.go            Dispatcher: CallResolver impl, param binding,
                            coercion, defaults, recursion guard
    float.go               strconv shim
    dispatch_test.go       10 unit tests

  materialize/
    callable.go            MaterializeCallable: per-run scratch dir,
                            args.json/prelude.json/result.json,
                            multi-lang script render, language registry,
                            ReadResultFile
    lib_sh.go              embedded bash helper library
    callable_test.go       9 unit tests

  runner/
    callable.go            RunCallable: exec child, capture result,
                            structured logging
    callable_test.go       5 integration tests (actually fork bash)

  cli/
    call.go                gmk call <function> [k=v...] [--json ...]
    call_helpers.go        strconv shim
    list.go                gmk list [--targets|--functions|--languages|--loggers]
                            with --json output mode
    doc.go                 gmk doc <name> with --json and --shell-helpers
    schema.go              gmk schema — emits JSON Schema for build.yml
    json_util.go           shared jsonEncode helper
    cli_3b_test.go         16 integration tests for the new commands

  integration/
    call_test.go           TestExamplesCall: exercise every function
                            in every example through the full stack
    helpers_3b.go          test helpers
    examples_test.go       (updated to allow function-only examples)

examples/
  functions/build.yml      typed function with params, result, lib.sh helpers
  prelude/build.yml        prelude bindings, pure-data function, target prelude
  multi-language/build.yml same function in bash/python/node/perl
```

## Test count

```
Package                            Tests
internal/cache                       6
internal/cli                        24   (+16 new in 3b: list, doc, schema, call)
internal/dag                        17
internal/exec                       12
internal/expr                       73   (+34 new in 3b)
internal/funcs                      10   (NEW)
internal/integration                28   (+9 new TestExamplesCall subtests)
internal/ir                          6
internal/load                       55   (+15 new in 3b)
internal/logger                     18   (NEW)
internal/materialize                15   (+9 new callable tests)
internal/resolve                    18
internal/runner                     17   (+5 new callable tests)
internal/scope                      36
                                  -----
Total                              335 explicit tests + ~19 subtests
                                   = 354 passing, 1 skipped (jq required)
```

## CLI surface

After Stage 3b, `gmk` exposes nine subcommands:

```
gmk call        invoke a function and print its result
gmk completion  generate shell completion (cobra built-in: bash/zsh/fish/powershell)
gmk doc         show full doc for a function/target, or --shell-helpers for lib.sh
gmk dryrun      show what would run for a target without executing it
gmk help        help about any command
gmk list        list targets, functions, languages, and addressable logger names
gmk run         run a target (and its dependencies)
gmk schema      emit JSON Schema for build.yml (for editor LSPs)
gmk version     print version and build commit
```

Setup for editor integration:

```bash
# Shell completion — once per shell:
gmk completion bash > /etc/bash_completion.d/gmk
gmk completion zsh  > ~/.zsh/completions/_gmk

# Editor LSP — drop schema next to your build.yml:
gmk schema > .gmk.schema.json
# Then in .vscode/settings.json:
#   "yaml.schemas": { ".gmk.schema.json": ["build.yml", "*.gmk.yml"] }
```

## What's intentionally deferred

These are designed but pushed to later stages of Stage 3b or beyond:

- **`gmk graph`** — DAG visualization for dep chains. (Stage 5 territory.)
- **Parent-socket IPC** — when a body invokes `gmk call` from inside,
  route to the parent process via Unix socket. Right now nested calls
  through `${call:name}` work via the in-process dispatcher; only an
  explicit `gmk call` child process from within a body would spawn a
  fresh process. The IPC closes that gap and is a clean addition.
- **`gmk-sh`** — embedded shell with curated u-root utilities. Deferred to
  Stage 4a per the locked plan.

None of these affect the architecture; they're features layered on top.

## Try it

```bash
go build -o gmk ./cmd/gmk
cd examples/functions
./gmk list
./gmk call greet who="world"
./gmk call greet who="Bengaluru" shout=true --output pretty

cd ../multi-language
for fn in sum-bash sum-python sum-node sum-perl; do
  echo "$fn: $(./gmk call "$fn" a=10 b=20)"
done

cd ../prelude
./gmk call config --output pretty
./gmk call deploy-version env=prod
```

## Stage 3b acceptance criteria — status

- [x] Logger infrastructure with hierarchical names, JSON config, per-sink policy
- [x] Expression parser: `.field`, `[key]`, `call:fn(args)`
- [x] Value lattice: list and map, JSON bridges
- [x] 6 new builtins: keys, values, first, last, to_json, from_json
- [x] IR: Function, FunctionParam, Prelude, Language
- [x] Loader: functions:, languages:, prelude:, script:, doc:
- [x] Function dispatcher with binding, coercion, recursion guard
- [x] Per-run scratch dir with JSON IPC
- [x] Multi-language registry (bash/sh/python/ruby/node/perl)
- [x] lib.sh bash helpers
- [x] `gmk call` CLI
- [x] `gmk list` CLI
- [x] `gmk doc <name>` CLI (with --json and --shell-helpers)
- [x] `gmk schema` CLI (JSON Schema for editor LSPs)
- [x] Shell completion (cobra built-in: bash/zsh/fish/powershell)
- [x] Examples for functions, prelude, multi-language
- [x] Integration tests exercising the whole stack
- [ ] `gmk graph`, parent-socket IPC for child `gmk call`
- [ ] gmk-sh (deferred to Stage 4a)
- [ ] Plugin-protocol stub→real (Stage 3f)
- [ ] Templates (Stage 3c), YAML tags (Stage 3d), secrets (Stage 3e)

Stage 3b is functionally complete for its core architectural goals AND
the Tier-1 UX commands; the remaining checkboxes are convenience
features that don't change the shape of anything.
