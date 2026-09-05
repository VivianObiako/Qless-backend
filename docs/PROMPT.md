# Build Qless — a zero-install virtual queue

Build a working MVP called **Qless**: a virtual queue for physical businesses (barbershops, clinics, restaurants, repair shops). A customer scans a QR code, enters a name, joins the queue, and walks away. The operator runs the queue from a dashboard. Everyone sees changes in real time.

**Promise:** Join the queue. Walk away. We'll let you know when you're close.

Build the real product, not scaffolding. No placeholder buttons, no core functionality left as TODO.

---

## Rules of engagement

- Work through the milestones in order. **Stop after each milestone and report** what works before starting the next.
- If two requirements conflict, or a decision is genuinely ambiguous, **stop and ask** — do not guess.
- Record every judgment call in `DECISIONS.md` (one line each: decision, why).
- Priorities are tagged **P0 / P1 / P2**. If time or complexity forces a cut, cut from P2 up. Never simplify a P0.

---

## Stack (fixed — do not substitute)

| | |
|---|---|
| Backend | Go 1.23+, stdlib `net/http` with `ServeMux` method+path patterns. No router dependency. |
| DB | PostgreSQL 16, `pgx/v5`, hand-written SQL. No ORM. |
| Migrations | `goose`, plain `.sql` files in `/migrations` |
| WebSockets | `gorilla/websocket` |
| Frontend | Next.js 16 App Router, TypeScript `strict`, Tailwind v4 |
| Components | Hand-built. Radix primitives **only** for Dialog (accessibility). No component library. |
| State | No global store. One `useQueue()` hook owning fetch + WebSocket. |
| QR | `qrcode.react` for render, canvas → PNG for download. No third-party QR API. |
| Font | Inter via `next/font` |
| Tests | Go stdlib `testing`; Playwright for E2E |

Single Go binary. No microservices, no Redis, no message broker.

---

## Design tokens (use these values, not adjectives)

```
Page          #F8F8F7      Surface       #FFFFFF
Text          #18181B      Text muted    #71717A      Text faint  #A1A1AA
Border        #E4E4E7      Border strong #D4D4D8
Accent        #4F46E5      Accent hover  #4338CA      Accent bg   #EEF2FF

Green  text #15803D  bg #F0FDF4   — active, healthy, attended
Amber  text #B45309  bg #FFFBEB   — getting close, warning
Red    text #DC2626  bg #FEF2F2   — your turn, urgent, error, closed
Gray   text #71717A  bg #F4F4F5   — inactive, skipped, completed

Radius   6px controls · 10px cards · full only for status dots
Shadow   sm: 0 1px 2px rgb(0 0 0 / .05)   md: 0 1px 3px rgb(0 0 0 / .08)
Spacing  4px base scale
Type     12 · 14 · 16 · 20 · 24 · 32 · 48 · 72 · 120
```

Queue numbers are the visual centrepiece: `font-variant-numeric: tabular-nums`, tight tracking, weight 600–700, 72px+ on customer view, 120px+ on display mode. `#27` must dominate the label above it.

Colour carries meaning — do not make everything indigo, and never use colour as the only signal (pair with text and/or icon).

**Avoid:** gradients, glassmorphism, card soup, decorative charts, pill-shaped everything, marketing illustrations, ambient animation. Reference points: Linear, Apple, modern POS and clinic check-in systems. Calm and operational, not SaaS-dashboard.

---

## Data model

```
owners                                             00002
  id uuid pk · recovery_code_hash unique
  pending_recovery_code_hash unique null
  created_at · updated_at

operators                                          00004
  id uuid pk · owner_id fk · display_name
  access_code_hash unique null · status ACTIVE | REVOKED
  created_at · updated_at

operator_queues  operator_id fk · queue_id fk · created_at

access_tokens                                      00002, 00004
  id uuid pk · token_hash unique
  principal_type OWNER | OPERATOR
  owner_id fk null · operator_id fk null
  created_at · last_seen_at

queues
  id uuid pk · name · slug unique · description
  average_service_minutes int · max_capacity int null
  status  OPEN | PAUSED | CLOSED
  next_number int default 1
  owner_id fk · show_names_to_operators bool default false
  created_at · updated_at

queue_entries
  id uuid pk · queue_id fk · number int
  customer_name · customer_token_hash
  status  WAITING | SERVING | ATTENDED | SKIPPED | LEFT | CLEARED
  joined_at · started_at · completed_at
  acted_by_type null · acted_by_operator_id fk null
```

