# Plan — owners, operators, and the navigation pass

Source of truth for this stretch of work. Check items off as they land.
Judgment calls get written up in `DECISIONS.md` as they are made.

## Where things stand

Phases 0–6, 8, 10, 11 and 12 are done. Phase 9 (the edge drawer) was
overtaken by the **Paper** redesign, which replaced the whole dashboard frame
with a sidebar; see phase 10 and `DECISIONS.md` "Direction — Paper". Phase 7
(documentation) has been done piecemeal as screens changed. Of the original
audit only multi-seat queues remains, in the backlog.

Migrations run to **00009**: 00005 records a customer's presence on their
entry, 00006 flags entries added at the counter as walk-ins, 00007 adds the
business settings (hold time, pause note, archiving, owner name), 00008
holds push subscriptions and 00009 records when service actually began.

The names toggle is live end to end: default off, settable per queue, and it
changes what staff receive on the dashboard, in history and over the socket —
on the next frame, not the next sign-in.

The gap phase 1 opened — a recovery code the web app dropped on the floor — is
closed by 2.5. Owners created before that screen existed still have no code they
have ever seen; their bookmarked dashboard link is their way back in, and
redeeming is the only way to mint them a fresh one.

Running the stack locally (two repositories side by side):

- Postgres: `docker compose up -d db` in `Qless-backend` — container `qless-db`,
  host port **5433**.
- API: `go run ./cmd/server` in `Qless-backend` on **:8080**, with
  `ALLOWED_ORIGIN` naming the web app's origin — CORS and the socket handshake
  both check it.
- Web: `npm run dev` in `Qless` on **:3000**. Check whether one is already
  running before starting another — two `next dev` processes share `.next` and
  will fight over the build cache.
- Checks: `npx tsc --noEmit` and `npx eslint` in `Qless`; `go test ./...` in
  `Qless-backend`.
- Branches at the time of writing: web `redesign/paper`, API
  `feature/presence`.
- Push is optional locally: `go run ./cmd/vapid` prints a key pair for the
  API's `.env`; without one the pass keeps its in-page nudge.

---

## Goal

Give Qless a single anonymous owner who can hold several queues, delegate the
counter to named operators without handing over control of the business, and get
back in on a new device — while fixing the navigation dead ends and the dashboard
theme bug that existed when this was written.

## Context

**This section is the starting point, not the current state.** It describes the
codebase as it stood before phase 1, which is what the plan was written against;
every line below has since been changed by the work in it. Kept in the past
tense it belongs in, because a plan that erases what it started from cannot be
read back.

What was here when this began:

- `queues.owner_token_hash` — one token per queue, SHA-256, set at create
  (`api/migrations/00001_init.sql:17`). This is the whole of "who is this" today.
- `requireOwner` — resolves the queue, reads the bearer token, compares hashes
  (`api/internal/api/api.go:67`). Every operator handler calls it directly; there
  is deliberately no middleware to forget.
- Realtime pre-renders **two** frames per event, public and operator, and picks
  one per connection by audience (`api/internal/api/events.go:52`,
  `api/internal/realtime/hub.go:32`). The names toggle becomes a third frame.
- Owner tokens live in `localStorage` as `qless.owner.<queueId>`
  (`web/lib/session.ts:23`) with nothing enumerating them — this is why there is
  no "my queues" today.
- `[data-surface="paper"]` hardcodes light values regardless of `data-theme`
  (`web/app/globals.css:78`), so the dashboard theme toggle currently does nothing.
- Customer name limit is 60 chars (`api/internal/api/customer.go:47`); queue name
  80 (`api/internal/api/queues.go:25`).

## Approach

**Two credential layers, not one.** An owner holds a recovery code; an operator
holds an access code. Neither is a bearer credential on every request — both are
redeemed once for a session token, and the session tokens live in an
`access_tokens` table (many rows per principal). That table is what makes
recovery non-destructive: a new device gets a new row, and the counter tablet's
row is untouched. "Sign out other devices" becomes a delete, not a re-key.

**Queue id is never a credential.** It says *which* queue. The code the person
types says *who they are*, and the server answers with their role and their
queues. A queue id in a URL grants nothing on its own.

**Services are deferred, not designed out.** Queues hang off the owner now.
Adding services later is one table, one nullable `service_id` on `queues`, and
one screen — no change to how access is checked, no migration of existing rows.
The authorization helpers must therefore never assume owner→queue is the only
path to a queue.

**Names are a per-queue setting.** Owners always see names. Operators see them
only if the queue says so, defaulting to off. A clinic keeps names off; a
barbershop turns them on.

### Permissions

| Action | Owner | Operator |
| --- | --- | --- |
| Serve next, skip, call specific | yes | yes |
| Pause, resume | yes | yes |
| Close, reset | yes | no |
| Queue settings, create queue | yes | no |
| Manage operators | yes | no |
| See customer names | always | per-queue toggle |
| Recover access | yes | no — owner re-issues |

---

## Steps

### Phase 0 — standalone UX fixes

No dependency on the identity work. The theme item is a live bug.

- [x] **0.1 Dashboard dark mode.** Done by *removing* the base
      `[data-surface="paper"]` block rather than adding a dark variant: in dark
      mode the dashboard now defines nothing and inherits the shell, which is
      what "like the rest of the app" actually means. The old palette moved to
      `[data-theme="light"] [data-surface="paper"]`. QrCode needed no work — it
      was already hardcoded `#111` on `#ffffff` and theme-independent by design.
