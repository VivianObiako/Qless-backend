# Decisions

Judgment calls made while building, and why. Newest section last.

## Milestone 1 — spine

**No accounts anywhere, including for operators.** Creating a queue returns an owner
token once; only its SHA-256 hash is stored. The dashboard URL carries it as `?k=`
so the link can be bookmarked or opened on a counter tablet. Anyone with the link
can operate the queue, which the create screen says plainly. `referrer: no-referrer`
is set app-wide so the token is never sent to another origin.

**`current_entry_id` dropped from the schema.** The customer being served is the one
entry with status `SERVING`, enforced by `CREATE UNIQUE INDEX … WHERE status =
'SERVING'`. One source of truth, and two operators racing to serve cannot both win —
the loser's transaction fails on the index rather than on a check we hoped the
application remembered.

**Duplicate prevention covers active entries only**, via a partial unique index on
`(queue_id, customer_token_hash) WHERE status IN ('WAITING','SERVING')`. This is what
lets a skipped or departed customer rejoin and take a fresh number. A blanket
uniqueness rule would have locked them out permanently.

**Waiting order is always by number ascending.** "People ahead" counts waiting entries
with a lower number. Serving a specific customer (milestone 3) pulls one person out
without reordering anyone else, so positions stay stable.

**Wait estimate: `peopleAhead × averageServiceMinutes`, ±20%, each end rounded to the
nearest 5, floored at 5 minutes.** Computed server-side so every device agrees. Zero
ahead returns no estimate at all — at that point "You're next" is the message, not a
duration. Rendered as `10–20 min` below 90 minutes and `1h 10m – 1h 50m` above.

**Public payloads carry numbers, never names.** `PublicState` exposes
`waitingNumbers: number[]`; a customer knows their own number from `/me` and derives
their position locally. Names appear only in the operator view, behind the owner
token. `TestPublicQueueStateContainsNoCustomerNames` guards this.

**Migrations live in `api/migrations/`, not the repo root.** `go:embed` cannot reach
above its own package, and embedding them makes the compiled server self-contained —
one binary to deploy, no migration directory to ship alongside it. They run on server
start as well as via `make migrate`.

**IDs are `string` in Go, not `uuid.UUID`.** Avoids a pgx codec dependency for no real
benefit; the API validates the shape before querying so malformed ids return 404
rather than a driver error.

**Postgres runs on host port 5433**, not 5432, so it cannot collide with a Postgres
already installed on the machine.

**shadcn/ui adopted selectively.** `components/ui/*` is registry code, left as shipped;
`components/*` is Qless product UI, hand-built against our tokens. The registry's
palette collided with ours — its `--color-accent` and `--color-muted` are neutral
surfaces, ours were the indigo brand and the secondary text colour — so the Qless
tokens were renamed to `brand` / `ink-muted` / `ink-faint` and shadcn's semantic
variables now point at our palette. Anything pulled from the registry inherits the
Qless look instead of importing its own.

**`sonner.tsx` adapted from the registry** to pin `theme="light"`. The app is
light-only and has no ThemeProvider, so the default `"system"` would have rendered
dark toasts over a light page.

**Reads from localStorage go through `useSyncExternalStore`**, not a read-in-effect.
The React compiler correctly flags the latter as a cascading render, and the store
approach also keeps tokens correct across tabs.

**Customer page refreshes on window focus** for this milestone. Milestone 2 replaces
the mechanism with a WebSocket, but the focus refresh stays afterwards as the resync
path after a dropped connection.

**Leave is in milestone 1**, not milestone 3 with the rest of the queue actions. The
customer view is required to show *Leave queue* prominently, and shipping a button
that does nothing was not an option.

## Design direction — "Ticket Pass"

The indigo-on-off-white system from milestone 1 was replaced wholesale by the
`design_handoff_qless_ui` bundle. Inter is superseded by **Instrument Serif**
(numerals, venue names, headlines) and **IBM Plex Mono** (everything else).