A session token points at exactly one principal — `principal_matches_type`
pins an OWNER row to an owner and no operator, and the reverse — and many rows
may exist per principal. That is what makes recovery non-destructive.

`acted_by_*` records who caused an entry's **current** status, not a full audit
trail. Revoking an operator is a soft delete precisely so history keeps
resolving to a name.

**Invariants — enforce in the database, not application code:**

- `one_serving_per_queue` — `UNIQUE (queue_id) WHERE status = 'SERVING'`. At most one customer being served per queue. There is no `current_entry_id` column; the current customer is derived from status. One source of truth. Parallel service points would begin by replacing this index — see the multi-seat entry in PLAN.md's backlog.
- `one_active_entry_per_number` — `UNIQUE (queue_id, number) WHERE status IN ('WAITING','SERVING')`. Partial, not total: 00003 narrowed it because "reset restarts numbering at 1" and "history is preserved" are otherwise mutually exclusive — the first customer of the new day collides with the cleared number 1 from the old one.
- `one_active_entry_per_token` — `UNIQUE (queue_id, customer_token_hash) WHERE status IN ('WAITING','SERVING')`. Duplicate prevention, in the database rather than in a check-then-insert.
- Indexes on `(queue_id, status)`, `(queue_id, number)` and `(customer_token_hash)`
- Number assignment happens inside a transaction that does `SELECT ... FOR UPDATE` on the queue row. Concurrent joins must never collide.
- Store token hashes only, never raw tokens. All timestamps UTC, set by the backend — never trust the browser clock.

---

## Semantics (these were ambiguous; here are the answers)

**Waiting order** is by `number` ascending, always. Serving a specific customer does not reorder anyone.

**People ahead** = count of `WAITING` entries in this queue with a lower number.

**Wait estimate** is computed server-side so every client agrees:
`base = peopleAhead × average_service_minutes`, range = `[base × 0.8, base × 1.2]`, each end rounded to the nearest 5, floor 5 min. Render as minutes below 90, otherwise `1h 50m`. Always present it as a range, never an exact promise. Zero ahead → no estimate, show the state instead ("You're next" / "It's your turn").

**Duplicate prevention** checks *active* entries only (`WAITING` or `SERVING`). A customer who was skipped or left **can rejoin** and receives a new number.

**Serve next / serve specific**: the entry currently `SERVING`, if any, transitions to `ATTENDED` with `completed_at` set. The chosen entry becomes `SERVING` with `started_at` set. Serving a specific customer leaves everyone else waiting, in order.

**Skip** sets `SKIPPED`; the record stays in history. The customer sees "You were skipped" with a *Rejoin queue* action, not a silent disappearance.

**Leave** sets `LEFT`, frees capacity, recalculates positions. Nothing is hard-deleted, ever.

**Reset** sets every `WAITING`/`SERVING` entry to `CLEARED` and `next_number` back to 1. History is preserved. No automatic date-based reset.

**Pause vs Close** are distinct: `PAUSED` blocks new joins but existing customers keep their positions and visibility. `CLOSED` blocks new joins; existing customers can still see their status. Both are reversible.

**Capacity**: when `WAITING + SERVING` reaches `max_capacity`, joins are rejected with "Queue is currently full." A departure reopens a slot. Null capacity means unlimited.

---

## Identity and authorization

**Customers (P0)** — no account, no email, no password. On join the server issues a random `customer_token`, stores its hash against `queue_id`, and the browser keeps it in `localStorage`. Rescanning the QR recovers the active entry: "You're already in this queue as #21." Names are display data, never identity.

**Owners (P0)** — no account either. Creating a queue mints an **owner** and returns a `XXXX-XXXX-XXXX-XXXX` **recovery code**, shown once; only its hash is stored. The code identifies the *business*, not a queue, and it is never sent on an ordinary request: it is redeemed at `POST /api/access/redeem` for a **session token**, which the browser holds and sends as `Authorization: Bearer …`.

Redeeming rotates the code, and the replacement is **staged** — the redeemed code keeps working until the client acknowledges the new one, because rotating in place turns a lost response into a permanent lockout.