- [x] **0.2 Customer view opens in a new tab**, `rel="noopener noreferrer"`.
      Markup verified; the in-app browser pane collapses `target="_blank"` into a
      same-tab navigation even for a plain anchor, so the new tab itself could not
      be proven here — worth one look in a real browser.
- [x] **0.3 Back links.** Print sheet → dashboard, shown only to a browser
      holding that queue's owner token, since the print page is public. `/create`
      already had a working home link via the wordmark, so nothing was added
      there. The customer page's "Qless pass" label is now the link home.
- [x] **0.4 Hero ticket 3D hover** — `web/components/HeroTicketStage.tsx`. Mouse
      tilts on two axes and lifts; click or tap turns it over onto the reel face.
      `id="hero-ticket"` moved to the untransformed outer frame so the reel still
      measures a clean rect, and the effect stays disarmed while `data-reel` is
      set. Reduced motion is implemented but was not runtime-verified — the
      browser pane cannot emulate the media query.
- [x] **0.5 Verify phase 0.** Dashboard dark and light confirmed by computed
      tokens and screenshots; print sheet stays warm in dark theme; tilt verified
      per edge and ignored for touch pointers; flip toggles both ways; no
      horizontal overflow at 320/375/1440.
- [x] **0.6 Small-screen follow-ups** (from review). The hero ticket stretched to
      the full column below `lg` — 852px at a 900px viewport — because
      `lg:w-[360px]` only applied from 1024 up. Capped with `max-w-[360px]`,
      which also aligns it with the reel's own `min(360px, 100vw - 48px)` card so
      the load sequence lands on a slot its own size. Both nav bars now drop the
      theme toggle's label below `sm` and keep the mark: on the dashboard four
      labelled controls were wrapping inside their own pill.

### Phase 1 — identity backend

- [x] **1.1 Migration `00002`.** `owners`, `access_tokens`, the `principal_type`
      enum, `queues.owner_id` and `queues.show_names_to_operators` (default
      false); `owner_token_hash` rehomed and dropped. Backfill is one owner per
      **distinct token**, not per queue — two queues sharing a token hash were
      already one holder, and it is the only reading that survives the unique
      constraint, which the test database proved by holding dozens of duplicates
      (the fixture derived its token from the test name). Verified on the dev
      database: all six live queues kept exactly the token hash they had.
      `access_tokens` carries a nullable `owner_id` FK rather than a polymorphic
      `principal_id`; 00003 adds `operator_id` beside it.
- [x] **1.2 Actor resolution.** `Store.ResolveActor` looks the session token up
      in `access_tokens` and returns `queue.Actor{Type, ID, OwnerID}`, touching
      `last_seen_at` at most once every five minutes in the same statement.
      `requireQueueAccess` covers what the permission table shares with
      operators — `serveNext` and `listEntries` moved onto it — and
      `requireOwner` narrows to the owner. `AuthorizeQueue` switches on actor
      type rather than joining `owner_id`, so operators are a second answer
      rather than a rewrite. The socket handshake resolves the same way.
- [x] **1.3 Create-queue attaches to an owner.** A presented owner token adds the
      queue to that business; no token mints an owner and returns the recovery
      code once. An **unknown** token is refused rather than treated as a new
      business — quietly minting a second owner would scatter one person's queues
      with no way to merge them. Owner, first token and queue are written in one
      transaction, with slug retries wrapping the whole of it.
- [x] **1.4 `POST /api/access/redeem`.** Returns role, session token and queues;
      the code that matched decides the role. Rotation is **staged**: the
      replacement is returned but the redeemed code keeps working until
      `POST /api/access/recovery-code/acknowledge`, because rotating in place
      turns a lost response into a permanent lockout. Redeeming a staged code
      settles it as current, so whichever code the owner actually received wins.
- [x] **1.5 `GET /api/me/queues`.** Role and queues for any actor.
- [x] **1.6 `POST /api/sessions/revoke-others`.** Owner only, keeps the calling
      device, and touches only that owner's own sessions — replacing your phone
      should not sign out the staff. Never run as part of recovery.
- [x] **1.7 Rate limiting and enumeration.** Bucket limiter keyed by address and
      by code prefix, plus a new `httpx.Lockout` (20 failures in 10 minutes → 15
      minutes closed) keyed by address only, so nobody can lock a legitimate
      owner out by hammering their prefix. Every failure — unknown, rotated
      away, empty — takes one path and returns one body; the code is
      deliberately **not** shape-validated first, since answering a malformed
      code without a round trip is itself a measurable difference.
- [x] **1.8 Go tests.** Recovery leaves other sessions alone; revoke-others
      signs out the rest and keeps the caller; redeem is rate limited by address
      and by prefix, and answers every bad code identically; an acknowledged code
      cannot be replayed while an unacknowledged one still works; another
      owner's valid token is now among the credentials every operator route must
      reject. Plus the migration walked end to end on a scratch database —
      legacy token still resolves and still opens its queue — and unit tests for
      the lockout and for code normalisation.

### Phase 2 — my queues and code entry

- [x] **2.1 `/enter`** — one field, routed by what the server says the code was.
      An owner's code is rotated on redemption, so the replacement is put in
      front of them before they go anywhere and `POST
      /api/access/recovery-code/acknowledge` is the last thing that runs —
      until it does, their old code still works. Reachable from the landing
      hero ("I have a code"), from `/queues` when signed out, and from a
      dashboard opened without a session. An operator covering exactly one
      queue lands on that dashboard; everyone else lands on `/queues`.