**Escalation is carried by inversion, not hue.** Vermilion `#E8552F` appears on
exactly two surfaces in the entire product: the "it's your turn" screen and step
05 of the landing page. Anywhere else it would stop meaning "your turn", so
errors, warnings and destructive actions are all monochrome — weight, outline
and inversion do that work instead.

**Proximity states are token scopes, not bespoke markup.** `data-surface="paper"`
(state 03) and `data-surface="signal"` (state 04) redefine `--shell`, `--strong`,
`--muted` and the board variables. Board, Notice and the ghost buttons re-tone
automatically; there is no per-state styling in those components. The same
mechanism gives the operator dashboard its paper surface for free.

**The operator dashboard runs on paper, the customer ticket on the dark shell.**
The handoff marks the dashboard as not yet designed in the final palette, so this
is translated from direction 3a. Beyond matching 3a, the surface split means an
operator can never mistake their dashboard for a customer's ticket.

**`--chip-bg` / `--chip-fg` exist so the logo survives both surfaces.** A paper
chip is invisible on a paper shell; these two variables flip with the surface.

### Deviations from the handoff, for design review

- **Barcode strip omitted.** The handoff marks it a placeholder and offers
  "functional or omit". A scannable code needs operator-side scanning, which does
  not exist yet, and a decorative barcode would be a lie about what the product
  does.
- **"See a live demo" is "See how it works"**, anchored to the five steps. There
  is no public demo queue to link to; a real one needs a seeded slug in env.
- **State 04's "Chair 2"** is dropped — Qless has no station or chair model. The
  serif line reads "{queue name} is ready for you."
- **Light theme is wired but has no toggle.** Tokens and `data-theme="light"` are
  in place; the handoff shows no toggle on any customer surface, so none was
  invented. It is a venue setting waiting for the settings screen.
- **Undesigned surfaces extended in-language**: join and name entry, the
  already-in-queue / skipped / left / cleared notices, paused, closed, full,
  queue-not-found, connection-lost, the create form and the queue-ready screen.
  The join screen reuses the ticket as an unclaimed stub so the customer can
  judge the wait before giving a name. These are interpretation and should be
  reviewed against the next design round.

## Direction extensions — theme, desktop, motion

**Theme toggle.** Dark stays the default; light is a preference. The control
names what you are switching *to*, and its mark is a ticket stub whose fill
inverts — no sun or moon, because the product has no icon language and does not
need one for this. Two variants: a labelled pill for nav bars, and a silent
20px mark on the customer pass where nothing may compete with the number.

Persisted in `localStorage` and applied by an inline script in `<head>` before
first paint, so the shell is never painted in the wrong colour and corrected.

**The toggle disappears from state 03 onward.** Once a customer is being called,
a settings control is noise. It is present on states 01 and 02 and gone after.

**State 03 flips against the theme, not toward paper.** Escalation is a
reversal, so a light-mode customer inverts to a dark shell with a paper ticket —
the mirror of dark mode. `[data-surface="flip"]` plus a
`[data-theme="light"] [data-surface="flip"]` override does this in CSS alone,
with no component needing to know the theme.

This forced one real fix: the `ink` button variant was hardcoded dark-on-light
and vanished when the flip went the other way. It is now `contrast`
(`bg-strong text-shell`), which resolves against whatever surface it lands on.
`ghostOnPaper` was removed for the same reason — plain `ghost` already re-tones.

**Desktop pass.** The handoff is a 390px reference, so above `lg` the stack
becomes a two-column composition inside a 1000px frame, vertically centred: the
ticket held at a readable size on the left, board and actions on the right.
Numerals step up (hero 126 → 164, next 168 → 196, turn 250 → 300). State 04
splits into numeral-beside-message with the hairline rule turning vertical.
It is a desktop composition, not a phone layout stretched wide.

**Load animation.** A board assembling itself: five rows arriving from
alternating sides on a 110ms stagger and settling flush, with the inverted
"YOU" row landing last. Drawn from the board's own vocabulary rather than a
borrowed spinner, and used on both the pass and the dashboard.