A principal may hold many session tokens, one per signed-in device. Recovering on a new phone therefore does not sign the counter tablet out; **Sign out other devices** is a separate, deliberate `POST /api/sessions/revoke-others`. A dashboard link may still carry a session as `?k=` to move it between devices, and the receiving browser stores it and strips it from the URL.

**Operators (P1)** — named staff who work the counter. The owner issues each a reusable **access code**, typed into the same box at `/enter`; the code that matches decides the role, so nobody has to know which kind they hold. Operators are assigned to queues, can be reassigned, re-coded or revoked at any moment, and revoking is a soft delete so history keeps resolving to a name. See the permission table in `PLAN.md`.

**Never a bearer credential on every request.** Both kinds of code are redeemed once. A queue id says *which* queue; the session token says *who is asking*. Holding a queue's id, slug or dashboard URL grants nothing on its own.

Redeem is the one endpoint where guessing wins something: pace it by address and by code prefix, lock an address out after sustained failures, and answer every wrong code identically along a single path — including malformed ones, since answering those faster is itself a measurable difference.

**Enforcement**: a customer must never be able to skip, serve, close, reset, or reconfigure. Check on the Go side, on every request. Frontend hiding is not authorization.

---

## API

`{key}` accepts a queue id or a slug. `[access]` is any principal with a path to
that queue — the owner, or an assigned operator. `[owner]` is the owner alone.

```
GET    /healthz

POST   /api/access/redeem                       code → role, session, queues
POST   /api/access/recovery-code/acknowledge    settle a staged code   [owner]
GET    /api/me/queues                           who am I, what can I open
POST   /api/sessions/revoke-others              sign out my other devices [owner]
PATCH  /api/me                                  my display name        [owner]
GET    /api/push/key                            VAPID public key, 404 when push is off

GET    /api/operators                                                  [owner]
POST   /api/operators                           create, returns code once [owner]
PATCH  /api/operators/{id}                      rename, reassign       [owner]
POST   /api/operators/{id}/code                 regenerate             [owner]
POST   /api/operators/{id}/revoke                                      [owner]

POST   /api/queues                              create (recovery code once)
GET    /api/queues/{key}                        public queue state
PATCH  /api/queues/{key}                        config                 [owner]
POST   /api/queues/{key}/close|reset                                   [owner]
POST   /api/queues/{key}/archive|unarchive                             [owner]
POST   /api/queues/{key}/pause                  optional {note}        [access]
POST   /api/queues/{key}/resume                                        [access]
POST   /api/queues/{key}/next                   serve next             [access]
POST   /api/queues/{key}/entries                add a walk-in          [access]
POST   /api/queues/{key}/entries/{entryId}/serve|attend|skip|start     [access]
GET    /api/queues/{key}/entries                the dashboard's own view [access]
GET    /api/queues/{key}/history                ?limit= up to 1000, default 200 [access]

POST   /api/queues/{key}/join                   join
GET    /api/queues/{key}/me                     my active entry (customer token)
POST   /api/queues/{key}/presence               on my way / here / hold (customer token)
POST   /api/queues/{key}/push                   bind a push subscription (customer token)
DELETE /api/queues/{key}/push                   forget an endpoint
POST   /api/queues/{key}/leave                  leave

GET    /api/queues/{key}/ws                     realtime
```

Each handler calls `requireOwner` or `requireQueueAccess` itself. There is
deliberately no middleware, so a new route cannot silently skip the check by
forgetting to be wrapped in one.

**Events:** `QUEUE_UPDATED · CUSTOMER_JOINED · CUSTOMER_LEFT · CUSTOMER_SKIPPED · CUSTOMER_SERVED · CUSTOMER_ATTENDED · CUSTOMER_PRESENCE · QUEUE_PAUSED · QUEUE_RESUMED · QUEUE_CLOSED · QUEUE_RESET`

**Standing down.** Calling anybody while somebody is at the counter stands
that person down: attended if their service had begun (`servedAt` set),
skipped and held if it had not.

**Skip is not final.** A skipped entry keeps its number for the queue's
`holdMinutes` and can be served (recalled) in that window. After it the number
may have been reissued, and the call is refused with `recall_expired` (409).
The dashboard view lists the entries still inside the window as `skipped`.
A hold time of zero makes a skip final.

