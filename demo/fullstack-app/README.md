# fullstack-app — gmk demo

A complete deployment pipeline modeled in one `gmk.yml`: bootstraps a
CA, builds a Go backend and an HTML/JS frontend into container images,
pushes them to a local Zot OCI registry, generates k8s manifests via
jinja templates, packs everything into an OCI artifact, and deploys to
k3s on a prod host using `oras pull` instead of `git clone`.

This is the validation case for Stage 3c.2 — every section exercises
shipped gmk features and surfaces what the feature set can already do.

## What the demo proves

| Feature exercised                | Where                                                                   |
|----------------------------------|-------------------------------------------------------------------------|
| Structured vars (nested maps)    | `backend:`, `frontend:`, `postgres:`, `ingress:` blocks in `vars:`      |
| Structured vars as iterable list | `apps:` list of maps                                                    |
| `${render:tmpl(...mapvar)}` splat| `manifests-postgres` target, `manifests-ingress` target                 |
| Iteration combinator (`map:`)    | `manifests-apps` target — N apps without duplicating target definitions |
| Python function w/ JSON IPC      | `render-app-manifest` function                                          |
| Templates (jinja engine)         | All 5 templates under `templates/`                                      |
| Multi-language fan-out           | Bash targets + Python function in the same project                      |
| Target preludes via `env:`       | `images-backend-binary` (GOOS/GOARCH set in env block)                  |
| Deep `deps:` chains              | `all` → `deploy` → `bundle` → `manifests` → `pki` (5 levels)            |

It also surfaces one **real limitation**: gmk's `includes:` mechanism
promotes vars but NOT targets/functions/templates from included files.
So the natural module split (gmk/pki.yml, gmk/registry.yml, ...) isn't
viable today; this demo consolidates everything into one `gmk.yml`
with section comments instead. Documented inside the file's header.

## Prerequisites

The targets shell out to real tools. On the build machine you'll need:

| Tool       | Used by section          | Reason                                  |
|------------|--------------------------|-----------------------------------------|
| `docker`   | registry, images, build  | run Zot, build images                   |
| `go` 1.22+ | images-backend-binary    | compile the backend                     |
| `openssl`  | pki                      | generate CA + server cert               |
| `oras`     | bundle, deploy           | push/pull non-image OCI artifacts       |
| `kubectl`  | deploy                   | apply manifests                         |
| `k3s`      | deploy                   | the target cluster                      |
| `python3` + `jinja2` | manifests-apps | the render-app-manifest function uses Python's jinja2 |

For the demo to **load** in gmk (parse + dryrun) you don't need any of
these — just the gmk binary. To **run** any target end-to-end, you
need the tools that target uses.

## First-time setup

Before the first run, create the CA password file:

```
mkdir -p ~/.gmk/fullstack-app
chmod 700 ~/.gmk/fullstack-app
printf '%s' 'your-strong-passphrase' > ~/.gmk/fullstack-app/key
chmod 600 ~/.gmk/fullstack-app/key
```

(The `pki-ca` target also accepts `~/.gmk/key` as a global fallback if
you have multiple gmk projects sharing one CA password.)

Then tidy the backend's Go module once:

```
cd backend && go mod tidy && cd ..
```

## The workflow

```
gmk run setup       # generate CA + boot local Zot
gmk run build       # build backend + frontend images, push to Zot
gmk run manifests   # render all k8s YAML
gmk run bundle      # tar + push as OCI artifact to Zot
gmk run deploy      # oras pull + kubectl apply against k3s
```

Or, end-to-end:

```
gmk run all
```

## Section walkthrough

The `gmk.yml` is divided into six numbered sections plus aggregates.

### Section 1 — PKI (`pki-*`)

`pki-ca` reads the raw password from `~/.gmk/<project>/key`, generates
an `aes256`-encrypted CA private key, then a self-signed CA cert. The
password is passed to openssl via `-passout file:...` so it never
appears on the command line.

`pki-server-cert` generates an unencrypted server key + a cert signed
by the CA. SAN list includes the ingress host and `localhost`.

Outputs land in `.gmk-cache/out/pki/`. Only `ca.crt` and `server.crt`
get bundled — keys stay local.

### Section 2 — Registry (`registry-*`)

`registry-ensure` is the idempotent boot for a Zot container at
`localhost:5000`. Other targets depend on it so it's always up before
push/pull. `registry-reset` wipes the data volume; `registry-stop`
preserves it.

### Section 3 — Images (`images-*`)

Two parallel pipelines:

```
images-backend-binary → images-backend-image → images-backend-push
images-frontend-image                       → images-frontend-push
```

The backend is built statically (`CGO_ENABLED=0`) and lives in a
distroless image. The frontend has no build step — nginx + a COPY of
plain HTML/JS/CSS.

### Section 4 — Manifests (`manifests-*`)

This is where the iteration combinator earns its keep. Per-app
manifests (Deployment + Service for both backend and frontend) are
rendered by:

```yaml
${join(map:render-app-manifest(items=apps,
                               out_dir=manifests_dir,
                               template_path="templates/app.yaml.jinja",
                               ...), " ")}
```

Adding a third app means adding one row to `vars.apps`. No new
target. The function uses Python's own jinja2 (since gmk's `render:`
doesn't fire from inside a function body — same template syntax
either way).

Single-instance manifests (namespace, postgres, ingress) use
`${render:tmpl(...mapvar)}` splat directly.

### Section 5 — Bundle (`bundle-*`)

`bundle-stage` collects:
- All rendered manifests
- `pki/ca.crt` and `pki/server.crt` (public certs only)
- A rendered `install.sh` for prod-host use

`bundle-create` tars it. `bundle-push` pushes to Zot as an OCI
artifact with a custom media type (`application/vnd.gmk.bundle.v1.tar+gzip`)
plus standard `org.opencontainers.image.*` annotations.

### Section 6 — Deploy (`deploy-*`)

These run on the **prod host**. The pipeline is just `oras pull` →
extract → `kubectl apply -f manifests/`. The prod host needs nothing
from this source tree.

For prod hosts without gmk installed, the bundled `install.sh` script
does the same steps standalone.

## Layout

```
demo/fullstack-app/
├── README.md         (this file)
├── gmk.yml           (the whole pipeline, ~650 lines, well-sectioned)
├── backend/
│   ├── main.go       (Go HTTP server + Postgres client)
│   ├── go.mod
│   ├── Dockerfile    (distroless static)
│   └── README.md
├── frontend/
│   ├── index.html    (vanilla HTML, no framework)
│   ├── app.js        (vanilla JS, fetch + DOM)
│   ├── style.css
│   ├── nginx.conf    (proxies /api → backend Service)
│   └── Dockerfile    (nginx:1.27-alpine)
└── templates/
    ├── namespace.yaml.jinja
    ├── postgres.yaml.jinja      (StatefulSet + Service + PVC + Secret)
    ├── app.yaml.jinja           (per-app Deployment + Service, used by map:)
    ├── ingress.yaml.jinja       (Traefik)
    └── install.sh.jinja         (bundled in the OCI artifact)
```

## Validating without the tooling

You can confirm the project parses without docker/k3s/openssl/oras:

```
gmk list                            # all 38 targets visible
gmk doc pki-ca                      # check one target
gmk dryrun all                      # see the full dep graph
```

The integration test suite intentionally doesn't exercise this demo
because the tooling isn't available in CI.
