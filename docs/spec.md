# gmk Specification

**Version**: 0.1 (Draft)
**Status**: Design document
**Audience**: Implementers, contributors, early adopters

---

## 1. Overview

gmk is a **declarative task orchestrator with environment binding**. It provides a YAML-based model for describing builds, deploys, probes, and operational tasks across multiple languages, with materialized scripts on disk, SQLite-backed caching, structured manifest output, and explicit semantics throughout.

It is not a build system in the traditional sense — it does not own compilation. It is an orchestrator that delegates compilation and language-specific work to native tools (cmake, gradle, cargo, go build, etc.) while owning the surrounding concerns: environment composition, conditional execution, dependency tracking, probe-based environment detection, and reproducible script execution.

The conceptual model is small: **five primitives** (vars, scripts, conditionals, includes, probes) compose into arbitrary build/deploy/ops workflows. Domain knowledge lives in user-included YAML libraries, not in the tool binary.

---

## 2. Goals & Non-Goals

### Goals

- **Closed conceptual surface**: five primitives, fully discoverable via `gmk` subcommands
- **Closed schema**: unknown YAML keys are parse-time errors
- **Materialized scripts on disk**: every executable is a readable, runnable file
- **Provenance manifest**: classification-aware build provenance for every release
- **Multi-language scripts**: bash, sh, python, perl, ruby, node, pwsh, cmd
- **Fast tab-completion**: <30ms via structural-only IR queries
- **Single-binary distribution**: no daemon required for the tool itself
- **Explicit semantics**: no implicit activation, no convention magic, no hidden state
- **Autoconf-style probe phase**: `configure` separated from `run`
- **One consistent interface across domains**: via composable YAML libraries
- **Language-neutral plugin contract**: map→map JSON over stdin/stdout or RPC

### Non-Goals

- Replace Android Gradle Plugin or Spring Boot Maven Plugin
- Reimplement Maven Central / Gradle Module Metadata resolution
- Provide IDE integrations (delegated; ship JSON Schema for editor support)
- Become a SOAR / event-driven runtime
- Become a CI/CD service
- Own language-specific compilation (delegated to specialist tools or daemons)
- Provide install/export config files for downstream CMake consumers

---

## 3. Core Concepts

| Primitive | Purpose | YAML key / construct |
|---|---|---|
| **Vars** | Scoped named values, eager or lazy | `vars:` block |
| **Targets** | Named runnable units with deps and scripts | `targets:` block |
| **Conditions** | Boolean expressions gating execution | `when:`, `all:`, `any:`, `none:` |
| **Includes** | Library composition | `includes:` list |
| **Probes** | Environment detection with caching | `!probe:*` tags |

Auxiliary concepts:

- **Scope**: the lexical context (directory, section) within which vars are visible; vars inherit from enclosing scope and may be overridden locally.
- **Cache mode**: how often a lazy value re-evaluates (`parse`, `configure`, `ttl:N`, `per_run`, `per_read`).
- **Producer**: a unified term for "thing that runs and produces a value or side effect" — both targets and value-producing vars are producers.
- **IR**: the parsed, resolved intermediate representation; cached as MessagePack.
- **Manifest**: the build provenance document — frozen IR state + probe results + source hashes + run metadata.

---

## 4. File Layout

### Project tree

```
my-project/
├── build.yml                  # entry point
├── vars/
│   └── common.yml             # locally-included shared vars
├── deploy/
│   └── k8s.yml                # deploy targets
└── vendor/                    # vendored library YAMLs (optional)
    └── probes/
        └── jvm.yml
```

### Cache tree

```
.gmk-cache/
├── ir.msgpack                 # serialized IR, mtime-validated
├── manifest.json              # source path → ir_hash mapping
├── probes.db                  # SQLite, configure-mode results
├── ttl.db                     # SQLite, TTL-mode results
├── deps.db                    # SQLite, target → input edges
└── code/                      # materialized scripts
    ├── global/                # from <lib> includes
    │   └── probes/jvm.yml/HAVE_JAVA17.sh
    └── local/                 # from ./local includes
        ├── build.yml/go_build_myapp.sh
        ├── build.yml/git_sha.sh
        └── deploy/k8s.yml/deploy_myapp.sh
```