**Motion budget.** The direction allows motion only on change, with the live
dot as the sole ambient exception. Everything added respects that: a one-time
hero reveal on load, numeral swaps, staggered board entry, button press at
`scale(0.99)`, surface cross-fades. Nothing loops except the live dot and the
load animation, which by definition stops when loading does. All of it is
disabled or neutralised under `prefers-reduced-motion`.

**Lenis is scoped to the landing page only** and skipped entirely under reduced
motion. The pass and the dashboard are single-view operational screens where
hijacking the scroll would fight the user rather than flatter the page.

## Landing load sequence — the ticket reel

A strip of perforated tickets feeds across the screen, each tearing off at its
seam and tumbling away, until one is left showing its back — "No more waiting."
As it travels into the hero's slot it turns over on its Y axis to reveal the
front: the live hero ticket. Then the reel unmounts.

**The last ticket is not a picture of the hero ticket — it is the same
component.** `HeroTicket` renders either face, and the strip's height is
measured from the real hero card at layout time. So the card only translates
and rotates; nothing scales, no text distorts or reflows in flight, and the
card left underneath at the end is byte-identical to the one that landed.

Measuring the height was not optional. Both faces started on `min-h-[236px]`
and the hero face's copy pushed it to 284px — a 48px jump at the handoff that
the screenshots hid and only a `getBoundingClientRect` comparison caught.

**Runs every visit**, per the brief that this is there to make people curious
enough to try the product. To earn that it stays under ~2s (1040ms strip +
160ms hold + 780ms turn) and any key or pointer press skips it instantly.

**Skipped entirely under `prefers-reduced-motion`** — a preloader with the
motion removed is just a delay, which is worse than not having one.

**It never gates the page.** The landing is in the DOM from first paint with the
reel overlaid and marked `aria-hidden`, so assistive technology and search
crawlers never wait on it. A pre-paint inline script sets `data-reel` so the
landing's own entrance stays paused underneath instead of flashing.

**Honest note for design review:** this is the largest piece of decoration in a
product whose direction says "no ambient motion" and "don't decorate the queue".
A one-time load moment is not ambient, and it is built entirely from the
product's own vocabulary — the same ticket, the same perforation, the same
serif numerals — but it is a marketing flourish in a system built on restraint
and should be signed off as such.

## Milestone 2 — realtime

**Every event carries a full snapshot, never a delta.** A frame that arrives out
of order, or a frame missed entirely while a phone was in a tunnel, still leaves
the browser on the correct state. The event *type* exists so the UI can react to
what happened, not so it can reconstruct what changed. Queue payloads are small
enough that this costs nothing worth measuring.

**`PublicState` gained an `estimates` table, indexed by people ahead.** This is a
deviation from the payload the brief specifies, and it resolves a genuine
conflict in it: positions must be derived in the browser (otherwise the server
addresses a payload to one customer, and the public frame goes to everyone), but
the wait estimate must be computed server-side so every screen agrees. A table
of every position this queue currently has satisfies both — it contains no
names, and the formula stays in one language. The alternative was reimplementing
`EstimateWait` in TypeScript and hoping the two never drift.

**The operator's socket authenticates with `?k=` in the query string.** A browser
cannot set an `Authorization` header on a WebSocket handshake. The token is
already in the dashboard URL, `referrer: no-referrer` is set app-wide, and the
request logger records `r.URL.Path` without the query — so this adds no exposure
the dashboard link did not already have.

**A wrong token is refused, not quietly downgraded to a public connection.** A
dashboard showing a live indicator over a feed that will never contain names is
worse than an error the operator can act on. A socket opened with *no* token is a
customer's socket and stays one; there is no message it can send to change its
audience, because the read pump only ever processes pongs.

**Serve-next broadcasts one event, not two.** It both attends one customer and
calls another. Since every event carries a full snapshot, a second frame would
add nothing but noise, so the broadcast names the fact that matters to whoever is
listening: `CUSTOMER_SERVED`, or `CUSTOMER_ATTENDED` when the queue has emptied.