**Estimates learn.** `averageServiceMinutes` is the starting figure. Once the
last twelve hours hold five real service times, every estimate uses the
average of the last ten, and the public state says which figure it used in
`serviceMinutes`. A service time runs from `servedAt` — when the person was
actually at the counter — to done, falling back to the call for an entry
nobody marked. `servedAt` is inferred from presence and recall, or set by
`start`.

**Push.** With VAPID keys configured, the server sends each subscribed phone
the three nudges — close, next, your turn — once per rung, after the frame.
Without keys the key endpoint answers 404 and the pass nudges from the page.

**Walk-ins.** Staff can add a person who has no phone; the entry is flagged
`walkIn`, joins the back of the line and is served like any other.

**Payloads are scoped by audience — this is a privacy requirement, not a nicety.**

- *Public* (customers, display screens): `{ queue, servingNumber, waitingNumbers: number[], waitingCount, isFull, estimates }`. **No names** — the key is not blank, it is absent. A customer knows their own number from `/me` and computes their position from `waitingNumbers` locally, so no client ever receives another customer's name.
- *Owner* (session token presented on connect): the above plus full entries with names. Always.
- *Staff* (an operator's session): the owner's frame when the queue's `show_names_to_operators` is on — byte for byte, not a re-render — and a redacted one when it is off, with `customerName` blank and `showsNames: false` alongside it, so the client renders a queue of numbers deliberately rather than looking broken.

Three audiences, three frames, decided per event rather than per connection: an
owner who realises staff should not be seeing names flips the setting and it
stops on screens that are already open. `frameFor` defaults to the public frame,
so an audience added later without a frame leaks nothing.

Realtime hub is in-process. Single backend instance is acceptable — document that limitation in the README and note Postgres `LISTEN/NOTIFY` as the scaling path. Do not build it.

**Rate limiting**: in-memory token bucket — 5 joins/min per IP per queue, 30 writes/min per IP overall. Reject with 429 and a human-readable message.

Never leak raw backend errors to the UI.

---

## Frontend routes

```
/                        landing
/create                  create queue
/enter                   turn a code into a session
/queues                  the queues this session can open
/operators               the roster                                [owner]
/q/[slug]                customer: join + live status (one page, four states)
/dashboard/[id]          the counter
/dashboard/[id]/history  history
/dashboard/[id]/share    link, QR, print sheet, display, customer view
/dashboard/[id]/settings config                                    [owner]
/display/[slug]          public display
/print/[slug]            printable QR sheet
```

`/create` and `/enter` are the two ways in, and both are screens rather than
links. Everything from `/queues` inwards shares one frame: a sidebar that answers,
top to bottom, which queue (a switcher), what am I doing (Counter, History,
Share, Settings) and who am I (Team, appearance, sign out) — pinned from
1024px, a bar with tabs below it.

**Customer view (mobile-first, P0)** — business name, `Currently serving #14`, `Your number #21`, people ahead, estimated wait, a progress indicator, and *Leave queue* as a visible secondary action (not buried). State changes with proximity: many ahead → neutral/green "You're in the queue"; ≤3 ahead → amber "You're getting close"; 1 ahead → "You're next"; serving → red/prominent "It's your turn!" — impossible to miss.

**Operator dashboard (desktop-first, tablet-usable, P0)** — the customer being served dominates the screen, then a single obvious **Serve Next**, then the waiting list with number, name, estimate, and per-row *Serve this customer* / *Skip*. The operator should never have to hunt for the next action.

**Display mode (P1)** — `NOW SERVING`, the number at 120px+, next three numbers, and a QR to join. No names. Updates live. Legible across a room.

**Print sheet (P1)** — Qless mark, business name, "Scan to join the queue", QR. Must print cleanly in black and white.

**Landing (P1)** — hero "Stop waiting in line.", one line of support copy, *Create a queue* CTA, a five-step visual of the flow. Nothing more; the product matters more than the marketing.

---

## Cross-cutting requirements (P0 unless noted)

**Reconnection** — exponential backoff, subtle inline status ("Reconnecting…" → "Connected"), full state refetch on reconnect, no duplicated actions.

**Notifications (P1)** — browser Notification API at 3 away / 1 away / your turn. Opt-in, never mandatory, degrades silently where unsupported. The app is fully usable without it.

**Accessibility** — WCAG 2.2 AA. Semantic HTML, keyboard navigation, visible focus, labelled controls, accessible dialogs, contrast checked against the tokens above. ARIA live regions for queue changes — polite, and they must not steal focus. Respect `prefers-reduced-motion`.

**Animation** — only to show that the queue changed: number transitions, entries arriving and leaving, status changes. No ambient motion.

**States** — real loading skeletons and designed empty/error states: queue not found, closed, paused, full, connection lost, entry expired.

**Confirmation dialogs** — leave queue, skip, close, reset. Not for Serve Next.

**Responsive** — verify at 320 / 375 / 390 / 768 / 1024 / 1440px. No horizontal scroll anywhere.

---

## Milestones — stop and report after each

1. **Spine.** Docker Compose + Postgres, migrations, create queue, join, `/me`, serve next, operator dashboard, customer view. No realtime yet (manual refresh is fine at this stage). Concurrency-safe numbering from day one.
2. **Realtime.** WebSocket hub, scoped payloads, all events, reconnection, live customer + dashboard.
3. **Full operator surface.** Skip, serve specific, attend, pause, resume, close, reset, history, settings, capacity, authorization enforced end to end.
4. **Polish.** Display mode, QR + print sheet, notifications, landing page, empty/error/loading states, accessibility pass, responsive pass.

**All four are complete.** The work since is tracked as phases in `PLAN.md`,
which is the source of truth for what has been built and why: identity and
operators (1–5), polish (6), documentation (7), and the dashboard's navigation
(8–9). That plan also carries the backlog — multi-seat queues, services,
archiving a queue — and the reasoning behind each judgment call lives in
`DECISIONS.md`.

---

## Verification — automated, not a manual checklist

**Go tests (P0):** concurrent joins (fire 50 goroutines, assert 50 unique consecutive numbers, zero collisions) · duplicate join prevention · rejoin after skip and after leave · capacity limit · join blocked when paused and when closed · serve next · serve specific · skip · leave · reset · every operator endpoint rejects a missing or wrong owner token.

Since operators exist, add: the permission table asserted endpoint by endpoint · another owner's valid token opens nothing · a revoked operator's session and code both die on the next request · recovery leaves other sessions signed in · an acknowledged code cannot be replayed while an unacknowledged one still works · redeem is rate limited and answers every bad code identically · the toggle off leaves no name in the dashboard response, the history response or the staff socket frame, and each inverse with it on.

**Playwright (P1):** customer joins and sees their number · position updates live in a second browser context when the operator serves · customer recovers their entry after a browser restart · display mode updates without refresh · public WebSocket payload contains no customer names · owner recovers in a second context and sees every queue · operator redeems a code and sees only assigned queues.

Run them. Paste the passing output in your final report. Do not claim a scenario works without having executed it.

---

## Out of scope — do not build

AI, chat, payments, subscriptions, SMS, WhatsApp, email notifications, native apps, analytics dashboards, customer accounts, billing, calendar or appointment booking, reviews, loyalty.

**Staff roles were built** — see the operators section above. They were on this
list in the original brief and came off it deliberately: delegating the counter
without handing over the business turned out to be the difference between a demo
and something a shop with two employees could use.

**Multi-location and services are designed around, not built.** Queues hang off
an owner, and the authorization helpers never assume owner→queue is the only
path to a queue, so services are one table, one nullable column and one screen
whenever they are wanted. Parallel service points within one queue are a larger
job — see PLAN.md's backlog for why the SERVING index makes it so.

Qless stays a queue system.

---

## Deliverables

- `docker compose up` starts Postgres; documented commands start the API and the web app
- `make seed` creates a demo queue ("Ade's Barbershop", 15 min service time, a few waiting customers)
- `make test` runs backend tests
- `.env.example` covering `DATABASE_URL`, `API_URL`, `NEXT_PUBLIC_API_URL`. No hard-coded secrets.
- `README.md` — setup, demo walkthrough, deployment notes (free-tier friendly), and known limitations
- `DECISIONS.md` — every judgment call you made
- A final report: what was built, what was cut and why, test output

---

## The bar

A real barbershop could try this tomorrow. The queue itself — the number changing, customers moving, "you're next" arriving — is the product's visual interest. Don't decorate the queue; make the queue beautiful.

