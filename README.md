# Less Queues — API

The Go backend for [Less Queues](https://github.com/VivianObiako/Qless), a zero-install
virtual queue for physical businesses. The Next.js frontend lives in its own
repository.

## Running it locally

The quickest way in, with nothing but Docker installed:

```bash
docker compose up --build   # Postgres and the API on :8080
```

Point the web app at `http://localhost:8080` and it works. Migrations run when
the server starts, so a fresh clone needs no setup step.

To work on the API itself, run Postgres in Docker and the server on the host,
where a code change is a restart rather than a rebuild:

```bash
cp .env.example .env
make up        # start Postgres only, and wait for it
make api       # run the server on :8080
```

`make seed` creates a demo queue and prints a customer link, a dashboard link
and a recovery code. It works with either way of running the API.

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
| `ALLOWED_ORIGIN` | no | Comma-separated origins for CORS and the WebSocket handshake; the first is where a tapped notification lands |
| `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY` | no | Turn on Web Push. `go run ./cmd/vapid` prints a pair; without one the pass nudges from the page |
| `VAPID_SUBJECT` | no | `mailto:` contact sent with each push, per the spec |

No secret is committed. `.env` is ignored; `.env.example` carries local
development defaults only.

## Layout

```
cmd/          server, migrate, seed and vapid entry points
internal/api  HTTP handlers and routing
internal/…    queue rules, storage, realtime hub, tokens, middleware
migrations/   goose migrations, embedded into the binary
docs/         product docs shared with the frontend repo
```

## Environments

There are three, and they share nothing:

| | Development | Staging | Production |
|---|---|---|---|
| Runs on | Your machine | Render `lessqueues-api-staging` | Render `qless-api-eu` |
| Branch | any | `staging`, once CI passes | `main`, once CI passes |
| Database | Docker Postgres from `docker-compose.yml` | Its own empty Neon database | Neon project `qless-eu` |
| Web origin | `http://localhost:3000` | `https://staging.lessqueues.com` and Vercel previews | `https://lessqueues.com`, then the old `https://qless-indol.vercel.app` |
| Push keys | optional | its own pair | its own pair |

The rule that keeps them apart: **a value that names one environment never
appears in another's settings.** Staging's `DATABASE_URL` is never production's,
and production's `ALLOWED_ORIGIN` never lists a preview domain.

Changes move one way. Work on a feature branch, merge it into `staging`, check
it there against its own data, then merge `staging` into `main`. Production
deploys from `main` only.

Staging's database is created empty and migrated by the server on first boot. It
is deliberately not a Neon branch of production, because a branch carries every
real queue, ticket and owner token with it.

## Deploying

The API runs on Render from the `Dockerfile`; Postgres is a Neon database.
Neon rather than Render's own Postgres because Render's free database expires
after a month and Neon's does not.

**1. Database.** Create a Neon project and copy the *direct* (unpooled)
connection string, with `sslmode=require`, which the driver needs. Create one
project per environment: production and staging never share one.

**2. Service.** On Render, create a Blueprint from this repository. It reads
`render.yaml` and provisions a Docker web service per entry, each with `/healthz`
as its health check. Every environment variable is declared `sync: false`, so
Render asks for them instead of reading them from the repo:

| Variable | Value |
|---|---|
| `DATABASE_URL` | That environment's own Neon connection string |
| `ALLOWED_ORIGIN` | That environment's web domains. The first is where a tapped push notification lands |
| `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY`, `VAPID_SUBJECT` | That environment's own push keys |

`PORT` is injected by Render and read automatically. No secret is ever committed
— the repository contains only `.env.example`.

**3. GitHub.** Install the Render GitHub app on this repository. Both services
deploy only after CI passes (`autoDeployTrigger: checksPass`), and Render learns
of a push and its checks through that app. Without it nothing deploys on its
own, and the Blueprint never syncs.

**4. Migrations** run when the server boots, so a deploy needs no release step.
That is also what builds a new staging database from nothing.

### Two things to know about the free tier

The service sleeps after roughly 15 minutes with no traffic, and the first
request afterwards takes about 50 seconds. An open WebSocket counts as traffic,
so it stays awake while anyone is watching a queue — but a real counter should
be on a paid instance.

Neon's compute also sleeps and wakes in under a second.

### Previews

`ALLOWED_ORIGIN` accepts a comma-separated list, which is what lets a Vercel
preview deployment talk to the **staging** API:

```
ALLOWED_ORIGIN=https://qless-*-vivianobiakos-projects.vercel.app,http://localhost:3000
```

A `*` stands for one run of characters with no dot or slash, so that one entry
covers every preview and the `staging` branch of this Vercel project and
nothing on another domain. Previews belong to staging, not production: the
production API lists its own domain only.

CORS answers with whichever origin actually called, and the WebSocket handshake
asks the same question through `httpx.OriginAllowed`, so the two cannot drift.