**All ten event types are defined; five are emitted.** Skip, pause, resume, close
and reset have no endpoints until milestone 3. Defining the whole enum now means
each of those handlers adds one line rather than a payload format.

**A client whose buffer fills is disconnected, not buffered for.** It is sixteen
events behind on a queue that changes a few times an hour — it is gone, not slow.
Dropping it costs that browser a reconnect and a refetch, which is the same
recovery path as any dropped connection, and it stops one dead socket from
stalling the fan-out for everyone else in the shop.

**The logging middleware had to grow a `Hijack` method.** Wrapping a
`ResponseWriter` in a struct hides every other interface the original
implemented, so the upgrader could not reach the raw connection and every
handshake failed with a 500. Worth recording because nothing about the symptom
points at the logger.

**The focus refetch from milestone 1 stays, alongside the socket.** A phone that
was asleep can hold a connection the browser has quietly frozen: it still reads
as open and no frame ever arrives. One request on focus closes that gap, and the
server's own ping/pong catches the cases where the connection is properly dead.

**A fetch that a live frame overtook keeps only the part it was sent for.** The
customer view is assembled from two sources — public state over the socket, the
customer's own entry over HTTP — so an in-flight `/me` that lands after a newer
frame merges its entry into the newer state rather than overwriting it. Without
that, a busy queue could roll a screen backwards.

**Two hooks share one `useQueueSocket`, rather than collapsing into a single
`useQueue`.** The brief asks for one hook owning fetch and socket, which is a
rule against a global store, not against the customer and operator surfaces
having their own state. They receive different payloads under different
authority; merging them would produce one hook branching on audience throughout.

## QR codes

Pulled forward from milestone 4, because a queue nobody can scan into is a link,
not a product.

**The code is always dark-on-white, on every surface and in both themes.** It is
a thing a stranger's camera has to read off a screen at an angle in bad shop
lighting. Tinting it to the ticket's cream, or letting it invert with the theme,
trades scans for taste.

**The print sheet retones itself through the design tokens, not by overriding
colours.** In `@media print` the sheet redefines `--strong`, `--muted` and the
chip variables: muted grey is hierarchy on a monitor and a smudge on a laser
printer. The logo chip matters most — browsers drop background colours when
printing, which would have left a near-white Q on white paper, so in print the
chip loses its fill and the Q becomes the ink.

**The sheet is 90vh in print, not 100vh.** A rounding difference between the page
box and the viewport unit is enough to push the footer onto a second, otherwise
blank, sheet of paper.

**The printed sheet is deliberately static** — mark, business name, one
instruction, the code, the link. No numbers, no counts, nothing that is wrong by
the time someone reads it taped to a door. The only live thing on it is the code,
and the code never changes.

**The download finds its canvas in the DOM at click time.** A ref threaded from a
hook into JSX is exactly what the React compiler's lint refuses, and it was
protecting real intent: the canvas is needed when the operator clicks, not while
rendering.

## Phase 1 — identity

**Two credential layers, not one.** A code is something a person types — read off
a printed sheet, dictated across a counter. A session token is something a
browser holds and never shows anyone. Conflating them, as the single owner token
did, forces one secret to be both memorable enough to re-enter and long enough
to be safe, and it has to travel on every request to boot. So a code is redeemed
*once* for a session token, and only session tokens authenticate requests.

**Session tokens are rows, not a column.** `access_tokens` holds many per
principal, which is what makes every operation on identity additive rather than
destructive. Recovering on a new phone inserts a row; the counter tablet's row is
untouched. "Sign out other devices" is a `DELETE`, not a re-key, so it can be
offered as a deliberate act instead of a side effect of getting back in. The old
design had exactly one secret per queue, so *every* one of these operations was
the same operation: replace it, and break every device that had it.

