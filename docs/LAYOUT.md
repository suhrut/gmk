# Project Layout

A **gmk project** is a directory tree with a single canonical
`gmk.yml` at its root. Every `gmk` command — `run`, `call`, `dryrun`,
`list`, `doc` — finds that file by walking up from the current working
directory and refuses to operate if none is found.

This document describes the layout convention, the rationale, and what
each piece is for. The convention is one of those rules that becomes
invisible once you internalize it; the goal here is to make the first
encounter quick.

## The shape

```
myproject/
├── gmk.yml                # canonical entry point — exactly one per project
├── gmk/                   # split files, included from gmk.yml (optional)
│   ├── ca.yml
│   ├── registry.yml
│   ├── frontend.yml
│   ├── backend.yml
│   └── k8s.yml
├── .gmk-cache/            # tool-private state (created automatically)
│   ├── gmk.db                          # SQLite: run history, day counters
│   ├── lib.sh                          # bash helper library (auto-sourced)
│   ├── bodies/<sha256>/body.<ext>      # content-addressed scripts
│   └── runs/<YYYYMMDD>/<seq>-<name>/   # per-call scratch dirs
├── frontend/              # source: your Dockerfiles, code, configs
├── backend/
└── ...
```

The top-level `gmk.yml` is usually small — declarations of project-wide
variables plus an `includes:` list pulling in the split files:

```yaml
includes:
  - gmk/ca.yml
  - gmk/registry.yml
  - gmk/frontend.yml
  - gmk/backend.yml
  - gmk/k8s.yml

vars:
  project_name: "fullstack-demo"
  registry:     "reg.local:5000"
```

## Why this shape

### Why `gmk.yml` at the root, not somewhere else

Build configuration is a property of the *project*, not of a
subdirectory. Every modern project tool puts its manifest at the root:
`Cargo.toml`, `go.mod`, `package.json`, `pyproject.toml`. gmk follows
the same convention so the project-discovery rules and mental model
match what you already know.

Consequences worth knowing:

- **One SQLite database per project.** Every `gmk <cmd>` invocation
  from anywhere in the tree opens the same `.gmk-cache/gmk.db`. One
  audit trail, one place to look for "what ran when".

- **Docker build contexts are predictable.** A `docker build -f
  frontend/Dockerfile` invocation always runs from the project root,
  so `COPY shared/types ./` in a Dockerfile works because the context
  is the whole project, not a subdirectory.

- **No "relative to which YAML file?" ambiguity.** Every relative path
  in any included `gmk/*.yml` resolves against one known root.

- **Tab completion works from anywhere.** `gmk run <TAB>` from
  `services/auth/src/` shows all targets in the whole project,
  because the walk-up finds the same `gmk.yml` you'd find from the
  root.

### Why `gmk/` (visible directory), not `.gmk/` (hidden)

Build configuration is part of the project — visible like `Makefile`,
`package.json`, the `src/` directory. Hidden directories are for
tool-private state, which `.gmk-cache/` already covers. The split
matches Cargo: visible `Cargo.toml` and `src/`, hidden `target/`.

### Why exactly one `gmk.yml`

Multiple `gmk.yml` files in a tree would force complex segregation
rules: which `.gmk-cache/` does each one use? How do runs invoked
from the outer project address callables in the inner one? Workspace
management is its own design problem (cargo workspaces, npm
workspaces, go work) that we deliberately defer until there's
concrete demand and concrete examples to design against.

The convention is: **one `gmk.yml` per project, at the top.** If you
need a sub-component, put its YAML in `gmk/that-component.yml` and
include it from the top-level. If you need a fully independent
project, give it its own tree.

The walk-up algorithm doesn't enforce this — it just takes the first
`gmk.yml` it finds. If you accidentally nest, the inner one wins by
default. That's a documentation convention, not a runtime check.

## Discovery rules

When you run `gmk <command>` without `-f`:

1. Start at `$PWD`.
2. Look for `gmk.yml` in this directory. If present (and a regular
   file, not a directory), use it.
3. If not, move to the parent directory and try again.
4. If the walk reaches the filesystem root with no `gmk.yml` found,
   error out: *"not a gmk project (no gmk.yml found from <cwd> up
   to /)"*.

Symlinks are followed: a `gmk.yml` symlink to a real file elsewhere
counts as a project marker (useful for shared-configuration setups).