- [x] **2.2 `/queues`** — server-backed via `GET /api/me/queues`. Four states:
      signed out, no queues yet (owner), assigned to nothing (operator, plain
      "ask your manager"), and the list. A 401 clears the dead session rather
      than reporting an error. **The "operators" entry is not here** — `/operators`
      does not exist until 4.4, and a nav item that 404s is worse than one that
      arrives with its screen. Landing nav gains "My queues" via `MyQueuesLink`,
      client-rendered on the `useIsClient` pattern.
- [x] **2.3 Wordmark goes to the right home.** Signed in → `/queues`, signed out
      → `/`, resolved after hydration so the two never disagree. Both roles share
      that home; an operator's list is simply shorter.
- [x] **2.4 `?k=` stripped** with `history.replaceState` — which Next wires into
      its own router, so no navigation and no remount. Only after the session
      write is read back successfully: in a private window the write can be
      silently dropped, and the URL is the last other copy of that token.
- [x] **2.5 Recovery code screen at create** — ticket stock, copy + download,
      and a checkbox that gates the continue button. Shared with 2.1 as
      `components/RecoveryCode.tsx`. Nothing is acknowledged to the server here:
      acknowledgement promotes a *staged* code, and a code issued at create has
      nothing behind it to promote.
- [x] **2.6 Session model in the browser** (fell out of the above). One
      `qless.session.token` replaces the per-queue `qless.owner.<id>` keys. The
      old keys are still read, never written, and a dashboard opened with one
      promotes it to a session — so nobody is signed out by this change.
      `createQueue` now sends the session, so a second queue joins the business
      instead of starting a new one, and `QueueReady` no longer hands out a
      `?k=` link or claims that the link is the credential.

### Phase 3 — operator surface (PROMPT.md milestone 3)

The actions themselves, none of which exist yet — the store has only `Join`,
`MyEntry`, `Leave`, `ServeNext`, `SetStatus`. Written after phase 1 so each
endpoint picks `requireOwner` or `requireQueueAccess` once, rather than being
written against the old single-token check and then revisited.

- [x] **3.1 Per-entry actions** — `serve`, `attend`, `skip`. Serving one
      customer attends whoever was at the counter, because there is one counter
      and a unique index enforces it; nobody else is reordered. `attend` accepts
      a waiting entry as well as the serving one, so an operator who dealt with
      someone without calling them first can say so. Acting on a finished entry
      is a stale dashboard rather than an error, and answers 409
      `entry_not_active`.
- [x] **3.2 Queue lifecycle** — `pause`, `resume` (shared with operators),
      `close`, `reset` (owner only).
- [x] **3.3 Settings** — `PATCH /api/queues/:id`, owner only. Renaming does
      **not** change the slug: that address is printed on a sheet taped to a
      door. `showNamesToOperators` is settable here; phase 5 makes it change a
      payload.
- [x] **3.4 History** — `GET /api/queues/:id/history`, newest first, capped at
      200 and behind the same check as the dashboard, since it carries names.
- [x] **3.5 Record who acted.** Landed with 00004, as planned. `acted_by_type`
      plus `acted_by_operator_id` on `queue_entries`, stamped by serve, attend,
      skip and reset. It records **who caused the current status**, not a full
      audit trail: a customer called by an operator and finished by the owner
      reads "attended by the owner", which is what the row's own status says
      happened. Rows written before this are unambiguous anyway — the owner was
      the only principal that could exist.
- [x] **3.6 Dashboard controls** — per-row Serve and Skip, a "finish without
      calling anyone" action at the counter, a queue-controls panel, and
      `/dashboard/[id]/settings` and `/dashboard/[id]/history` screens.
      Confirmations on skip, close and reset; none on serve next or pause.
      `ConfirmDialog` needed repairing first: it was styled with `text-ink` and
      `bg-urgent`, tokens the redesign removed, so its classes emitted nothing
      and `destructive` was a no-op — on a dialog the customer page was already
      using for "leave queue".
- [x] **3.7 Go tests** — serve specific leaves positions alone, a second serve
      attends the first, skip preserves the record and allows a rejoin, stale
      actions 409, another queue's entry is unreachable, reset clears and
      restarts while keeping history, pause and close block joins reversibly,
      settings update only what was sent, renaming keeps the slug, history holds
      only finished entries. Capacity and paused/closed joins were already
      covered in `storage_test.go`. Every new endpoint is in the
      missing-or-wrong-token table.
- [x] **3.8 Migration `00003`** (unplanned, forced by 3.2). `UNIQUE (queue_id,
      number)` spanned every entry, which makes "reset restarts numbering at 1"
      and "history is preserved" mutually exclusive — the first customer of the
      new day collided with the cleared number 1 from the old one. Replaced with
      a partial unique index over active entries only, which is the invariant
      that actually matters and matches the two indexes already in this schema.
      **Phase 4's operators migration is therefore 00004.**

### Phase 4 — operators (roles and roster)

- [x] **4.1 Migration `00004`** (was 00003; 3.8 took that number) — `operators`,
      `operator_queues`, the `operator_id` half of `access_tokens`' check
      constraint (now `principal_matches_type`, which pins a token to exactly
      one principal), and the acting-principal columns that close **3.5**.
      Revoking is a soft delete so history keeps resolving to a name.