**Recovery does not sign anything out, and never will by default.** The common
reason to recover is a new phone, not a stolen one. An owner who recovers on the
bus should not arrive to find the shop's tablet logged out. Someone who has
actually lost a device asks for that on purpose, on `/api/sessions/revoke-others`.

**A rotated recovery code is staged, not swapped.** Redeeming mints the
replacement and returns it, but the redeemed code keeps working until the client
calls `/api/access/recovery-code/acknowledge`. Rotating in place is a coin flip
on the network: lose the response to a dropped connection or a closed tab and the
owner is holding a dead code with no way back into their own business — the exact
disaster recovery exists to prevent. Two live codes for the length of one screen
is by far the cheaper failure. Redeeming the staged code also settles it as
current, so whichever of the two the owner actually received is the one that
survives.

**A queue id is not a credential.** It says which queue; the token says who is
asking. Authorization is a question about an actor, never about a URL, and
`TestOperatorEndpointsRejectNonOwners` now includes a perfectly valid session
token belonging to a different business among the credentials that must fail.

**Authorization asks the actor's type, not the queue's owner column.**
`AuthorizeQueue` switches on what kind of principal is asking rather than
joining `queues.owner_id`, even though owning it is the only path that exists
today. Assigned operators are a second path in 00003 and a service grouping
would be a third; every handler already asks the question in a form those
answers fit. Handlers get a yes or no, never a route.

**`requireOwner` splits in two.** `requireQueueAccess` covers what the permission
table shares with operators — serving, skipping, pausing — and `requireOwner`
covers what it does not: closing, resetting, settings, the roster. Serving next
and listing entries moved to the shared check now, so that adding operators
changes what `AuthorizeQueue` answers rather than what handlers ask. There is
still deliberately no middleware: a new route that forgets to call one of these
is visibly missing a line, not silently uncovered.

**One owner per distinct token in the backfill, not one per queue.** Two queues
sharing an owner token hash were always operated by one holder — that token
opened both dashboards — so collapsing them into a single business is what the
old schema actually meant. It is also the only reading that survives the unique
constraint on `token_hash`, which the test database proved by containing dozens
of such rows: the fixture derived its token from the test name, so every run
wrote the same hash again.

**Owners who predate this get a recovery code they never see.** The backfill
generates one, stores its hash, and prints it nowhere. They are no worse off than
they were — before this they had no recovery path at all — their bookmarked
dashboard link still works exactly as it did, and a code that only the database
has cannot be phished out of anyone. Issuing codes we cannot deliver would be
worse than issuing none.

**`access_tokens` points at owners with a nullable foreign key, not a
polymorphic `principal_id`.** One id column covering two tables would have saved
a column and cost every foreign key and cascade in the table. 00003 adds
`operator_id` beside it and the other half of the check constraint.

**Codes use Crockford's base32 and are read forgivingly.** No I, L, O or U:
the first three are the characters people confuse, and dropping U means the
alphabet cannot spell the one four-letter word that would end up on a printed
sheet. Sixteen characters is 80 bits, shown as four groups. On the way in,
case, spacing and dashes are stripped and confusable characters are folded (O→0,
I and L→1) before hashing — someone copying a code is not choosing a password,
so a typo they cannot even see is our problem, not theirs.

**Redeem gives one answer along one path.** A code that never existed, one that
has been rotated away, and an empty string all take the same branch, do the same
lookup and return the same body. Notably the code is *not* validated for shape
first: rejecting a malformed code without a database round trip would answer
measurably faster than rejecting a well-formed one, which is a difference an
attacker can use. No artificial delay is added on top, because there is no longer
a differential to mask.

**Rate limiting and lockout are separate mechanisms because they do different
jobs.** The token bucket paces guessing but never stops it — it refills forever.
The lockout closes the door after twenty wrong codes from one address inside ten
minutes. The bucket is keyed by address *and* by the code's first group, so
spreading an attack across many addresses does not buy a free sweep through codes
that begin alike; the lockout is keyed by address only, so nobody can shut a
legitimate owner out by hammering their prefix.

