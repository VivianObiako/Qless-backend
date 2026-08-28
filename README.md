# Qless — API

The Go backend for [Qless](https://github.com/VivianObiako/Qless), a zero-install
virtual queue for physical businesses. The Next.js frontend lives in its own
repository.

## Running it locally

Postgres runs in Docker; the API runs on the host.

```bash
cp .env.example .env
make up        # start Postgres and wait for it
make api       # run the server on :8080
```

`make seed` creates a demo queue and prints a customer link, a dashboard link
and a recovery code.

Migrations are applied on server start as well as by `make migrate`, so a fresh
clone reaches a working state without a separate setup step.

## Tests

```bash
make test
```

The integration tests need `TEST_DATABASE_URL`. `make up` creates that database
via `scripts/init-test-db.sql`, and `make migrate` brings it up to date.

## Configuration

| Variable | Required | Notes |
|---|---|---|
| `DATABASE_URL` | yes | Managed Postgres needs `sslmode=require` |
| `TEST_DATABASE_URL` | tests only | Integration tests skip when unset |
| `PORT` | no | Defaults to `8080`; hosts that inject `PORT` override it |
| `ALLOWED_ORIGIN` | no | Comma-separated origins for CORS and the WebSocket handshake |

No secret is committed. `.env` is ignored; `.env.example` carries local
development defaults only.

## Layout

```
cmd/          server, migrate and seed entry points
internal/api  HTTP handlers and routing
internal/…    queue rules, storage, realtime hub, tokens, middleware
migrations/   goose migrations, embedded into the binary
docs/         product docs shared with the frontend repo
```

## Deploying

The API runs on Render from the `Dockerfile`; Postgres is a Neon database.
Neon rather than Render's own Postgres because Render's free database expires
after a month and Neon's does not.

**1. Database.** Create a Neon project and copy the *pooled* connection string.
It already ends in `sslmode=require`, which the driver needs.

**2. Service.** On Render, create a Blueprint from this repository — it reads
`render.yaml` and provisions a Docker web service with `/healthz` as its health
check. Both environment variables are declared `sync: false`, so Render asks for
them instead of reading them from the repo:

| Variable | Value |
|---|---|
| `DATABASE_URL` | The Neon pooled connection string |
| `ALLOWED_ORIGIN` | The Vercel production domain, plus any preview domain |

`PORT` is injected by Render and read automatically. No secret is ever committed
— the repository contains only `.env.example`.

**3. Migrations** run when the server boots, so a deploy needs no release step.

### Two things to know about the free tier

The service sleeps after roughly 15 minutes with no traffic, and the first
request afterwards takes about 50 seconds. An open WebSocket counts as traffic,
so it stays awake while anyone is watching a queue — but a real counter should
be on a paid instance.

Neon's compute also sleeps and wakes in under a second.

### Previews

`ALLOWED_ORIGIN` accepts a comma-separated list, which is what lets a Vercel
preview deployment talk to this API:

```
ALLOWED_ORIGIN=https://qless.app,https://qless-git-my-branch.vercel.app
```

CORS answers with whichever origin actually called, and the WebSocket handshake
asks the same question through `httpx.OriginAllowed`, so the two cannot drift.