- [x] **4.2 Operator CRUD** — list, create, rename, reassign, regenerate code,
      revoke, all owner-only and all scoped by `owner_id` as well as by id, so
      one owner cannot reach another's staff by guessing. Codes are reusable and
      revocable; regenerating retires the old one at once, with none of the
      owner's staged rotation — the person handing over the code is standing
      right there.
- [x] **4.3 Permission table enforced.** Phase 1's split meant no handler
      changed: filling in `AuthorizeQueue`'s operator branch was the whole of
      it. Verified endpoint by endpoint in a table test rather than assumed.
- [x] **4.4 `/operators` roster** — add, assign by toggling queue chips,
      regenerate, revoke. Access codes shown once on ticket stock; revoked staff
      listed separately with why they are still there.
- [x] **4.5 Role-aware dashboard** — an operator sees Pause, the counter, the
      waiting list, History and the share/QR panel; Close, Clear and Settings
      are gone. A queue switcher appears above the counter for anyone covering
      more than one queue. The role comes from storage, which cannot drift — a
      principal's type never changes — and decides only what is *drawn*.
- [x] **4.6 Go tests** — a table of what an operator may and may not do, another
      owner's staff unreachable, assignment to another owner's queue silently
      dropped, revocation killing sessions and the code on the next request,
      regeneration retiring the old code, unassignment leaving a valid session
      with an empty list, and history naming an operator after they are revoked.

### Phase 5 — the names toggle

- [x] **5.1 Third realtime frame.** `Audience` is now `Public` / `Owner` /
      `Staff` — `Operator` was the right name only while the owner was the only
      operator there was. `buildEvent` renders three frames; with names on the
      staff frame *is* the owner frame, same bytes, so the only one ever
      constructed to leave something out is the one that has to be.
- [x] **5.2 Same split on the HTTP surfaces** — the dashboard view **and
      history**, which was the easy one to miss: it carries names too, and staff
      who had just been kept from them on the counter could have read them all
      on the next screen. One `maySeeNames(queue, actor)` answers for all three.
- [x] **5.3 Owner setting wired up.** The checkbox shipped in 3.3; it now
      changes payloads. It takes effect on the **next frame**, not the next
      sign-in — an owner who realises staff should not be seeing names does not
      have to get everyone to sign out.
- [x] **5.4 Leak test** — with the toggle off, the dashboard response, the
      history response and the staff socket frame are each asserted to contain
      no customer name. Plus the inverses, so a change that hides names from
      everybody cannot pass as a fix: the owner always sees them, staff see them
      once the toggle is on, and a socket opened *before* the toggle flipped
      stops receiving names on its very next frame.

### Phase 6 — polish (PROMPT.md milestone 4)

Milestone 4 as written, grown by the screens this plan adds.

- [x] **6.1 Display mode** — public, numbers only, unaffected by roles.
      `/display/[slug]` built to the room-display panel in the design handoff:
      gold "NOW SERVING", the number at 264px, "UP NEXT" carrying the following
      three, the business name and waiting count, and a 300px QR. It holds the
      dark shell in either theme, like the print sheet holds paper — nobody
      picks a theme on a screen bolted to a wall. `usePublicQueue` is a new
      hook rather than `useCustomerQueue` with the identity parts unused: a
      board has no token, no entry and no actions. Verified live in the browser
      — a customer joining and an operator serving both move it without a
      reload, and the frames it receives carry no `customerName` key at all.
- [x] **6.2 QR and print sheet.** The recovery code does **not** go on the print
      sheet — that item was written before the code became single-use and
      hash-only, and it is now impossible twice over: nothing can reproduce the
      code after it is issued, and `/print/[slug]` is a public page designed to
      be photographed and taped to a door. The intent survives, moved to the one
      moment the code is known: `components/RecoveryCode.tsx` gains a **print**
      affordance beside its existing copy and download, already behind the
      forced acknowledgement. The paper behind the till gets made at create and
      recovery time. **Settled — do not revisit.**
      Done: a **Print** button beside Copy and Download, and a sheet designed
      for paper rather than a screenshot of a screen — printed masthead, issue
      date, business name, the code, and where to type it. The screen chrome is
      dropped with Tailwind's `print:` variant and the tokens are retoned under
      `html:has(.recovery-sheet)`, because the dark shell the sheet sits in
      belongs to the page and not to the component. Print-previewed rather than
      assumed, by flipping the `@media print` blocks to `screen` in the live
      CSSOM and screenshotting at A4 width. Also: the print sheet holds the
      QR's square open before the origin resolves, so it no longer relays
      itself out under the operator; `downloadQrPng` now re-draws onto a white
      field with a four-module quiet zone, since the exported bitmap had none
      and the wrapper's padding does not travel with it; and the dashboard's
      share panel gained the **Display board** link that 6.1 needs to be
      reachable at all.
- [x] **6.3 Notifications** — customer-side, unaffected by roles.
      `useTurnNotifications` fires once each at three away, one away and your
      turn, driven by the `proximityOf` ladder the page already derives, and
      only while the tab is hidden — a notification on top of the screen that
      is already shouting is a second alert for something nobody missed. The
      opt-in replaces the reassurance row's old promise, which the app had
      never actually kept. Verified end to end against a stubbed Notification
      with the document hidden: one alert per rung, correct copy, none while
      visible. The real permission prompt could not be driven — the automated
      browser has notifications blocked at the profile level — but that path
      was verified in the other direction: with permission genuinely denied the
      page says so plainly and everything on it still works.
