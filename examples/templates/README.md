# gmk template examples

Five examples covering the template feature surface. Each is self-
contained — `cd` into it and `gmk run <target>`. They progress from
trivial to richer.

| # | Directory                    | Teaches                                                   |
|---|------------------------------|-----------------------------------------------------------|
| 1 | `01-inline-header/`          | Simplest case: inline `body:`, named args                 |
| 2 | `02-dockerfile-multi-engine/`| `file:` form; `engine:` selection; jinja vs go side-by-side |
| 3 | `03-k8s-deployment/`         | Nested maps; loops over `.items()`; conditional blocks    |
| 4 | `04-nginx-config/`           | Lists of maps; filters (`\| default(N)`); branched logic   |
| 5 | `05-splat-and-defaults/`     | `...mapvar` splat; override patterns; multi-splat merging |

## How to read these

Each example is one project: a `gmk.yml` and (where used) a
`templates/` subdir holding the template files. The `gmk.yml` has a
comment block at the top explaining what's being shown and what to
run.

The header comments in each `gmk.yml` are intentionally verbose so
you can learn the feature without reading any Go source. If something
isn't covered there, it's covered in `docs/LAYOUT.md`.

## The shape of the feature

A template is named text plus an engine. It takes named args (or
splatted map args) and produces a rendered string. Targets and
functions invoke templates via `${render:name(args)}` in their
expressions; the result flows through the same expression machinery
as any other Value.

That's the entire feature. Engines (currently `jinja` and `go`) do
the actual rendering — gmk is just the glue. If your template needs
something gmk doesn't expose, add it on the engine side: gonja
custom filters/tests for jinja, FuncMap for Go text/template.

## Running everything

```bash
# From the gmk repo root, in any of these directories:
cd examples/templates/01-inline-header
gmk list --targets
gmk run emit-header
cat /tmp/header.txt
```

The default engine is `jinja` (set in `cmd/gmk/main.go`); templates
that want Go's `text/template` declare `engine: go` explicitly.