**Creating a queue with an unknown token is refused, not treated as a new
business.** Quietly minting a second owner for someone whose session has expired
would scatter their queues across two businesses, invisibly and with no way to
merge them afterwards. A 401 is recoverable; that is not.

**A queue and its owner are created in one transaction, and a slug collision
retries the whole of it.** "Every queue has an owner" is a database invariant
rather than a hope, and an abandoned attempt leaves no half-made business behind.

**Constant-time comparison is gone, and its absence is not a regression.** The
old check fetched a stored hash and compared it with `subtle.ConstantTimeCompare`.
The token hash is now the key of a unique index: either a row with that exact
hash exists or the caller is nobody, and there is no secret being compared to
leak timing about.

**`last_seen_at` is touched at most once every five minutes per token**, in the
same statement that resolves the actor. The column answers "is this tablet still
in use", not "when was this button clicked", and a write on every operator action
would be a lot of writes for a question nobody asks that precisely.

**`show_names_to_operators` ships in this migration but does nothing yet.** The
column exists so that the queues predating operators are already carrying the
setting, defaulted to off — a clinic has to opt in to staff seeing names rather
than opt out. Phase 5 wires it to the realtime frames.

## Phase 2 — the session in the browser

**One session key replaces the per-queue ones.** `qless.owner.<queueId>` is now
read but never written; `qless.session.token` is the whole of who this browser
is. The old shape is why there was no "my queues" screen: a token filed under a
queue's own id can only answer a question you already know the answer to. The
new one asks the server, which is the only party that knows.

**Nobody is signed out by that change.** A pre-session token is a valid session
token — it is a row in `access_tokens` after the 00002 backfill, just filed
under the wrong key in the browser. Opening a dashboard that has one promotes it
to a session, so the migration happens on the next visit and no one has to
notice.

**A dashboard link no longer carries `?k=`.** The token is stripped from the URL
with `history.replaceState` — which Next wires into its own router, so nothing
navigates and nothing remounts — but only once the session write has been read
back. A private window can accept a write and lose it, and at that moment the
URL is the only other copy of the credential. Throwing it away on faith would
lock the operator out of the queue they just created.

That also changes what the create screen says. It used to tell an owner to
bookmark the dashboard link and keep it to themselves, which was true when the
link *was* the credential. Now the link is an address and the recovery code is
the credential, so the copy says that instead.

**The recovery code screen is the one place a forced acknowledgement is
honest.** Everywhere else a checkbox in front of a button is friction for its
own sake. Here the code is not stored anywhere retrievable, the next screen
cannot show it again, and there is no email to reset it to — the tick is the
difference between an owner who can recover their business and one who cannot.

**Create and recovery show the same screen but only recovery acknowledges it.**
Acknowledgement promotes a *staged* code; a code minted at create has nothing
behind it to promote, so calling the endpoint there would be a request that does
nothing. The asymmetry is in the model, not an oversight.

**`/enter` is the only door, and it does not ask who you are.** The person types
a code and the server answers with a role. A screen that asked "are you an owner
or an operator?" would be asking the client to assert something only the server
can know, and would have to be re-checked anyway.

**The queue list shows a status pill only when the queue is not open.** On a row
that is itself a link, a pill reading "OPEN" is read as the button that opens
it. Paused and closed are the states worth catching at a glance; open is the
unremarkable case and says more by saying nothing.

**Primary links use `contrast`, not `paper`.** Cream on the dark shell reads as
the primary action; the same cream on the light shell is nearly invisible
against `--shell-soft`. `contrast` resolves against whatever surface it lands
on, which is what the variant was added for in the first place. `LinkButton`
exists so an anchor that looks like a button is styled from `Button`'s own
classes rather than a copy of them — and stays an anchor, so middle-click,
long-press and the keyboard all still work.

## Phase 3 — the operator surface