- [x] **6.4 Landing page** — including the "my queues" and "I have a code"
      entries from 2.1 and 2.2. Reviewed against milestone 4 rather than
      rebuilt, and it already carries everything that description asks for:
      the hero line, one paragraph of support copy, the Create-a-queue CTA and
      the five-step flow, plus the two entries phase 2 added. Two things were
      wrong on inspection and are fixed: a commented-out "For business" nav
      link left behind from the handoff, and a `how-it-works` section whose
      only content is an ordered list — a heading to the eye, nothing at all to
      a screen reader jumping between landmarks. It now carries a visually
      hidden `<h2>`. The reel and the smooth scroll were both re-checked
      against `prefers-reduced-motion` and both already opt out.
- [x] **6.5 Empty, error and loading states** for the new screens too: invalid
      code, rate limited, revoked operator, assigned to no queues. The first
      and last shipped in phase 2. **Rate limited** is now its own state on
      `/enter` rather than a field error — nothing about what they typed was
      rejected, and saying so under the input sends them off checking a code
      that may be perfectly good. **Revoked** turned out to be two states, not
      one: the server answers 401 both to "this token is unknown" and to "this
      queue is not yours", deliberately, so `classifyUnauthorized` asks
      `/api/me/queues` — which is about the session and not about a queue — and
      the screens say either "this device has been signed out" or "not your
      queue". Only the first throws the session away. The old code cleared it
      for both, which signed a working operator out of their own business for
      opening `/operators`. Verified in the browser through a real operator:
      unassigned mid-session → "not your queue", session kept; revoked
      mid-session → signed out, session cleared, socket stopped rather than
      retrying eight times into a closed door.
- [x] **6.6 Accessibility and responsive passes**, now covering `/enter`,
      `/queues` and `/operators` as well.
      **Contrast was the real work.** Audited every token pair the product
      actually renders, and the muted tier failed AA almost everywhere it was
      used: `muted` on the light shell at 2.96:1, `faint` at 2.06:1, the
      ticket's `paper-muted` at 3.37:1 on the customer's own screen in both
      themes. Fifteen token values moved; the ladder strong → dim → muted →
      faint is intact and every combination that carries small text now clears
      4.5:1, every large one 3:1, with the script kept honest by re-running it
      against the new values. `faint` stops being a small-text tone — the two
      microcopy lines and the input placeholder moved to `muted` — and becomes
      the control-boundary tone, which fixes a separate 1.4.11 failure: ghost
      buttons and inputs were outlined in `shell-line` at 1.4:1 against the
      shell, a boundary nobody could see. **The signal colour moved** from
      `#E8552F` to `#CE4B2A`; see DECISIONS for why nothing else could.
      Also: `main` landmarks and a single `h1` on every screen (the turn
      screen, the dashboard, the board and the print sheet each had none); a
      polite live region on the customer pass, mounted outside the four state
      screens so escalation is announced rather than swallowed; the input's
      `focus:outline-none` removed so keyboard focus is visible on fields as
      well as links; and the landing's smooth scroll now moves the focus point
      it was cancelling. Verified: no horizontal scroll on any of the eleven
      screens at 320/375/390/768/1024/1440, and a DOM audit across all of them
      reporting zero unlabelled controls, nameless links or duplicate ids.

### Phase 7 — documentation

Written last on purpose: phases 8 and 9 changed the screens these documents
describe. The divergences below were found by reading the code and the live
database on 2026-08-15, not from memory — every line is a claim a document makes
that the product no longer honours. Anyone doing this phase should be
transcribing, not remembering.

**A rule for the phase.** Where a document and the code disagree, the code wins
unless the code is wrong. Two of the items below are the second case and are
flagged as such: they are gaps to close in code, not sentences to rewrite.

- [ ] **7.1 `PROMPT.md` — the spec contradicts the product in six places.**
      - *Frontend routes* list eight; there are eleven. Missing: `/enter`,
        `/queues`, `/operators`.
      - *API* lists eleven endpoints; there are twenty-seven. Missing entirely:
        the whole of `/api/access/*`, `/api/me/queues`,
        `/api/sessions/revoke-others`, all five `/api/operators*` routes,
        `GET /api/queues/{key}/entries` — which is the dashboard's own payload
        and was never in the spec — and `/healthz`.
      - *Data model* still shows `queues.owner_token_hash`, which 00002 dropped.
        It does not have `owner_id`, `show_names_to_operators`, the
        `acted_by_type` / `acted_by_operator_id` columns on `queue_entries`, or
        any of the four tables added since: `owners`, `access_tokens`,
        `operators`, `operator_queues`.
      - *Invariants* claim `UNIQUE (queue_id, number)`. 00003 replaced it with
        `one_active_entry_per_number`, partial over WAITING and SERVING —
        without which "reset restarts numbering" and "history is preserved"
        cannot both be true. The one-SERVING-per-queue index is unchanged and
        should stay stated, because phase 9's backlog turns on it.
      - *Identity* describes the owner token as a bearer credential carried in
        `/dashboard/{id}?k={token}`, and asks for a "Revoke & regenerate link".
        Both were superseded in phase 1: codes are redeemed for sessions, `?k=`
        survives only as a device-to-device handoff that the receiving browser
        strips, and the revoke story is `POST /api/sessions/revoke-others`.
        "Operator accounts (P2 — optional)" was built, differently.
      - *Out of scope* still forbids `staff roles`, which phase 4 built.
        `multi-location` stays out and should now say it is designed around
        rather than merely unbuilt.
