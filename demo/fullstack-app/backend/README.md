# backend

Tiny Go HTTP server connecting to Postgres. Two endpoints:

- `GET /api/health` — pings the DB, returns `{"status": "ok"}` or 503.
- `GET /api/messages` — returns the 50 most recent rows from a
  `messages` table. The table is created and seeded automatically on
  first boot.

## Build

The `images-backend-binary` target in the parent `gmk.yml` does:

```
cd backend
go mod tidy            # first time only — fetches lib/pq
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o build/server ./...
```

You'll want to run `go mod tidy` once manually before the first
`gmk run images-backend-binary` so the go.sum gets populated.

## Connection config

Reads from these env vars (all have sensible defaults for the demo's
k8s deployment):

| Var          | Default      |
|--------------|--------------|
| `DB_HOST`    | `postgres`   |
| `DB_PORT`    | `5432`       |
| `DB_USER`    | `appuser`    |
| `DB_PASSWORD`| `apppass`    |
| `DB_NAME`    | `appdb`      |
| `PORT`       | `8080`       |

The k8s Deployment (templates/app.yaml.jinja) sets these from the
project's structured vars.