The entire tool state lives under `.gmk-cache/`. One `rm -rf` resets everything.

---

## 5. YAML Schema

### Top-level keys

| Key | Type | Purpose |
|---|---|---|
| `includes` | list | Library YAMLs to compose into this scope |
| `vars`, `vars.1`, `vars.2`, ... | mapping | Variable declarations, ordered by suffix |
| `targets` | mapping | Named runnable units |
| `meta` | mapping | Optional: project name, version, description |

Unknown top-level keys are parse-time errors.

### `includes:`

```yaml
includes:
  - "./vars/common.yml"                # local, explicit path
  - "<probes/jvm.yml>"                  # path-searched (gmk_PATH)
  - {path: "<probes/win.yml>",   when: "${OS} == 'windows'"}
  - {path: "<probes/posix.yml>", when: "${OS} != 'windows'"}
```

Search path (for `<...>` includes), in order:
1. Project-level `lib_path:` declarations
2. `$gmk_PATH` (colon-separated)
3. `~/.gmk/lib/`
4. `/usr/local/share/gmk/lib/`
5. `/usr/share/gmk/lib/`

### `vars:`

Multiple `vars:` blocks per document via numeric suffix (`vars`, `vars.1`, `vars.2`). All resolve in document order; later blocks see earlier values.

```yaml
vars:
  myapp_dir: "/work/myapp"                  # eager string
  java_home: !probe:has_tool            # tagged value
    name: java
    min_version: "17"

vars.1:
  out_dir: "${myapp_dir}/build"           # references earlier var
  git_sha: !sh                          # lazy
    cache: per_run
    run: "git rev-parse HEAD"
```

### `targets:`

```yaml
targets:
  build_myapp:
    deps: [git_sha, install_deps]       # deps may be targets or vars (unified producers)
    when: "${HAVE_GO}"                  # condition (single)
    env:                                # additional env exposed to script
      GOFLAGS: "-trimpath"
    cwd: "${myapp_dir}"                   # working directory
    lang: bash                          # script language (default: bash on unix, pwsh on win)
    cache: per_run                      # default for targets
    run: |
      go build -ldflags "-X main.sha=${git_sha}" -o ${out_dir}/myapp ./...

  deploy:
    all:                                # condition (multi, AND)
      - "${HAVE_K3S}"
      - "${PROFILE} == 'prod'"
    none:
      - "${MAINTENANCE_MODE}"
    deps: [build_myapp]
    run: |
      kubectl apply -f deploy.yml
```

Target fields:

