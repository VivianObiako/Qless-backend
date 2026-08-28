# Qless

A zero-install virtual queue for physical businesses — barbershops, clinics,
restaurants, repair shops.

**Join the queue. Walk away. We'll let you know when you're close.**

Customers scan a QR code, enter a name, and get a number. No app, no account, no
email. The operator runs the queue from a dashboard on the counter.

---

## Status

All four milestones are complete, and the work has continued past them.

| | |
|---|---|
| ✅ Milestone 1 | Schema, create queue, join, recover session, leave, serve next, operator dashboard, customer view |
| ✅ Milestone 2 | WebSocket realtime with audience-scoped payloads and reconnection |
| ✅ Milestone 3 | Skip, serve a specific customer, attend, pause/resume/close, reset, history, settings |
| ✅ Milestone 4 | Display mode, browser notifications, accessibility and responsive passes |

Beyond the brief: one anonymous **owner** can hold several queues and delegate
the counter to named **operators** without handing over the business, customer
names are a per-queue setting that operators see only if the queue says so, and
the dashboard's screens share a left-edge drawer rather than a row of tabs.

[PLAN.md](PLAN.md) is the source of truth for what has been built, phase by
phase, and carries the backlog — multi-seat queues, services, archiving a queue.
[DECISIONS.md](DECISIONS.md) has the reasoning behind every judgment call.

The UI follows the "Ticket Pass" direction in `design_handoff_qless_ui/` — a
physical ticket in Instrument Serif numerals on a near-black shell, where
getting closer to your turn inverts the screen rather than colouring it.
Vermilion appears on exactly two surfaces in the whole product.

See [PROMPT.md](PROMPT.md) for the full specification.

---

## Requirements

- Go 1.23+
- Node 20+
- Docker (for Postgres)

## Running it

```bash
cp .env.example .env
make up
```

Then in two terminals:

```bash
make api
```

```bash
make web
```

The API listens on `http://localhost:8080`, the web app on
`http://localhost:3000`. Migrations run automatically when the API starts.

### Demo

```bash
make seed
```

Creates "Ade's Barbershop" with four customers already waiting and prints two
links: the customer view and the operator dashboard. Open the customer view on a
phone (same network) and the dashboard on a laptop.

Serve a customer from the dashboard and watch the phone change without touching
it: the position, the estimate and the whole tone of the screen escalate as the
number gets closer. Pull the network on the phone and the indicator drops to
"Reconnecting…", then returns to "Live" and refetches on its own.

The dashboard also carries the QR code customers scan to join, and
`/print/{slug}` is a sheet to tape to the door.

Each run creates a fresh queue and prints its recovery code alongside the links.
Both credentials are shown once and never stored in recoverable form, so reusing
an old demo queue would leave you locked out of its dashboard.

### Tests

```bash
make test
```

Backend tests run against a separate `qless_test` database, created automatically
by `make up`. They cover concurrent joins, duplicate prevention, rejoining after
leaving, capacity, paused and closed queues, serve-next transitions, operator
authorization, and the guarantee that public payloads carry no customer names.

The identity tests cover the parts that are dangerous to get wrong: recovering on
a new device leaves other sessions signed in, an acknowledged recovery code
cannot be replayed while an unacknowledged one still works, redeem is rate
limited and answers every bad code identically, and another owner's valid token
opens nothing. The migration is walked end to end on a scratch database — a queue
created under the old single-token schema is still reachable with that token
afterwards.

The operator tests cover the counter itself: serving one customer by name leaves
everyone else's position alone, a skipped customer keeps their record and can
rejoin for a new number, acting on a row someone else already dealt with answers
409 rather than doing it twice, an entry from another queue is unreachable, and
reset clears the line and restarts numbering while keeping history.

The roster tests cover the permission table as a table — what an operator may do
and what they may not, endpoint by endpoint — plus the things that must bite
immediately: a revoked operator's session and code both stop working on the next
request, a regenerated code retires the old one, and one owner cannot reach
another's staff.

The names tests check all three surfaces a customer name could reach at once —
the dashboard response, the history response and the staff socket frame — with
the toggle off, and each inverse with it on, so hiding names from everybody
cannot pass as a fix.

The realtime tests connect real WebSockets to a test server and assert that a
join and a serve reach a connected client, that the operator's frames carry names
while a public client's never do, and that a socket presenting a wrong owner
token is refused rather than downgraded.

## Commands

| Command | What it does |
|---|---|
| `make up` | Start Postgres and wait for it to be healthy |
| `make down` | Stop Postgres |
| `make reset-db` | Destroy and recreate the database volume |
| `make migrate` | Apply migrations to both databases |
| `make api` | Run the Go API server |
| `make web` | Run the Next.js dev server |
| `make seed` | Create the demo queue |
| `make test` | Run backend tests |

## Layout