Pass `-f path/to/some.yml` to skip discovery entirely. This is the
escape hatch for CI scripts pointing at specific files, test
fixtures, and any case where you know exactly what you want loaded.

## What lives in `.gmk-cache/`

Everything gmk creates while operating. Created next to the discovered
`gmk.yml`, never in CWD if they differ. Safe to delete entirely: the
next `gmk` invocation will recreate what it needs. Should be added to
`.gitignore`:

```
# in your project's .gitignore
.gmk-cache/
```

Contents (current as of Stage 3c):

| Path                            | Purpose                                            |
|---------------------------------|----------------------------------------------------|
| `gmk.db`                        | SQLite — day-counters and per-run metadata         |
| `gmk.db-wal`, `gmk.db-shm`      | SQLite WAL sidecars (don't touch)                  |
| `lib.sh`                        | Shell helper library auto-sourced by bash bodies   |
| `bodies/<sha256>/body.<ext>`    | Content-addressed materialized scripts             |
| `runs/<YYYYMMDD>/<seq>-<name>/` | Per-call scratch dir (args, prelude, result, logs) |

Run-id format is `YYYYMMDD/<seq>`, allocated atomically via SQLite so
concurrent invocations never collide on the same id.

## Sub-projects and local includes

A sub-component within a project may have its own local YAML files
alongside its source. By convention these live next to the code and
are pulled in transitively via the top-level `gmk.yml`:

```
myproject/
├── gmk.yml                        # includes: [services/auth/component.yml]
└── services/
    └── auth/
        ├── component.yml          # local to this component
        ├── Dockerfile
        └── src/
```

The top-level `gmk.yml` is always the canonical entry point. There is
no `services/auth/gmk.yml` — that would mark `services/auth/` as its
own project (an inner one would shadow the outer for walk-up
discovery), which is exactly what the one-`gmk.yml`-per-project rule
forbids.

## What changed in Stage 3c

Before Stage 3c the canonical filename was `build.yml`. The rename
to `gmk.yml` reflects that gmk has outgrown pure build orchestration
into general-purpose polyglot task running, and follows the
tool-name-matches-manifest-name convention used by every modern
project tool. There is no fallback to `build.yml`; rename existing
files and update any tooling that referred to them.

## Templates (Stage 3c)

A gmk project may declare reusable text-rendering templates under
the top-level `templates:` block. Each template carries a body or
file reference, optional engine, and optional doc string:

```yaml
templates:
  # Inline template — short snippets where the body is clearer next
  # to the declaration.
  license-header:
    engine: jinja
    body: |
      // Copyright {{ year }} {{ owner }}
      // SPDX-License-Identifier: Apache-2.0

  # File-reference template — real templates that benefit from editor
  # syntax highlighting and can grow without stretching the YAML.
  dockerfile:
    engine: jinja
    file: templates/dockerfile.tmpl
    doc: Renders an application Dockerfile from build args.
```

File paths are resolved relative to the YAML that declared the
template. A template included from a library YAML looks for its
file in the library's directory, not the consumer's project root —
this keeps shared template libraries self-contained.

Targets and functions invoke a template via the `${render:name(args)}`
expression operator:

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

The render runs during prelude evaluation; the body receives the
rendered string as a normal prelude var. The body never directly
calls a template — all rendering is expression-level work that
happens before the script runs.

### Template engines

Two engines ship by default:

| Name      | Source                                      | When to use                                     |
|-----------|---------------------------------------------|-------------------------------------------------|
| `jinja`   | Pure-Go port of Jinja2 (gonja v2)           | Default. Familiar from Ansible/Helm/Salt; rich logic (filters, inheritance, macros, custom filters/tests). |
| `go`      | Stdlib `text/template`                      | Zero new deps; minimal-deps builds; users who prefer Go syntax. |

Per-template `engine:` overrides the default. Omit it to use the
project's registry default (set at startup to `jinja`).

Stage 3f plugins will add the ability to register additional engines
without touching the gmk binary. The same `template.Engine` interface
applies to plugin-provided engines.

### Build tag for minimal builds

The jinja engine pulls in gonja v2 and its transitive deps. To build
gmk without the jinja engine (and without those deps), pass
`-tags nogonja` to `go build`. The resulting binary still recognizes
the `go` engine; templates declared with `engine: jinja` (or omitted
when the default is jinja) fail with a clear error pointing at the
build tag. This is rarely needed in practice but useful for
constrained environments.
