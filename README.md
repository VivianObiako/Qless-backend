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