**Entry numbers are unique among *active* entries, not for all time.** The
original `UNIQUE (queue_id, number)` made two required behaviours mutually
exclusive: reset restarts numbering at 1, and reset preserves history. With
every past entry still holding its number, the first customer of the new day
collides with the cleared number 1 from the old one — which is exactly how it
failed, with a 500 on the first join after a reset.

Migration 00003 replaces it with a partial unique index over `WAITING` and
`SERVING`. The invariant worth enforcing is that two people *in the queue*
cannot hold the same number; once an entry is attended, skipped, left or
cleared it is a record of something that happened and its number is free again.
That is the same reasoning as `one_active_entry_per_token`, which has been
partial since 00001 — this one simply had not been thought through yet.

**Every operator action re-reads the queue before answering.** Handlers resolve
the queue at the top of the request and then change it, so the copy they hold is
stale by the time they respond: pausing answered `OPEN`, and resetting answered
with the old next number. `buildEvent` already re-read the row for exactly this
reason and said so in a comment; the HTTP response path had the same bug and no
comment. It now re-reads too, which makes it correct by construction for every
handler phase 4 adds.

**Serving a specific customer attends whoever was at the counter.** There is one
counter and `one_serving_per_queue` enforces it, so calling someone out of order
has to close out the current customer — the same thing serve-next does. What it
does not do is reorder anybody: the customer is lifted out of the line and
everyone else keeps the position their phone is showing them.

**Attending accepts a waiting customer, not just the one being served.** An
operator who dealt with somebody without formally calling them first should be
able to say so, rather than having to call them in order to finish with them.

**A stale row is a 409, not a 404 or a silent success.** Two operators on two
tablets will click the same row; the second one gets `entry_not_active` and the
dashboard refetches, because at that point its screen is known to be wrong. A
double click on one tablet is not that — serving someone already at the counter
answers with their entry unchanged, so a slow connection cannot produce an error
out of a no-op.

**`PATCH` decodes the body twice.** A `*int` cannot tell `"maxCapacity": null`
from an absent `maxCapacity`, and those mean opposite things: clear the limit,
or leave it exactly as it is. The body is read into a map to see which keys were
sent, then into the typed struct — still refusing unknown fields, so a typo in a
settings key fails loudly instead of silently leaving that setting alone.

**Renaming a queue does not change its slug.** The slug is derived from the name
at create time and then frozen. A queue's URL is printed on a sheet taped to a
door and encoded into every QR code already in the wild; regenerating it on a
rename would invalidate all of them to no benefit.

**Confirmation dialogs on skip, close and reset — not on serve next, and not on
pause.** Serve next is the action an operator takes all day; a dialog in front
of it is a tax on the main path. Pause is instantly reversible and disturbs
nobody. The other three either take something away from a specific customer or
end the day, and none of them is obvious from the button alone.

`ConfirmDialog` had to be repaired before it could be used. It was styled with
`text-ink`, `text-ink-muted` and `bg-urgent` — tokens the Ticket Pass redesign
removed — so those classes emitted nothing and its `destructive` prop had no
visual effect at all, on a dialog the customer page was already using for "leave
queue". Rebuilt on the live tokens, with destructive carried by inversion and
weight rather than colour, because vermilion means "it's your turn" and spending
it on a confirm button would cost it that meaning everywhere else.

## Phase 4 — operators

**Adding a second principal changed no handler.** `AuthorizeQueue` has switched
on actor type since phase 1, and every route already picked `requireOwner` or
`requireQueueAccess`; filling in the operator branch was the whole of enforcing
the permission table. That was the point of writing it that way three phases
early, and it is worth recording that the bet paid.

**An operator's code is nothing like an owner's recovery code**, and the
difference is who is standing there. A recovery code is the only way back into a
business, so it rotates on use and is staged until acknowledged. An access code
is handed over in person by someone who can hand over another one, so it is
reusable, it never rotates on its own, and regenerating retires the old one
immediately with no ceremony. Both are typed into the same box on `/enter`,
because the person typing should not have to know which kind they hold.

