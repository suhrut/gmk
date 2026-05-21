# gmk

> *A build orchestrator that explains itself.*
> Part of the [suhrut](https://github.com/suhrut) project family.

**Status: very early. Working on the walking skeleton.**

gmk is a declarative task orchestrator with environment binding —
designed for projects that span multiple languages, build tools, 
and deploy targets, where the current options (make, gradle, cmake, 
just) each cover only part of the workflow.

## What's different

- **Five primitives** (vars, targets, conditions, includes, probes) 
  compose into anything — no plugin sprawl
- **Materialized scripts on disk** — `bash .gmk-cache/code/build.sh` 
  reproduces any run exactly
- **autoconf-style configure phase** — `gmk configure` probes the 
  environment once, builds for years on the cache
- **`gmk explain <target>`** — answers "why did this run / not run / 
  resolve to that value" without log archaeology
- **No hidden magic** — closed schema, no convention activation, 
  every action is one command from being understood

## Status

| Feature | Status |
|---|---|
| YAML parsing + IR | 🚧 in progress |
| Materialized scripts | ⏳ planned |
| Probes / configure | ⏳ planned |
| Cache modes (SQLite) | ⏳ planned |
| Conditional logic | ⏳ planned |
| Manifest | ⏳ planned |
| Plugin protocol | ⏳ v2 |

## Quick example

[the simplest example from your stdlib/examples]

## Design

See [docs/spec.md](docs/spec.md).