| Field | Type | Required | Default |
|---|---|---|---|
| `deps` | list of names | no | `[]` |
| `when` / `all` / `any` / `none` | expression / list | no | always run |
| `env` | mapping | no | `{}` (inherits scope env) |
| `cwd` | string | no | project root |
| `lang` | enum | no | `bash` (unix) / `pwsh` (windows) |
| `cache` | enum | no | `per_run` |
| `phony` | bool | no | `false` |
| `output` | path or list | no | none (target has side effects only) |
| `run` | script body | yes | — |
| `brief` | format string | no | — |
| `parallel_jobs` | int | no | 1 (this target's internal parallelism) |

Targets may set `phony: true` to indicate they have no file output (always re-run when invoked).

---

## 6. Tag Reference

Tags are typed constructors for values. The tag set is **closed** — adding a tag requires a tool release. Closed surface is enforced by parse-time schema validation.

### Value-producing tags (eager unless noted)

| Tag | Purpose | Eval timing |
|---|---|---|
| `!sh` | Shell script; captured stdout = value | per_run (default) |
| `!cmd` | Alias for `!sh` | per_run |
| `!files` | Filesystem walk with includes/excludes | per_run |
| `!glob` | Single-pattern file glob | per_run |
| `!read_file` | Contents of a file | per_run |
| `!sha256_file` | Hash of a file | per_run |
| `!join` | List → string with separator | eager |
| `!format` | Template substitution | eager |
| `!now` | Current timestamp | per_read |
| `!random` | Random value | per_read |

### Reference tags

| Tag | Purpose |
|---|---|
| `!env` | Environment variable lookup |
| `!ctx` | Context variable (passed at invocation via `--ctx K=V`) |
| `!lazy` | Force lazy evaluation on an otherwise-eager value |

### Probe tags (cache mode default: `configure`)

| Tag | Purpose |
|---|---|
| `!probe:has_tool` | Tool exists on PATH, optional version check |
| `!probe:has_lib` | Library available (via pkg-config or path search) |
| `!probe:has_header` | C/C++ header exists in include paths |
| `!probe:compiles` | Tiny test program compiles & links |
| `!probe:port_free` | TCP/UDP port is unbound |
| `!probe:host_reachable` | Host responds to TCP/ICMP |
| `!probe:file_exists` | File exists |
| `!probe:os` | Current OS (`linux`, `darwin`, `windows`) |
| `!probe:arch` | Current architecture |
| `!probe:cpu_count` | Logical CPU count |
| `!probe:goss` | Wrapper around Goss probe spec file |
| `!probe:sh` | Custom shell-based probe with `{ok, detail}` return |

### Logical/structural tags

| Tag | Purpose |
|---|---|
| `!switch` | Multi-case conditional value |
| `!pin` | Force a normally-lazy value to be evaluated once at parse |
| `!fresh` | Force a normally-cached value to re-evaluate per read |
| `!plugin` | Map→map plugin invocation |
| `!daemon` | Persistent worker process declaration |
| `!daemon_call` | RPC to a daemon |

### Tag argument shape

Most tags accept either a string (short form, where unambiguous) or a mapping (full form):

```yaml
short:  !sh "git rev-parse HEAD"
full:   !sh
  cache:    per_run
  shell:    bash
  parse:    string
  trim:     true
  timeout_s: 30
  run: |
    git rev-parse HEAD
```

---

## 7. Expression Language

Expressions appear inside `${...}` within strings, in `when:` clauses, and in `!switch` cases. Same grammar everywhere.

### Reference forms

| Syntax | Meaning |
|---|---|
| `${VAR}` | Bare var reference (current scope chain) |
| `${kind:NAME}` | Typed reference (`env`, `ctx`, `lazy`, `cmd`) |
| `${VAR:-default}` | Default if VAR unset/empty |
| `${VAR:?error msg}` | Required; error if unset |
| `${VAR%suffix}` / `${VAR#prefix}` | Strip trailing/leading match |
| `${VAR/old/new}` | Single string replacement |
| `${func(arg1, arg2)}` | Function call |
| `${A \| func \| func2(x)}` | Pipeline (left value is first arg of right function) |

### Operators

| Operator | Precedence | Meaning |
|---|---|---|
| `( )` | highest | grouping |
| `\|` | high | pipeline |
| `== != < <= > >=` | medium | comparison |
| `!` | medium | unary not |
| `&&` | low | short-circuit AND |
| `\|\|` | low | short-circuit OR |

### Built-in functions (closed set)

**String**: `len`, `lower`, `upper`, `trim`, `trim_left`, `trim_right`, `substr`, `starts_with`, `ends_with`, `contains`, `split`, `join`, `replace`, `regex`, `regex_extract`, `regex_replace`

**Numeric**: `to_int`, `to_float`, `to_string`, `min`, `max`, `clamp`

**List**: `len` (overloaded), `first`, `last`, `nth`, `concat`, `unique`, `sorted`

**Path**: `dirname`, `basename`, `extname`, `path_join`, `path_abs`, `is_abs`

**Boolean**: `ok`, `not`, `and`, `or`, `equal`

**Version-aware**: `version_gte`, `version_lt`

**Discovery**: `gmk funcs [--search PATTERN] [<name>]` lists functions with signatures and examples.

### Examples

```yaml
# Nested function call
k8s_minor: "${to_int(regex_extract(${K8S_VERSION}, '^v1\\.(\\d+)', 1))}"

# Same, piped (preferred for readability)
k8s_minor: "${K8S_VERSION | regex_extract('^v1\\.(\\d+)', 1) | to_int}"

# In a switch case
worker_count: !switch
  - {when: "${gpu_count | to_int >= 4}", value: 16}
  - {when: "${gpu_count | to_int >= 1}", value: 4}
  - {default: true,                      value: 1}
```

---

## 8. Conditional Logic

Three sites use the same grammar:
1. Target gating (`when:` / `all:` / `any:` / `none:` on a target)
2. Switch case discriminators (within `!switch`)
3. Include conditionals (`when:` on entries in `includes:`)

### Forms

| Form | Semantics |
|---|---|
| `when: "EXPR"` | Single expression |
| `all: [EXPR, EXPR, ...]` | All must be true (implicit AND) |
| `any: [EXPR, EXPR, ...]` | At least one true (implicit OR) |
| `none: [EXPR, EXPR, ...]` | None true (implicit `!(A || B || ...)`) |
| Combined `all:` + `none:` | All positive must hold AND none of exclusions must hold |
| Nested: `all:` containing `any:` containing `all:` etc. | Arbitrary depth |

A target may use **one** of these forms; combining `when:` with `all:`/`any:`/`none:` at the same level is a parse error.

### Example

```yaml
targets:
  build_gpu:
    all:
      - "${HAVE_GPU}"
      - "${gpu_count | to_int >= 1}"
    none:
      - "${IS_WSL}"
      - "${IN_DOCKER_LIMITED}"
    run: |
      ...

  java_home: !switch
    - {when: "${OS} == 'linux'",  value: /usr/lib/jvm/java-17}
    - {when: "${OS} == 'darwin'", value: /Library/Java/.../Home}
    - {default: true,             value: /opt/java}
```

### Complex logic

For depth > 3, hoist intermediate booleans to named vars:

```yaml
vars:
  is_native_linux: "${OS} == 'linux' && !${IS_WSL} && !${IN_DOCKER}"
  is_gpu_capable:  "${HAVE_GPU} && ${gpu_count | to_int >= 1}"

targets:
  build_full:
    all: ["${is_native_linux}", "${is_gpu_capable}"]
    run: ...
```

`gmk explain <target>` shows every sub-condition's resolution regardless of authoring style.

---

## 9. Cache Modes & Evaluation Timing

Five cache modes determine when a value is evaluated and how long the result persists:

| Mode | When evaluated | When invalidated | Storage | Typical use |
|---|---|---|---|---|
| `parse` | At YAML parse / IR build | Source mtime | In IR cache | Eager string ops |
| `configure` | At `gmk configure` | Explicit `--refresh` | `probes.db` | Slow env probes |
| `ttl:DURATION` | First use, then cached | TTL expiry or `--refresh` | `ttl.db` | Slow-drifting values |
| `per_run` | First use this invocation | End of process | Memory | Build-time queries |
| `per_read` | Every reference | N/A | None | `!now`, `!random` |

### Default cache modes by tag

| Tag | Default |
|---|---|
| `!sh`, `!cmd`, `!files`, `!read_file` | `per_run` |
| `!probe:*` | `configure` |
| `!now`, `!random` | `per_read` |
| `!join`, `!format`, eager strings | `parse` |

User may override per declaration:

```yaml
disk_free_gb: !sh
  cache: ttl:24h
  run: df -BG / | tail -1 | awk '{print $4}' | tr -d G
```

### Substitution at script generation

When materializing a script:
- Refs whose values are known at generation time (`parse`, `configure`, hot-`ttl`) → **substituted literally** into script body
- Refs whose values are per-invocation (`per_run`, `per_read`, `ctx`) → **emitted as env-var references**; gmk evaluates and exports before invoking script

The script file is invariant as long as YAML and parse/configure-time bindings haven't changed. Lazy values flow through env, not regeneration.

---

## 10. Materialized Scripts

Every `!sh`/`!cmd`/target script body is materialized to a real file under `.gmk-cache/code/`.

### Path convention

```
.gmk-cache/code/<scope>/<relative_source_yml>/<var_or_target_name>.<ext>
```

- `<scope>`: `global` for `<lib>` includes, `local` for `./` includes
- `<relative_source_yml>`: directory mirror of YAML source path
- `<ext>`: per `lang:` (`.sh`, `.py`, `.pl`, `.ps1`, `.rb`, `.mjs`, etc.)

### Standard header (bash example)

```bash
#!/usr/bin/env bash
# ════════════════════════════════════════════════════════════════
# Generated by gmk
# Source:    build.yml:42  (target: go_build_myapp)
# Scope:     /root/myapp
# IR hash:   sha256:a3f9c8...
# DO NOT EDIT — regenerated on source change
# ════════════════════════════════════════════════════════════════

set -eo pipefail
[ "${gmk_V:-0}" -ge 1 ] && set -x
[ "${gmk_V:-0}" -ge 2 ] && export PS4='+ [${BASH_SOURCE}:${LINENO}] '

trap 'rc=$?; printf "gmk_ERR target=%s script=%s line=%d exit=%d\n" \
      "go_build_myapp" "$0" "$LINENO" "$rc" >&2; exit $rc' ERR

# Resolved scope env (eager values, baked at generation)
export GOFLAGS="-trimpath"
cd "/work/myapp"

# Lazy env populated by gmk at invocation:
#   GIT_SHA     ← !sh per_run   (code/local/build.yml/git_sha.sh)
#   BUILD_TIME  ← !sh per_read  (code/local/build.yml/build_time.sh)

# ──────────── user script body ────────────
go build -ldflags "-X main.sha=${GIT_SHA}" -o build/myapp ./cmd/myapp
# ──────────── end user script body ────────
```

### Per-language defaults

| `lang:` | Extension | Header conventions |
|---|---|---|
| `bash` | `.sh` | `set -eo pipefail`, `set -x` at V=1, `trap … ERR` |
| `sh` | `.sh` | `set -e`, no traps |
| `python` | `.py` | `sys.excepthook` for error wrap, `gmk_V` exposed as `V` |
| `perl` | `.pl` | `use strict; use warnings; $SIG{__DIE__}` |
| `ruby` | `.rb` | Equivalent error trap |
| `node` | `.mjs` | `process.on('uncaughtException')` |
| `pwsh` | `.ps1` | `$ErrorActionPreference='Stop'`, `Set-PSDebug -Trace 1` at V=1 |
| `cmd` | `.cmd` | Native error handling |

### Properties

- **Standalone executable**: `bash .gmk-cache/code/local/build.yml/go_build_myapp.sh` reproduces the exact run
- **Lintable**: `gmk lint` dispatches to shellcheck/ruff/PSScriptAnalyzer/etc.
- **Diffable**: deterministic generation (no timestamps in body); byte-identical across runs with same input
- **Secret-safe**: vars classified `secret`/`pii` reference env vars; values never baked into the file

---

## 11. Probes & Configure Phase

Probes are environment-detection operations cached aggressively. The `gmk configure` command runs all `cache: configure` probes; subsequent `gmk run` invocations consume the cached results.

### Standard probe library

Ships as YAML libraries on the path:

```
<probes/system.yml>     # os, arch, cpu_count, mem_total_gb, kernel_version
<probes/c.yml>          # has_cc, cc_version, has_header, has_function, sizeof_*, compiles
<probes/cpp.yml>        # has_cxx, cxx_std_17, has_lib_*
<probes/jvm.yml>        # has_java, java_version, has_jdk_17, gradle_available
<probes/python.yml>     # has_python3, python_version, has_module_*
<probes/go.yml>         # has_go, go_version, has_cgo
<probes/rust.yml>       # has_cargo, has_rustc, target_supported_*
<probes/node.yml>       # has_node, node_version, has_npm, has_pnpm
<probes/docker.yml>     # has_docker, docker_version, buildkit_available
<probes/k8s.yml>        # has_kubectl, k8s_version, cluster_reachable, has_namespace_*
<probes/cuda.yml>       # has_nvidia_smi, cuda_version, gpu_count, gpu_memory_mb
<probes/network.yml>    # port_free, host_reachable, dns_resolves, https_ok
<probes/find_*.yml>     # find_openssl, find_zlib, find_libxml2, ... (cmake find_package equivalents)
```

### Probe result shape

```yaml
HAVE_JAVA17: !probe:has_tool
  name: java
  min_version: "17"
  remediation: "Install via: mise install java@17"
```

After `gmk configure`, the value `HAVE_JAVA17` is a bool; the probe result is recorded in `probes.db` with:
- Resolved value
- Source location of the probe declaration
- Timestamp of probing
- Captured stdout/stderr of the probe script
- Remediation text (used on subsequent failure displays)

### Configure command output

```
$ gmk configure
checking for java >= 17...        yes (openjdk-17.0.9)
checking for nvidia-smi...        no
checking docker daemon...         yes
checking kubectl cluster...       yes (cluster: prod-east)
checking openssl >= 3.0...        yes (3.0.11)
checking port 5432 free...        no (in use by pid 4421 postgres)

5 of 6 required probes passed, 1 failed.
  ✗ nvidia-smi — required by target gpu_test
    → https://docs.nvidia.com/datacenter/tesla/tesla-installation-notes/
```

### Refresh

```bash
gmk configure --refresh                # all probes
gmk configure --refresh HAVE_JAVA17    # single probe
```

---

## 12. Manifest (Build Provenance)

Each release build optionally produces a structured manifest capturing the complete configuration that produced the artifact. JSON canonical; YAML as `--format=yaml` view.

### Schema (top-level)

```json
{
  "schema_version": "1",
  "tool":           { "name": "gmk", "version": "0.3.1", "ir_hash": "..." },
  "generated_at":   "2026-05-21T14:23:11Z",
  "git":            { "commit": "...", "branch": "...", "tag": "...", "dirty": false },
  "sources":        [ {"path": "...", "sha256": "..."}, ... ],
  "probes":         { "JAVA_VERSION": {...}, "K8S_VERSION": {...} },
  "resolved_vars":  { "myapp_dir": {...}, "jwt": {"value": "<REDACTED:secret>"} },
  "target":         { "name": "...", "scripts": [...], "exit_code": 0, "duration_s": 252.4 },
  "environment":    { "os": "...", "arch": "...", "user": "<REDACTED:pii>" }
}
```

### Classification & redaction

Vars carry an optional `class:` field. Manifest emission replaces values with `<REDACTED:<class>>`:

| Class | Behavior |
|---|---|
| `secret` | Value replaced; name retained |
| `pii` | Value replaced; name retained |
| `pci`, `phi` | Value replaced; name retained |
| (default) | Value emitted as-is |

`--include-secrets` requires `gmk_DUMP_SECRETS=1` env var as belt-and-suspenders.

### Diff

```
$ gmk manifest diff v1.2.3.json v1.2.4.json
CHANGED probes:
  K8S_VERSION:  v1.29.4 → v1.30.1
CHANGED resolved_vars:
  go_version:   1.22.3 → 1.22.7
UNCHANGED: 47 vars, 12 probes, 8 sources
```

### Standard library target

```yaml
includes: ["<targets/manifest.yml>"]

targets:
  release:
    deps: [build_artifact, manifest]
    run: tar czf release.tar.gz dist/ build-manifest.json.gz
```

---

## 13. Commands (CLI Reference)

| Command | Purpose |
|---|---|
| `gmk run <target>` | Execute a target |
| `gmk configure [--refresh]` | Evaluate all `cache: configure` probes |
| `gmk explain <target>` | Show resolved env, conditions, deps, scripts for a target |
| `gmk env <target> [--eager-only]` | Print effective env (use `--export` for sourceable) |
| `gmk reproduce <target>` | Generate one-shot reproducer script (re-evals lazies on each run) |
| `gmk script <target> [--cat \| --edit \| --reveal]` | Path/contents/editor for the materialized script |
| `gmk manifest [--format=json\|yaml\|spdx\|cyclonedx] [--redact-secrets]` | Emit build manifest |
| `gmk manifest diff <a> <b>` | Diff two manifests |
| `gmk deps why <target>` | Show why a target needs rebuild |
| `gmk deps tree <target>` | Recursive dep tree |
| `gmk deps reverse <file>` | Targets that depend on a file |
| `gmk lint [<target>]` | Lint materialized scripts via per-lang linters |
| `gmk cache clean` | Wipe `.gmk-cache/` entirely |
| `gmk cache prune [--dry-run] [--older-than N]` | Remove orphan/stale cache entries |
| `gmk daemon {start\|stop\|restart\|status} [<name>]` | Manage persistent daemons |
| `gmk funcs [--search PATTERN] [<name>]` | List/describe built-in functions |
| `gmk tags` | List available tags with descriptions |
| `gmk probes` | List configured probes and their cached values |
| `gmk fmt [--conditions=expression\|nested\|named]` | Format YAML files |

### Verbosity

| Flag | Equivalent env | Behavior |
|---|---|---|
| (none) | `V=0` | Brief output; errors at full detail |
| `-v` | `V=1` | Show each command (`set -x`); brief tool actions |
| `-vv` | `V=2` | + env vars, cwd, durations, cache events |
| `-vvv` | `V=3` | + IR resolution traces, scope lookups |

Errors always print at full verbosity regardless of `V`.

---

## 14. Plugin Contract

Plugins are language-neutral extension points. Contract:

- **Input**: JSON map on stdin (one-shot) or Unix-socket request (daemon)
- **Output**: JSON map on stdout (one-shot) or socket reply (daemon)
- **Schema**: `version` field declares which input/output shape; tool validates
- **Sandboxing**: plugin sees only what input map provides paths to
- **Exit**: 0 = success; non-zero = failure; stderr captured for `explain`

### One-shot plugin invocation

```yaml
targets:
  build_image:
    run: !plugin
      name:    docker_image_build
      version: "1"
      input:
        dockerfile: Dockerfile
        context:    .
        tags:       ["${image}:${version}"]
      capture_outputs:
        image_id:     "BUILT_IMAGE_ID"
        image_digest: "BUILT_IMAGE_DIGEST"
```

### Daemon plugin invocation

```yaml
includes: ["<daemons/java.yml>"]    # defines `java_daemon` !daemon spec

targets:
  compile:
    deps: [java_daemon]
    run: !daemon_call
      daemon: java_daemon
      method: incremental_compile
      args:
        sources:        "${src_files}"
        classpath:      "${classpath}"
        report_deps_to: "${gmk_DEPS_DB}"
```

The daemon's `/incremental_compile` reports back a list of compiled files and their dep edges; tool ingests into `deps.db`.

### Plugin discovery

- Plugins ship as single binaries (or scripts with shebang) on `PATH` or via `<plugin/...>` YAML libraries that declare them.
- `gmk plugins list` enumerates available plugins with version + brief.
- Awesome-list convention initially; structured registry only if ecosystem warrants.

---

## 15. Standard Library Overview

Categories shipped with the tool:

| Category | Path | Purpose |
|---|---|---|
| Probes (system) | `<probes/system.yml>` | OS, arch, CPU, memory |
| Probes (lang) | `<probes/{c,cpp,jvm,python,go,rust,node}.yml>` | Toolchain detection |
| Probes (find) | `<probes/find_*.yml>` | Library detection (cmake find_package equivalents) |
| Probes (infra) | `<probes/{docker,k8s,cuda,network}.yml>` | Infrastructure detection |
| Rules (lang) | `<rules/{c,go,rust,jvm}-build.yml>` | Per-language build target patterns |
| Rules (cmake) | `<rules/cmake.yml>` | CMake invocation wrappers |
| Rules (gradle) | `<rules/gradle.yml>` | Gradle daemon wrapper + Android conveniences |
| Daemons | `<daemons/{java,python,node,clang}.yml>` | Persistent worker declarations |
| Release | `<release/{c,android,embedded,rust,go,jvm}.yml>` | Per-domain manifest extensions |
| Targets | `<targets/{manifest,lint,clean}.yml>` | Common reusable targets |

Standard library coverage target for v1: top ~50 most-used domain libraries.

---

## 16. Examples

### Minimal C/C++ project

```yaml
includes:
  - "<probes/c.yml>"
  - "<probes/find_openssl.yml>"

vars:
  src:     !files {base: src, include: ["**/*.c"]}
  cflags:  "-O2 -g -Wall ${OPENSSL_CFLAGS}"

targets:
  build:
    when: "${HAVE_CC} && ${OPENSSL_FOUND}"
    run: |
      mkdir -p build
      ${CC} ${cflags} ${src | join(' ')} ${OPENSSL_LIBS} -o build/app

  test:
    deps: [build]
    run: ./tests/run.sh build/app

  clean:
    phony: true
    run: rm -rf build
```

### Multi-tool release pipeline

```yaml
includes:
  - "<probes/system.yml>"
  - "<probes/go.yml>"
  - "<probes/docker.yml>"
  - "<probes/k8s.yml>"
  - "<targets/manifest.yml>"

vars:
  version:   !sh {cache: per_run, run: "git describe --tags --dirty"}
  git_sha:   !sh {cache: per_run, run: "git rev-parse HEAD"}
  image:     "myorg/myapp"
  profile:   "${PROFILE:-staging}"

targets:
  build:
    when: "${HAVE_GO}"
    env: { CGO_ENABLED: "0", GOFLAGS: "-trimpath" }
    run: |
      go build -ldflags "-X main.version=${version} -X main.sha=${git_sha}" \
        -o build/myapp ./cmd/myapp

  image:
    deps: [build]
    when: "${HAVE_DOCKER}"
    run: !plugin
      name: docker_image_build
      input:
        dockerfile: Dockerfile
        context:    .
        tags:       ["${image}:${version}", "${image}:${profile}"]
      capture_outputs:
        image_digest: "IMAGE_DIGEST"

  deploy:
    deps: [image, manifest]
    all:
      - "${HAVE_K3S}"
      - "${profile} != 'local'"
    run: |
      kubectl apply -f deploy/${profile}.yml
      kubectl set image deployment/myapp app=${image}@${IMAGE_DIGEST}

  release:
    deps: [deploy]
    when: "${profile} == 'prod'"
    run: |
      tar czf release-${version}.tar.gz build/myapp build-manifest.json.gz
```

---

## Appendix A: Discoverability Commands

The tool's conceptual surface is enumerable by querying the tool itself:

```bash
gmk tags                 # all known tags + descriptions
gmk funcs                # all built-in expression functions
gmk probes               # all probes (declared and cached)
gmk plugins              # available plugins
gmk daemon status        # running daemons
gmk script <target>      # materialized script path
gmk explain <target>     # full resolution trace
```

A new user can `gmk help` then `gmk <subcommand> --help` and discover every primitive the tool offers without reading external documentation.

## Appendix B: Anti-Goals (Things We Deliberately Avoid)

- **Auto-discovery of project type or layout** — every input comes from explicit YAML.
- **Implicit profile/plugin activation** — every condition is a literal expression.
- **Configuration phase distinct from execution phase** — one phase, parse → execute.
- **Open schema** — unknown keys are parse errors.
- **DSL closures-as-config** — YAML is data; scripts are scripts.
- **Convention over configuration** — explicit over implicit, always.
- **Plugins with access to project model** — plugins are sandboxed map→map.
- **Recursive includes that mutate parent scope** — includes contribute to scope; they don't mutate enclosing files.
- **`make help` style "documented in comments only"** — `gmk explain` and `--help` are first-class.

## Appendix C: Glossary

| Term | Meaning |
|---|---|
| **IR** | Intermediate Representation — parsed, resolved, cacheable form of YAML |
| **Producer** | Unified term for "things that run" — both targets and value-producing vars |
| **Scope** | Lexical visibility region for vars; inherited from enclosing context |
| **Tag** | Typed value constructor (e.g., `!sh`, `!probe:has_tool`) |
| **Probe** | Environment-detection operation, cached at `gmk configure` |
| **Manifest** | Build provenance document (frozen IR state + probe results + source hashes + run metadata) |
| **Materialized script** | Generated `.sh`/`.py`/`.ps1` file in `.gmk-cache/code/` |
| **Cache mode** | Determines when a value evaluates and how long the result persists |
| **Plugin** | Language-neutral extension via map→map JSON contract |
| **Daemon** | Persistent worker process for high-frequency operations |

---

*End of specification.*