- [ ] **7.2 `DECISIONS.md` — two of the five named entries are missing.** "Two
      credential layers" and "queue id is not a credential" are written. Not
      written as standalone entries, though the substance is scattered through
      phases 1 and 5: **non-destructive recovery**, **names as a per-queue
      setting**, **services deferred**. Write those three. Do not rewrite the
      other two.
- [ ] **7.3 `README.md` — the most wrong document in the repository.** Not in
      the original plan; it is in PROMPT.md's deliverables and it is stale
      enough to mislead. Its Status table says milestones 1 and 2 of 4 are
      complete and 3 and 4 are not; all four are, plus five phases beyond them.
      Also: *Layout* lists three hooks where there are seven and omits
      `/enter`, `/queues`, `/operators` and `/display` from the route summary.
      Everything below "How identity works" is accurate and should be left
      alone — it was written as those phases landed, which is exactly why.
- [ ] **7.4 Playwright.** Nothing exists: no dependency, no config, no `e2e/`.
      PROMPT.md asks for five scenarios and this plan added three; the union is
      what to build. Note that `make test` runs backend tests only, and that
      the web checks (`tsc`, `eslint`) are not wired into any make target —
      decide whether `make test` should cover all three.
- [ ] **7.5 Two gaps where the code is wrong, not the document.**
      - The permissions table in this plan grants an owner "create/**delete**
        queue". There is no delete-queue endpoint and never has been. Either
        build it or strike the word — but a permissions table that promises a
        capability is the worst of the three options.
      - `PROMPT.md` requires "no hard-coded secrets" and `.env.example` covers
        every variable the app reads. Confirmed accurate; no action, recorded
        so the next audit does not re-derive it.

### Phase 8 — dashboard navigation

- [x] **8.1 Dashboard sub-nav.** Counter / History / Settings as a row under the
      dashboard header, on all three routes, replacing the two links that were
      buried in the queue-controls panel. Settings and History are navigation and
      were sitting inside a panel of actions, two buttons from "Clear queue";
      the sub-pages also had no way back except a ghost button at the bottom.
      Not a sidebar: it eats the horizontal space the counter needs on a tablet,
      and the direction is "calm and operational, not SaaS-dashboard".
      Done by extracting `DashboardChrome` — the header, the tab row and
      `<main>` — and having each screen's own client component render it, rather
      than a route layout: what changes in that bar (the queue's name, its
      status, the live dot) is the page's state, and a layout sits above the page
      it would have to read it from. History and Settings stop hand-rolling a
      smaller header and their server pages are four lines each. The queue name
      is now the single `h1` for all three sections, so "History." and
      "Settings." are h2s beneath it, and the duplicate name label and the
      bottom "Back to dashboard" buttons are gone. Settings is drawn only for an
      owner, read the way the dashboard already reads role.
- [x] **8.2 Destructive actions behind a disclosure.** Pause stays visible — it
      is the several-times-a-day control. Close and Clear move behind one
      deliberate step, because a confirm dialog is weak protection against a
      fat-finger on a counter tablet: the dialog gets tapped out of habit too.
      A disclosure (`aria-expanded` + a toggle) rather than a menu, since
      `aria-haspopup="menu"` drags in roving tabindex, arrow keys and typeahead,
      and the fixed stack allows Radix for Dialog only.
      Inline rather than floating, so there is no absolute positioning, no
      click-outside handler and nothing to clip; the region stays in the DOM and
      is hidden, so `aria-controls` always resolves. Escape closes it and
      returns focus to the toggle, handled on the section rather than on the
      document. The two confirmations are untouched. Monochrome throughout.
- [x] **8.3 Queue switcher for owners.** It rendered only for operators, so an
      owner with three queues had to leave via the wordmark to reach another. Now
      rendered for everyone; it already hides itself below two queues, so a
      single-queue owner sees exactly what they saw before. It shares the tab
      row's band rather than taking one of its own.

### Phase 9 — the edge drawer

**Superseded by phase 10.** The drawer was never built; the Paper redesign
answered the same two problems with a pinned sidebar and a queue switcher
popover. Kept for the reasoning. Supersedes 8.1's tab row. Phase 8 was not wasted: it created `DashboardChrome`
as the one place all dashboard screens get their frame, and this replaces the
band inside that component instead of touching five screens.

The problem it solves is two problems. The header carries six controls and
wraps into three bands of chrome at tablet width before any queue is visible;
and the queue switcher is a row of pills, which fails at four queues and was
asked to hold ten.

- [ ] **9.1 The drawer.** A shutter pulled from the left edge by a flush,
      unpadded ink tab that travels with the panel's edge, so the handle is
      always attached to the thing it moves. Inline at `lg` and above, where it
      is pinned open and the content sits beside it; an overlaying card with a
      shadow below that, closed by default. Non-modal — it is navigation, not a
      decision — so a disclosure with `aria-expanded`/`aria-controls` rather
      than a dialog, matching 8.2. Escape closes it and returns focus to the
      tab. Elevation on the dark shell comes from `shell-soft` and a lifted
      hairline; a shadow on `#111` is invisible.
