# local-registry — Zot OCI registry in docker

Tiny gmk project that runs a [Zot](https://zotregistry.dev) OCI
registry in a docker container. Used by `demo/fullstack-app/` to
push images and OCI artifacts during the build pipeline.

Why not Docker Hub or a real registry? Because the demo's whole point
is to produce a self-contained bundle that doesn't require an
external account or network. Zot runs in 30 MB of RAM and a single
container.

## Run it

```
gmk run start         # boot the registry in the background
gmk run status        # confirm it's listening
gmk run stop          # stop it
gmk run logs          # tail container logs (Ctrl-C to detach)
```

By default, listens on `localhost:5000`. Override via vars:

```
gmk run start --no-cache    # any value works; pull fresh
```

(Note: gmk doesn't take per-run --arg yet — that's Stage 4. Override
by editing the `zot_port` var in `gmk.yml` for now.)

## What's running

A single docker container `gmk-zot-demo` from
`ghcr.io/project-zot/zot-linux-amd64:latest`, with `/var/lib/registry`
mounted as the named volume `gmk-zot-data` so images survive
container restarts.

For a tighter setup with TLS and auth, the fullstack-app demo's
`pki:` module shows how to wire those in.