**Revoking is a soft delete, and it takes away four things at once**: the status
flips, the code hash is set to NULL, every session token is deleted, and the
assignments are cleared. Clearing the hash rather than filtering on status at
redeem means a revoked code cannot be matched by any lookup anyone adds later.
The row itself stays so history keeps resolving to a name — deleting the person
would quietly rewrite what happened.

**Assignment is checked on every request, not carried in the token.** An owner
who unassigns a queue expects that to bite on the operator's next action, not on
their next sign-in. It costs one indexed lookup that `AuthorizeQueue` was making
anyway.

**Assigning to a queue you do not own is silently dropped, not refused.** The
insert selects from `queues` filtered by owner, so an id belonging to another
business matches no row. An error would confirm that the id exists, which is a
small oracle to hand out for no benefit; the response returns the assignments
that were actually made, so nothing is hidden from the owner either. Getting
that second half wrong was the bug the tests caught — the handler was echoing
back the ids it had been *asked* for.

**`acted_by` records who caused the current status, not a full audit trail.**
Two columns, and the last actor on an entry wins: a customer called by an
operator and then finished by the owner reads "attended by the owner", which is
exactly what the row's status says happened. Tracking serve and attend
separately would be more columns for a distinction nobody has asked for, and
history shows terminal states.

The operator's name is joined at read time rather than copied onto the entry, so
renaming somebody corrects their whole history instead of leaving it stamped
with a name they no longer use.

**An access token points at exactly one principal.** 00002's check only
guaranteed that an OWNER token had an owner; 00004 replaces it with one that
pins both directions — an owner token has an owner and no operator, an operator
token the reverse. The operator's owner is reached through `operators.owner_id`
rather than denormalised onto the token, so there is one place that says who
somebody works for.

**The dashboard hides controls by role read from storage.** A principal's type
never changes, so the stored role cannot drift, and a browser holding only a
pre-session token is an owner by definition — operators did not exist when those
were issued. It decides what is *drawn*; the server checks every request
regardless, which is what the operator permission table test actually asserts.

## Phase 5 — the names toggle

**Three audiences, and the third one is named for what it is.** `Public`,
`Owner`, `Staff`. Calling the dashboard audience "operator" was accurate right
up until there were operators, at which point it described two different people
with different rights to the same screen. `frameFor` also defaults to the public
frame rather than falling through to a dashboard one, so an audience added later
without a frame leaks nothing.

**With names on, staff receive the owner's frame byte for byte.** Only the
redacted frame is built separately, and only when it is needed. The single frame
in this product that exists to leave something out is the one the setting is
about — everything else is the same encoding shared by everyone entitled to it.

**One function answers "may this person see names", and every surface asks it.**
`maySeeNames(queue, actor)`. There are three places a customer name can reach a
screen — the dashboard view, the realtime frame built from it, and history — and
history is the one that would have been missed: it is a separate query, on a
separate screen, and staff kept from names at the counter could have read every
one of them a click later. Redaction happens on the way out of the view builder
rather than at each caller, because a caller that forgets is a leak and a view
builder that forgets is a test failure.

**The toggle takes effect on the next frame, not the next sign-in.** A
connection's *audience* is fixed when it opens, but what each audience's frame
contains is decided per event. So an owner who realises staff should not be
seeing names flips the switch and it stops, on screens that are already open,
without anybody signing out. There is a test for exactly that — a socket opened
while names were on, still connected when they go off.

**Redacted means blank, not absent.** `customerName` stays in the payload as an
empty string and the view carries `showsNames` alongside it. The client renders
a queue of numbers deliberately, and says so once in a quiet notice, because a
counter of blank rows with no explanation reads as a bug rather than as the
setting the owner chose. A name of "" is not otherwise reachable — the column
requires one to three-score characters after trimming.

**"By you" is only true for the owner.** History attributes each entry to
whoever caused its current status, and the first cut rendered an owner's action
as "by you" for everybody — so staff saw their manager's work credited to
themselves. It now reads "by the owner" unless the owner is the one reading. The
underlying data was right; the sentence was not.