- [ ] **9.2 Five destinations, two groups.** *This queue* — Counter, History,
      Settings (owner). *Your business* — Queues, Operators (owner). The queue
      group is omitted where there is no current queue. **Queues keeps its own
      page** rather than listing inline, which is what makes the drawer scale:
      five fixed entries whether the owner runs one queue or fifty, no scroll
      and no filter, ever.
- [ ] **9.3 The header sheds what the drawer now holds.** The theme toggle and
      the customer-view link move into the drawer's footer — both are
      occasional, and removing them is what finally leaves the bar with four
      things, two of which are state rather than controls.
- [ ] **9.4 `/queues` and `/operators` adopt the chrome**, so the menu is on
      every screen it can reach. Their URLs do **not** move under `/dashboard/`:
      the goal is the menu, which the chrome delivers on its own, and this
      product's re-entry story leans on bookmarked links. A dashboard here is
      always *a queue's* dashboard, so a queue-less screen under that prefix
      would be the odd one out anyway.
- [ ] **9.5 `QueueSwitcher` goes.** A pill row for ten queues is the thing this
      phase exists to fix, and the Queues page now does its job. It costs an
      owner with two queues one extra step to switch; if that bites, the answer
      is a recent-queues line in the drawer, not the pills back.

---

### Phase 10 — Paper

The redesign, on web branch `redesign/paper` and API branch
`feature/presence`. Six steps, each verified against the live API before the
next began.

- [x] **10.1 Tokens and type.** Light-first `:root`, `[data-theme="dark"]`
      inversion, "system" preference resolved before paint. Geist and Geist
      Mono via `next/font`; nothing heavier than 500.
- [x] **10.2 Primitives.** Pill buttons, 44px fields with a halo focus, the
      ticket with hairline notches, the mark (a stub tile with a Q punched
      out), theme toggle, notices, dialogs.
- [x] **10.3 Navigation.** `DashboardChrome` becomes a sidebar answering
      three questions top to bottom: which queue (switcher), what am I doing
      (Counter, History, Share, Settings), who am I (personal menu). The
      content column is centred at 1680px; the top row holds the finder and the
      live/status dots. `/queues` and `/operators` share the chrome.
- [x] **10.4 Counter, settings, history.** The counter puts the number being
      called in vermilion with presence beside it; rows carry "Call now" and a
      per-row menu. Settings is two columns with a switch. History is a
      full-width table — sortable by number, name and time, filterable by day,
      outcome and who served, paginated at 25, exportable as CSV.
- [x] **10.5 Customer pass, join, create, enter.** Presence is a flow: on my
      way, then here; when called, here or a two-minute hold. Recorded on the
      entry by the API (00005) and shown to the counter as a tag.
- [x] **10.6 Landing, display, print, wake lock, manifest.** The reel and the
      hero ticket stay. The display board is retuned with a larger QR column;
      the counter and the board hold a wake lock.
- [x] **10.7 Follow-ups from the first review.** Walk-ins (`POST
      /api/queues/{key}/entries`, 00006) for a person with no phone. Skipping
      keeps the number and lists it under "Skipped recently" with one-tap
      recall; after the window the call is refused with `recall_expired`.
      Creating a queue from the dashboard or the queues list happens in a
      dialog. History fetches up to 1000 rows.

### Phase 11 — the rest of the audit

Everything the product review listed as missing, except multi-seat queues,
which stays in the backlog because it changes the socket contract.

- [x] **11.1 Hold time** (00007 `hold_minutes`, default 10, 0–120). One
      business setting with three jobs: the counter suggests a skip once a
      called person has been silent that long (a two-minute request from the
      pass adds two), a skipped number is recallable for that long, and the
      pass tells the customer the figure. Zero makes a skip final and hides
      the recall list. Replaces the fixed 30-minute window.
- [x] **11.2 Pause with a note** (`pause_note`). `POST …/pause` takes an
      optional `{note}`; it shows on the counter's status line, the join
      page, and the wall, and clears on resume.
- [x] **11.3 Archive a queue** (`archived_at`, `POST …/archive|unarchive`,
      owner). Archiving closes the queue, hides it from `/api/me/queues` and
      refuses joins and reopening; history stays and the queues page lists it
      under "Archived" with Restore. Settings has the action.
- [x] **11.4 Live queues list.** `/api/me/queues` returns cards with
      `servingNumber` and `waitingCount`, plus `archived` and the owner's
      `displayName`.
- [x] **11.5 Owner display name** (`owners.display_name`, `PATCH /api/me`,
      `ownerName` on create). The personal menu shows it and offers the
      dialog; history carries `ownerName` so staff see a name instead of
      "the owner".
- [x] **11.6 Live estimate.** `MeasuredService` averages the last ten real
      start-to-finish times from the past twelve hours; once there are five
      the public state's `serviceMinutes` and every estimate use it. The
      operator view carries `measured` and settings says which figure is in
      use.
- [x] **11.7 New-day prompt.** The operator view carries `lastActivityAt`;
      an owner opening an idle dashboard twelve hours later is asked whether
      to start again at 1. Dismissable per session; never automatic.
- [x] **11.8 Chime on the wall.** Two synthesised notes when the serving
      number changes, armed by a tap on the board and remembered per device.
- [x] **11.9 Full queue.** The join page says it updates on its own and the
      button returns when a place frees.
- [x] **11.10 Push notifications** (00008, `GET /api/push/key`,
      `POST|DELETE /api/queues/{key}/push`). A service worker at `/sw.js`, a
      subscription taken when the pass's opt-in is granted, and the server
      sending each rung of the ladder once per phone after every frame.
      Optional per deployment: VAPID keys from `go run ./cmd/vapid`.