```
api/
  cmd/           server, migrate, seed
  internal/
    api/         HTTP handlers, request shaping, error mapping, events
    queue/       domain types and pure logic (wait estimates, positions)
    storage/     the only package that speaks SQL
    realtime/    the WebSocket hub and its connections
    httpx/       JSON, middleware, rate limiting
    token/       opaque token issue and hashing
    database/    migration runner
  migrations/    embedded SQL
web/
  app/           routes — landing, create, enter, queues, operators,
                 q/[slug], dashboard/[id]{,/history,/settings},
                 display/[slug], print/[slug]
  components/    Qless product UI
  components/ui/ shadcn registry components
  hooks/         useCustomerQueue, useOperatorQueue, usePublicQueue,
                 useQueueSocket, useTurnNotifications, useStoredValue, useTheme
  lib/           API client, shared types, session tokens, access classification
  e2e/           Playwright
```

## How identity works

There are still no accounts. What there is instead is a **code** you type and a
**session token** your browser holds, and the database stores only their hashes.

- **Customer token** — issued on join, kept in `localStorage`, sent as
  `X-Customer-Token`. Lets someone close their browser, rescan the QR code, and
  recover their position. Names are display data, never identity.
- **Recovery code** — returned once when a queue is created, in the form
  `XXXX-XXXX-XXXX-XXXX`. It identifies the *business*, not a queue, and it is
  never sent on an ordinary request: it is redeemed at
  `POST /api/access/redeem` for a session token. Redeeming rotates it, and the
  replacement only takes effect once the client acknowledges it.
- **Access code** — the same shape, held by an operator. Reusable rather than
  single-use, and the owner can regenerate or withdraw it at any time. It says
  who they are; the queues they can open come from what the owner assigned them.
- **Session token** — one signed-in device, held in `localStorage` under
  `qless.session.token` and sent as `Authorization: Bearer …`. A principal may
  hold many, which is why recovering on a new phone does not sign the counter
  tablet out and why "sign out other devices" is a separate, deliberate request.
  A dashboard link may still carry one as `?k=` to move it to another device;
  the receiving browser stores it and takes it straight back out of the URL.

A queue id says *which* queue; the token says *who is asking*. Holding a queue's
id, slug or dashboard URL grants nothing on its own — every operator endpoint
resolves the token to an actor and asks whether that actor may act on that
queue. Nothing relies on the frontend hiding a button.

Which means there are two ways in and both are screens, not links: `/create`
starts a business and shows its recovery code once, and `/enter` turns a code
back into a session. `/queues` is what either of them lands on — the list the
server returns for whoever you turn out to be.

Redeem is the one endpoint where guessing wins something, so it is paced by
address and by code prefix, locks an address out after sustained failures, and
answers every wrong code identically along a single path.

## Who sees customer names

Three audiences, decided by the server and never by the client:

- **Customers and display screens** get numbers. Always, on every surface.
- **The owner** gets names. Always.
- **Operators** get names only if that queue says so — `show_names_to_operators`,
  off by default. A barbershop turns it on; a clinic leaves it alone.

The split is enforced where the payload is built, not where it is rendered, and
it covers the dashboard, the history screen and the realtime socket alike.
Changing the setting takes effect on the next frame, so screens already open
stop showing names without anyone signing out.

The same applies to the WebSocket. A browser cannot set an `Authorization` header
on a handshake, so the operator's socket carries the token as `?k=` and the
server verifies it before upgrading. Without a valid token a socket receives
public frames — numbers, never names — for as long as it is connected, and there
is nothing it can send to change that.

## Environment

| Variable | Used by | Purpose |
|---|---|---|
| `DATABASE_URL` | api | Postgres connection string |
| `TEST_DATABASE_URL` | api tests | Separate database for the test suite |
| `PORT` | api | HTTP port, defaults to 8080 |
| `ALLOWED_ORIGIN` | api | CORS origin for the web app |
| `NEXT_PUBLIC_API_URL` | web | Where the browser reaches the API |

`.env` is read from the repository root. Never commit it; `.env.example` is the
template.

## Deploying

The API is a single Go binary with migrations embedded, so any host that runs a
container or a binary will do — Fly.io, Render, Railway. Set `DATABASE_URL` and
`ALLOWED_ORIGIN`; migrations apply on boot.

The web app is a standard Next.js deployment. Set `NEXT_PUBLIC_API_URL` to the
API's public URL.

Postgres can be any managed instance; the schema is small enough for a free tier.

### Known limitation

The realtime hub is in-process, so the API is a **single-instance** deployment.
Running two instances would leave clients connected to one unaware of changes
made through the other. Postgres `LISTEN/NOTIFY` is the path to fixing that when
it matters; it is deliberately not built yet.

WebSockets need a host that supports them and a proxy that will not idle them
out. The server pings every 54 seconds, which is inside the usual timeouts, and
a dropped connection is not a failure state — the browser backs off, reconnects
and refetches.

