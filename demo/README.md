# gmk demos

Two reference projects exercising gmk's current feature set end to
end. Unlike `examples/`, these are *full-blown* — they target real
infrastructure (k3s, Zot OCI registry, Postgres) and produce
deployable artifacts. They exist to validate that gmk's current
shipped features (no new ones added since Stage 3c.2) are sufficient
to model a real production deployment pipeline.

| Demo                | What it shows                                                        |
|---------------------|----------------------------------------------------------------------|
| `fullstack-app/`    | Go backend + plain HTML/JS frontend + Postgres on k3s; OCI bundling for prod-side deploy without git-clone |
| `local-registry/`   | Just the Zot lifecycle — start/stop a local OCI registry in docker; reusable building block |

## Prerequisites

The demos invoke external tooling rather than reimplementing it:

- `docker` (for image builds and running Zot locally)
- `kubectl` and `k3s` (or any kubeconfig-reachable cluster — k3s is the easy default)
- `oras` ([OCI Registry As Storage](https://oras.land/)) for pushing & pulling non-image artifacts
- `openssl` (for the CA bootstrap)
- `go` and a recent `node` are NOT required by the frontend (it's plain HTML/JS) but Go is required for the backend

If a demo target depends on one of these and you don't have it
installed, the failure will be a missing-binary error from the
runner, not a confusing gmk error.

## Sandbox / structure validation only

These demos are NOT exercised by the integration tests because the
tests can't assume docker, k3s, openssl, oras, etc. are available.
What CAN be checked locally without any external tooling:

```
cd demo/fullstack-app
gmk list                    # all targets and functions visible?
gmk doc <target-name>       # the doc shape parses?
gmk dryrun build            # the dep graph is sane?
```

For the real deploy, you provide the tooling; gmk orchestrates.