- [x] **11.11 When service actually begins** (00009 `served_at`). The call
      and the service are different moments, and the gap is the customer
      walking back. `served_at` is inferred — already "here" when called,
      "here" said at the counter, recalled from a skip — or set by one tap
      (`POST …/entries/{id}/start`). The measured estimate runs from it, the
      counter shows "Serving for" once it is set and the overdue nudge stops,
      and history gains Arrived and Served columns plus an average service
      figure. The operator view carries `arrival`, the average time to turn
      up once called, which is what a hold time should be set against.

### Phase 12 — the counter's stages, and iPads

- [x] **12.1 One thing at a time.** The counter card offers only what its
      stage allows: Serve next when the counter is empty; Start serving or
      Skip and hold once somebody is called (Skip leads once the hold time
      has run out); Done once they are being served, with the hold kept as a
      quiet link. Serve next never has anyone to finish implicitly.
- [x] **12.2 Standing down.** Calling anybody while somebody is at the
      counter attends them if service had begun and skips-and-holds them if
      not, so a no-show is never written into history as served. Call now on
      the list and the recall list is disabled while somebody is being
      served, with the reason on hover and in a line under the list.
- [x] **12.3 Arrival on screen.** A fifth stat, "Arrive after call", and a
      line under the hold-time field that says the same figure and warns when
      the hold is shorter than it.
- [x] **12.4 iPad pass.** Reviewed at 744, 820, 1024 and 1180 wide. Under a
      coarse pointer, buttons grow to 44/40px, the finder, selects and sort
      headers to touch size, and every input reads at 16px so iOS does not
      zoom on focus; taps no longer wait for a double. The counter's two
      columns follow the content width through a container query, so a
      12.9-inch iPad held upright stacks them. The wall number is larger in
      portrait, the sound and live controls head the board where the code
      column stacks, and the chrome and board pad for the notch and home
      indicator (`viewport-fit=cover`).
- [x] **12.5 History as a working record.** Search by name or exact number,
      a Walk-ins entry in the outcome filter, and real pagination: ten rows a
      page by default with 25, 50 and 100 on offer, first / previous / next /
      last and the page count between. On a desktop the search leads the
      filter row with the filters at the right; on a tablet it takes a line
      of its own under the title.
- [x] **12.6 The counter's finder is parked.** Not rendered for now — a
      counter of a dozen people does not need one — and kept wired for the
      day a queue is long enough to.

---

## Backlog — not scheduled

Real features, deliberately not in the current plan. Each needs its own plan
before it is started.

- **Multi-seat queues.** Several service points — chairs, counters, exam rooms —
  drawing from one waiting line, so more than one customer is being served at
  once. This is not a UI change: `UNIQUE INDEX (queue_id) WHERE status =
  'SERVING'` (`api/migrations/00001_init.sql:34`) is the invariant the whole
  system rests on, and 3.1's "serving one customer attends whoever was at the
  counter" is only true because there is one counter. It needs a migration, a
  seat concept an operator can be bound to, a plural `servingNumber` on the
  public payload — which changes the socket contract, the customer's board and
  the display board's whole composition — and a divisor in `EstimateWait`
  (`api/internal/queue/estimate.go:25`), which otherwise quotes every customer
  N times their real wait. The obvious workaround, one queue per chair, does
  not work: it splits the waiting line, which is the entire point.
- **Deleting a queue.** The permissions table promised this from the first draft
  and no endpoint was ever written; 7.5 struck the word rather than leave a
  table making a promise. It is a real gap — an owner who mistypes a queue name
  at create is stuck with it, and `make seed` leaves one behind on every run.
  Not a `DELETE` though: entries hang off queues and history is the one thing
  this product never destroys, so it wants an archive flag that hides a queue
  from `/queues` and refuses new joins while leaving its history resolvable —
  closer to how revoking an operator works than to a delete.
- **Services.** Different queues for different things — a haircut and a beard
  trim off one roster. Designed for since phase 0: one table, one nullable
  `service_id` on `queues`, one screen, no change to how access is checked.
  Distinct from multi-seat, which is parallel capacity on *one* queue and is
  the harder of the two.

---

## Risks and open questions

**Risks**

- *The migration is the dangerous step.* It rehomes every existing owner token.
  If the backfill is wrong, live dashboard links break and there is no recovery
  code to fall back on. Needs a test that an existing token still works after
  migrating.
- *Redeem is a new front door.* It is the first endpoint where guessing wins
  something. Rate limiting and identical failure responses are not optional.
- *Names could leak to staff* through the realtime frame — three frames means
  three chances to send the wrong one. Guarded by 4.4.
- *Scope.* This is substantially more than the queue system PROMPT.md describes.
  Phase 0 is independent and can ship alone if you want to stop and reassess.

- *History attribution is time-sensitive.* See 3.5 — the acting principal has to
  be recorded from the moment operators exist. There is no retroactive fix.

**Settled**

- Operator access codes are **reusable**, revocable by the owner at any time —
  not single-use.
- Operators **can see the share and QR panel**, but cannot change settings.
- An operator assigned to no queues gets a plain **"ask your manager"** state,
  not an error.
- Services are deferred, designed for: one table, one nullable column, one
  screen to add later.
- Customer names are a per-queue setting, default off for operators.

