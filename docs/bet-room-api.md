# Bet room service — API contract

**Status: implemented**, with two named exceptions in §5.5 and §5.6. All eight routes are live; the
machine-readable form is [`openapi.yaml`](openapi.yaml), served at `GET /openapi.yaml`. The
boilerplate's `PUT /room`, `POST /join` and `DELETE /room/{id}` are gone — the room they operated on
no longer has the fields they set.

This document is the reasoning; the spec is the reference. Where they disagree, the spec is wrong.

**What is NOT implemented, and is documented rather than hidden:**

| Gap | Effect | Detail |
|---|---|---|
| Server-side eligibility re-checks | A crafted request can open a duel on a live event or on a market that is not two-way | §5.5 — needs a sportsbook feed. The **underround guard IS enforced**, on create and on acceptance, because it is pure arithmetic over stored odds |
| Settlement | A filled duel stays `filled` forever, so the settled, closed and rematch states are unreachable end to end | §5.6 — needs a settlement lookup |
| Frontend gaps G1, G2, G4, G5 | The service accepts what the widget sends today, but four things need widget changes to work properly | §6 |

## Why this is a transcription, not a design

The `sport-web-widgets` monorepo has a merged, tested `@sport-widgets/bet-room` widget. Its room
client already calls six routes against this service, with fixed paths, bodies and response shapes.
The paths are not open for improvement — changing them means changing shipped frontend code.

What *is* open: two routes the specs require and the client never got (§2.7, §2.8), a handful of
places where the client and the spec disagree (§6), and everything in §5 that the service has to own
and the current skeleton has no precedent for.

### Sources

Every claim below traces to one of these. Provenance is cited inline as `[src]` markers.

| Marker | Source |
|---|---|
| `[client]` | `widgets/bet-room/src/services/roomClient/roomClient.ts` |
| `[client-types]` | `widgets/bet-room/src/services/roomClient/roomClient.types.ts` |
| `[client-utils]` | `widgets/bet-room/src/services/roomClient/roomClient.utils.ts` |
| `[types]` | `libs/types/src/widgets/betRoom/{room,duel,strategy,config}.ts` |
| `[invite]` | `widgets/bet-room/src/utils/inviteLink.ts` |
| `[lifecycle]` | `openspec/changes/duel-bet-room-widget/specs/bet-rooms-room-lifecycle/spec.md` |
| `[duel]` | `openspec/changes/duel-bet-room-widget/specs/bet-rooms-duel-strategy/spec.md` |
| `[widget]` | `openspec/changes/duel-bet-room-widget/specs/bet-rooms-duel-widget/spec.md` |
| `[design]` | `openspec/changes/duel-bet-room-widget/design.md` |
| `[tasks]` | `openspec/changes/duel-bet-room-widget/tasks.md` |
| `[findings]` | `openspec/changes/duel-bet-room-widget/findings-market-eligibility.md` |

A route or field with no `[src]` marker is invention and should be challenged.

---

## 1. Wire-format constraints

These are the highest-risk part of the contract. None of them appear in the capability specs — they
live in the client's transport layer — and each one fails *silently*: the request succeeds, the
widget renders, and the wrong thing is on screen.

### 1.1 No response envelope `[client-utils]`

`requestRoomService` does `(await response.json()) as TData` with zero unwrapping. **The success
body IS the object.** A `{"data": …}` wrapper would make every field arrive `undefined`.

`writeJSON` (`internal/api/respond.go`) writes `v` unwrapped, and a test asserts the response has
no top-level `data` key.

### 1.2 The error body key is `message`, not `error` `[client-utils]`

`readErrorMessage` reads `body.message` and falls back to `response.statusText`.
`internal/api/respond.go` used to emit `{"error": "..."}`, which would have dropped **every**
room-service error message and shown the player an HTTP status line instead of the stated reason. A large share
of the widget's scenarios — "the reason given is that the market has no single opposite side"
`[duel]`, "the player is told which of those reasons applied" `[widget]` — depend on this one key.

```json
{ "message": "This duel has already been taken", "code": "roomFilled" }
```

`code` is additive: logs and future frontend branching. The widget reads only `message`.

> **DONE, and it was a breaking change.** `writeError` is shared, so renaming the key also changed
> the boilerplate routes — removed in the same commit — and both `scripts/smoke.sh` and the Postman
> collection asserted on the old shape and were rewritten with it. Two error shapes in one service
> would have been worse than one breaking change.
>
> `writeJSON` also sets `SetEscapeHTML(false)`. Left on, Go escapes `&` as `\u0026`: valid JSON that
> decodes correctly, but it renders every `inviteUrl` unreadable in logs and in curl.

### 1.3 Timestamps are epoch milliseconds, as JSON numbers `[types]`

`createdAt` and `expiresAt` on the room, and `expiresAt` on a seat hold, are all `number`. Go's
`time.Time` marshals to RFC3339 by default, which parses as `NaN` on the client and produces a
countdown that never ticks.

Use `int64` milliseconds in every DTO. Store `time.Time` internally if you like; convert at the edge.

`[types]` also flags the direction of the one conversion the widget does: `EventType.startTime` is
epoch *seconds* and is multiplied by 1000 before it reaches this contract.

### 1.4 The three selection keys are objects, not strings `[types]`

`marketId`, `marketItemId` and each side's `outcomeId` are **structured keys**. The widget composes
them from `MarketModelType`, `MarketItemKeyType` and `OutcomeKeyType` (`libs/types/src/markets.ts`)
and reads them back as objects; transcribing any of them as an opaque `string` 400s every room the
widget tries to create.

```jsonc
"marketId":     { "eventId": "evt-1", "marketType": 5, "period": 0, "resultKind": 1 },
"marketItemId": { "marketParameters": ["2.5"] },
"outcomeId":    { "type": 3, "values": [] }
```

`subPeriod` on `marketId` is **omitted** rather than zeroed when absent: the widget distinguishes "no
sub-period" from "sub-period 0" when it matches a market model against a placed bet.

**`marketParameters` and `values` must serialise as `[]`, never `null`.** Both are legitimately empty
— a parameterless market such as 1X2 has no parameters at all — and the widget's `findMarketItem`
compares them with `JSON.stringify(item.key.marketParameters) === JSON.stringify(outcomeId.values)`,
where `null` equals nothing. A Go nil slice marshals to `null`, so it is normalised on write **and**
on every read (`roomView`); a room stored with `null` silently fails to resolve its own market rather
than erroring.

### 1.5 The status-code map is exact `[client-utils]`

`mapStatusToErrorKind` is a closed switch. **Any status not in it collapses to `unavailable`**, which
renders as "duels are temporarily unavailable" `[widget]` — the wrong sentence for a business refusal.

| Status | Client kind | Use for |
|---|---|---|
| 400 / 422 | `validation` | unknown strategy `[lifecycle]`, malformed body, missing `betRef` |
| 401 | `unauthorised` | missing or invalid bearer token `[lifecycle]` |
| 403 | `forbidden` | wrong invite code on a seat claim `[lifecycle]`; confirming a hold that belongs to another user `[lifecycle]`; rematch reserved by a non-opponent `[duel]` |
| 404 | `notFound` | unknown room `[lifecycle]`, unknown hold |
| 409 | `conflict` | room filled `[lifecycle]`; seat already held `[lifecycle]`; creator taking a second seat `[lifecycle]`; underround at acceptance `[duel]`; daily limit reached `[widget]` |
| **410** | `expired` | room past `expiresAt` `[lifecycle]`; hold lapsed `[lifecycle]`; **event has started** `[duel]` — *not enforced, see §5.5* |
| 5xx / anything else | `unavailable` | — |

Two notes. **410-for-expiry is unusual** and is the single easiest thing here to get wrong — 409
would be the instinct, and it produces "already taken" where the truth is "no longer open".
**"Event has started" maps to 410** deliberately: the widget's `expired` presentation says the duel
is no longer open, which is the right sentence for a kicked-off event `[duel]`.

401 and 410 were both net-new: the boilerplate used 400/403/404/409/500 only.

Every row here is covered by a test in `internal/api`, except the "event has started" half of 410 —
that has no code path at all until §5.5 is resolved, and is listed so the status is not quietly
reused for something else.

### 1.6 Auth on every route `[lifecycle]` `[design]`

Bearer token in the `Authorization` header. `[client-utils]`'s `buildRequestHeaders` puts it nowhere
else, with the comment that a query string and a body are both logged and cached where a header is
not — so the service must never accept it in either.

The participant payload carries **no user identifier at all** `[lifecycle]`: the token is the only
caller identity the service gets. See §5.1 for the four things that depend on it.

CORS is an allowlist `[design]`, configured by `CORS_ALLOWED_ORIGINS`. An origin that is not on it
gets no CORS headers back, so a browser refuses the response — but the request is still served, because
CORS is a browser policy and not an authorisation check, and treating it as one invites a false sense
of a boundary.

**How the caller is verified.** `AUTH_JWT_SECRET` selects HS256 verification, with the caller taken
from the token's `sub` claim; the algorithm is pinned rather than read from the token's own header,
which is the classic JWT bypass. With no secret set the server falls back to an **insecure** mode
where the token itself is the caller's identity — so the smoke script and the Postman collection work
without a token issuer — and logs a warning saying exactly that at startup.

### 1.7 `payload` must decode as `json.RawMessage`

`decodeJSON` in `internal/api/handlers.go` sets `DisallowUnknownFields()`, and that applies to nested
structs too. If `payload` decodes into a typed duel struct, any field the widget adds later becomes a
400 on the one route whose job is to carry strategy data opaquely — and the strategy seam `[design]`
stops being a seam.

Decode `payload` as `json.RawMessage`; validate per-strategy after dispatching on `strategy`.

The same flag has a second consequence: **every request struct must enumerate every field the client
sends.** See the sequencing table in §2.

---

## 2. Routes

| # | Route | Purpose | Provenance |
|---|---|---|---|
| 1 | `POST /rooms` | create a room from an already-placed bet | `[client]` |
| 2 | `POST /rooms/{roomId}/seat` | reserve a seat (TTL hold) | `[client]` |
| 3 | `POST /rooms/{roomId}/seat/{holdId}/confirm` | confirm the seat with the placed bet | `[client]` — body incomplete, see G1 |
| 4 | `POST /rooms/{roomId}/read` | read room state (polled) | `[client]` |
| 5 | `GET /rooms?eventId=&status=open` | open rooms on an event | `[client]` |
| 6 | `POST /rooms/{roomId}/rematch` | rematch against the same opponent | `[client]` — drops 2 fields, see G2 |
| 7 | `DELETE /rooms/{roomId}/seat/{holdId}` | release a hold explicitly | **`[lifecycle]` only — no client method exists** |
| 8 | `GET /rooms/limits` | per-duel max, daily limit, played today | **`[widget]` `[design]` only — no client method exists** |

Routes 7 and 8 citing a spec but no client line is the point, not an omission: they are required
behaviour the frontend cannot currently ask for. Each needs a `RoomClientType` method added.

Paths and methods for 1–6 are transcribed from `roomClient.ts` and are **fixed**. Two that look wrong
and are not:

- **`POST /rooms/{roomId}/read` instead of `GET`** — the invite code is a join secret `[lifecycle]` and a
  query string is the one place it must never appear `[client]`.
- **`POST /rooms/{roomId}/seat` for a reservation** — a hold is a created resource, and the route is what
  ships.

Existing `PUT /room`, `POST /join` and `DELETE /room/{id}` are superseded by 1–7. `GET /ping` and
`GET /openapi.yaml` stay.

### 2.0 Shipped vs. proposed bodies — a sequencing constraint

Routes 3 and 6 are the only two where the body in this contract is **not** what the client sends
today. Building either from the shipped shape produces a service that cannot work; building from the
proposed shape produces one the current client cannot call.

| | Route 3 (confirm) | Route 6 (rematch) |
|---|---|---|
| What the client sends today | `{participant, betRef}` — cannot fill `opponentSide.placement` (G1) | `{participant, payload}` — no `betRef`, no `expiresAt` (G2) |
| What the route accepts | the above, **plus optional** `odd` and `placement{stake, lineItemId, dataVersion}` | `betRef` **required**, `expiresAt` optional |

Because `decodeJSON` forbids unknown fields (§1.7), both had to be declared either way.

**Route 3 accepts the incomplete body and fills the room**, recording the derived entry as the
joiner's stake and leaving `lineItemId`/`dataVersion` empty — visibly incomplete rather than
invented, and logged. The widget keeps working, and the data says what is missing.

**Route 6 refuses the incomplete body with a 400.** The opposite call, and deliberately so: a rematch
participant with no `betRef` has no link to money — the defect task 8c.2 `[tasks]` already fixed once
for create — and every such room would collide on the unique index over an empty bet id. This route
therefore cannot succeed from the shipped client until G2 lands, and says so rather than storing that.

---

### 2.1 `POST /rooms` — create a room `[client]`

The creator's bet is placed *first*, in the host bet slip, and the room is opened from it `[duel]`.
So the request always carries a real `betRef`.

```jsonc
// request
{
  "eventId": "evt-ucl-2026-09-15",
  "strategy": "duel",
  "participant": {
    "label": "Maksym K.",          // already-derived display form, never raw name fields
    "initials": "MK",
    "balances": { "eur": 75.5 }    // opaque, stored and returned unchanged
  },
  "betRef": { "id": "bet-8812", "number": 12345 },
  "expiresAt": 1757600000000,      // client's min(createdAt + inviteWindow, eventStart)
  "payload": { /* §3 */ }
}
```

`201` → a `BetRoomType` (§4).

**`betRef` is required, not optional** `[client-types]`: `BetRoomParticipantType.betRef` is required
on every participant `[types]`, and the creation ordering guarantees it is in hand. A room holding a
participant with no link to money is the whole thing this contract exists to prevent — the room is a
coordinator, never a wallet `[duel]`.

Service work:

1. Validate `strategy`; unknown → 400 with no room stored `[lifecycle]`.
2. Derive `capacity` from the strategy — 2 for `duel` `[lifecycle]` `[types]`. Never read it from the
   request.
3. **Clamp `expiresAt`** to `min(request.expiresAt, createdAt + inviteWindow, eventStart)`. The
   client sends its own computation `[client-types]` precisely so the constraint is structural rather
   than a check someone forgets — but the client is not a trust boundary `[design]`, so clamp anyway.
   The service may only ever *shorten* it.
4. Issue an `inviteCode` that is **not derivable from the room id** `[lifecycle]`, and an `inviteUrl`
   (§5.3).
5. Record the caller's token subject on the participant, unexported (§5.1).

Refusals: unknown strategy → 400 · event already started → 410 `[duel]` · daily limit reached → 409
`[widget]` · no credential → 401 `[lifecycle]`.

### 2.2 `POST /rooms/{roomId}/seat` — reserve a seat `[client]`

```jsonc
{ "inviteCode": "d4k9xq" }
```

`201` → a `BetRoomSeatHoldType` `[types]`:

```jsonc
{ "id": "hold-77", "expiresAt": 1757600060000 }
```

Joining is deliberately two steps `[lifecycle]`: reserve, then confirm. The joiner's entry is
committed on their behalf at a fixed amount, so the seat must be theirs before any bet exists — a
joiner must never be left holding a bet for a duel they did not enter `[duel]`.

The hold is returned **only to the joiner**. Another reader sees it as `status: "reserved"` and
nothing more `[types]`.

This is the most concurrency-sensitive route in the service — see §5.2.

> **The hold TTL is unsized, and the document should not pretend otherwise.**
>
> It must cover a `GetOutcomesV2` subscription load **plus** a placement round-trip, not a placement
> alone `[design]`: a bet request needs `lineItemId` and `dataVersion`, which the market feed does not
> carry, so the joiner's flow is hold → wait for the subscription → place → confirm.
>
> Task 6.9 `[tasks]` records this as never measured against real infrastructure, and that the only
> number in the codebase — `SEAT_HOLD_TTL_MS = 60_000` in the mock client — is an arbitrary
> placeholder, explicitly not to be read as a sized value.
>
> A TTL tuned for placement alone lapses under normal conditions, which turns the benign
> "seat reopens" path into the common case. Make it configuration, and measure it before launch.

Refusals: wrong code → 403 `[lifecycle]` · unknown room → 404 `[lifecycle]` · room filled, seat
already held, or the creator requesting a seat in their own room → 409 `[lifecycle]` · room expired
or event started → 410 `[lifecycle]` `[duel]`.

### 2.3 `POST /rooms/{roomId}/seat/{holdId}/confirm` — confirm the seat

```jsonc
// SHIPPED today [client] — incomplete; do not build only to this
{
  "participant": { "label": "Ivan P.", "initials": "IP", "balances": { "eur": 40.0 } },
  "betRef": { "id": "bet-8813", "number": 12346 }
}

// WHAT THE ROUTE ACCEPTS — odd and placement are optional, and used when present
{
  "participant": { "…": "…" },
  "betRef": { "id": "bet-8813", "number": 12346 },
  "odd": 207,                                                  // price actually accepted
  "placement": { "stake": 8.79, "lineItemId": "li-99", "dataVersion": 9 }
}
```

`200` → a `BetRoomType`.

**Why the shipped body is not enough.** `DuelSidePlacementType` requires `stake`, `lineItemId` and
`dataVersion` `[types]`, and the matched, settled and closed presentations render *each side's own*
`placement.stake` `[tasks]` — using the shared `figures.entry` instead would show the creator the
opponent's money under their own name, which `duel.ts` `[types]` warns about explicitly.

The service cannot derive them. The joiner is placed at the **repriced acceptance-time odd**, not the
`opponentSide.odd` stored at creation `[duel]`, so the stake is not recomputable from stored state.

On confirm the service writes `opponentSide.odd`, `opponentSide.placement`, and **refreshes
`figures` at the accepted price** — which is what "the 'both sides win the same amount' invariant is
restated at the accepted price" `[duel]` requires.

Only on confirmation does the joiner become a participant `[lifecycle]`.

Refusals: unknown room or hold → 404 `[lifecycle]` · hold belongs to a different caller → 403
`[lifecycle]` · hold lapsed or room expired → 410 `[lifecycle]`.

### 2.4 `POST /rooms/{roomId}/read` — read room state `[client]`

```jsonc
{ "inviteCode": "d4k9xq" }   // optional — a participant reads without it
```

`200` → a `BetRoomType`.

Must set `viewerParticipantId` for the caller (§5.1): the readable state has to tell the reader which
participant they are, because matching on the display label is not sufficient — labels are not unique
`[lifecycle]`.

A caller who is neither a participant nor a code holder still reads the room — it is observable, and
an anonymous spectator is the point — but `inviteCode` and `inviteUrl` are withheld, so watching
never becomes a way to take the seat. The url embeds the code as `betRoomInvite`, so the two are
disclosed together or not at all.

**This route dominates the service's request volume.** Clients observe state changes by re-reading;
the service must not require a persistent connection `[lifecycle]`. The widget's cadence `[design]`:

| Widget state | Cadence |
|---|---|
| idle | on mount, then on focus |
| waiting | ~5s |
| matched (pre-match or in play) | ~30s |
| settled, closed, not-taken | none — terminal |

Polling pauses when the document is hidden and resumes on focus `[design]`.

### 2.5 `GET /rooms?eventId={id}&status=open` — open rooms on an event `[client]`

`200` → an array of `BetRoomType`. **An event with no open rooms returns `[]`, never a 404**
`[lifecycle]`.

Returns the rooms on that event that are neither filled nor expired `[lifecycle]`, minus two
exclusions:

- **the caller's own rooms** `[lifecycle]` — needs §5.1;
- **rematch rooms** (`rematchOfRoomId` set) — those are joinable only by one named opponent `[duel]`
  and must never appear in a public list.

#### Decision: this response includes `inviteCode`

The idle state lists other players' open duels and offers taking the other side `[widget]`, but
reserving a seat needs the code (§2.2), which `[lifecycle]` calls the sole authorisation to take a
seat. Those two cannot both be literally true.

**Resolved in favour of the list, for callers who can act on it.** Listing a duel on an event *is*
the offer to anyone viewing that event, so returning the code is a deliberate disclosure rather than
a leak. The code remains the secret for the invite-link flow, and the rematch exclusion above keeps
the one genuinely restricted case unaffected.

**An anonymous caller is the exception.** This route no longer requires a credential, so the "anyone
viewing that event" the decision above was written for has widened from *any signed-in user* to
*anyone at all* — and that premise change is what the exception answers. `inviteCode` and
`inviteUrl` are omitted for a caller with no token. Taking a seat still requires one, so a code they
cannot use buys them nothing, while returning it would let a scraper harvest every open code on an
event in a single unauthenticated request — and would defeat the same secret being withheld on
§2.4. An anonymous viewer sees the duel and signs in to take it.

> **Consequence, stated so nobody "fixes" it later.** Any authenticated caller who can list an event
> can then reserve a seat in any room on that list, and `POST /rooms/{roomId}/seat` has no way to
> tell a code that came from the list from one that came from a link — nor should it. For listed
> rooms, the 403-on-wrong-code path stops being a meaningful boundary. Hardening reserve against this
> would silently break the idle-list flow.

Not currently exercised: task 8b.5 `[tasks]` records the open-duels list as unwired — `listOpenRooms`
exists but no epic reads it — so this can still be revisited with product before it ships.

### 2.6 `POST /rooms/{roomId}/rematch` — open a rematch

```jsonc
// SHIPPED today [client] — drops two fields the epic passes in
{ "participant": { "…": "…" }, "payload": { /* §3 */ } }

// WHAT THE ROUTE ACCEPTS — betRef required; expiresAt optional, defaults to the invite window
{
  "participant": { "…": "…" },
  "betRef": { "id": "bet-9001", "number": 12400 },
  "expiresAt": 1757700000000,
  "payload": { /* §3 */ }
}
```

`201` → a `BetRoomType` with `rematchOfRoomId` set.

A rematch is a **new duel**, subject to every eligibility rule, joinable only by the other participant
of the original duel rather than by the first person to open a link `[duel]`.

The new room inherits `eventId`, `strategy` and `capacity` from `{roomId}`, sets `rematchOfRoomId`,
and resolves the permitted opponent **server-side** from that reference. This is why the reference
exists rather than a participant id: the payload carries no stable cross-room identity `[design]`, so
only the token subject can make the restriction work (§5.1).

A non-opponent reserving a seat in a rematch room → 403 `[duel]`.

`sendRematchEpic` treats the restriction as entirely the service's — task 6.7 `[tasks]` records that
it is not re-tested client-side. There is no second line of defence here.

### 2.7 `DELETE /rooms/{roomId}/seat/{holdId}` — release a hold **(new)**

`[lifecycle]` has an explicit requirement — "A joiner SHALL also be able to release a hold
explicitly", with its own scenario — and `[duel]` requires that when a joiner's placement is rejected
"the seat becomes available again". Neither is served by anything the client calls today.

`204` on success. `403` if the hold is not the caller's. `404` if the room or hold is unknown.
**Idempotent**: releasing an already-lapsed or already-released hold is also `204`, since the
post-condition the caller wants is already true.

A released hold leaves no participant and no trace beyond the seat being open `[lifecycle]`.

Needs `releaseSeat` added to `RoomClientType` — see G4.

### 2.8 `GET /rooms/limits` — duel limits **(new)**

```jsonc
// 200
{ "perDuelMax": 500, "dailyLimit": 10, "playedToday": 3, "currency": "EUR" }
```

Scoped to the calling user. `[widget]` requires the widget to show the maximum stake per duel, the
daily duel limit and the count played today, and to refuse a duel that would exceed either — and
`[design]` states that the room service enforces the daily count regardless of what the widget knows.

The room service is the only component that knows how many duels a user opened today, so the count
has to come from here. `perDuelMax` and `dailyLimit` may be proxied from account settings `[design]`.

`POST /rooms` and `POST /rooms/{roomId}/rematch` enforce the same numbers: 409 with `code`
`dailyLimitReached` or `perDuelMaxExceeded`. The day boundary is **UTC midnight** — the service has
no timezone for a player.

The daily count is **best effort**: if the count query fails, creation is allowed through and the
failure is logged, because refusing a legitimate duel over a failed database read is the worse
outcome. `perDuelMax` is a pure comparison and is always enforced.

Needs a matching client method — see G5.

---

## 3. `payload` — the duel strategy payload `[types]`

Carried generically at the room level `[lifecycle]`, but the service **writes into it** on confirm
(§2.3) and on settlement (§5.6), so for `duel` it cannot be a blind blob.

```jsonc
{
  "marketId": { "eventId": "evt-ucl-final", "marketType": 5, "period": 0, "resultKind": 1 },
  "marketItemId": { "marketParameters": ["2.5"] },
  "creatorSide": {
    "outcomeId": { "type": 3, "values": [] },
    "odd": 182,
    "placement": { "participantId": "p-1", "stake": 10.00, "lineItemId": "li-12", "dataVersion": 7 }
  },
  "opponentSide": { "outcomeId": { "type": 4, "values": [] }, "odd": 205 },
  "figures": { "payout": 18.20, "entry": 8.87, "pot": 18.87 },
  "winnerParticipantId": "p-1"
}
```

**The selection is a triple**: `marketId` + `marketItemId` + each side's `outcomeId`, each one a
structured key rather than an opaque string (§1.4). The market and item sit at payload level, which
is what structurally guarantees both sides are on the same line — a market holds many items, so a
market id alone cannot tell Over 2.5 from Over 3.5 `[types]` `[findings]`.

Validation follows from that shape. `marketId.eventId` is required, and each side's `outcomeId` must
be present — decoded into a **pointer**, so only an omitted key is refused. A zero key is valid data,
not an absence: `type` 0 is a real outcome type, and the first side of a 1X2 market sends exactly
`{"type": 0, "values": []}`. Rejecting all-zero keys refused those duels outright. **`marketItemId` has no presence check at all**: an empty `marketParameters` is what a
parameterless market legitimately sends, so absent and valid are indistinguishable here. The two
sides must still name different outcomes, compared field by field — a key holding a slice is not
comparable with `==`, and nil and empty `values` are the same key.

`creatorSide.placement` is **always** present: the room is created from a bet that already exists.
`opponentSide.placement` appears only on confirm. `winnerParticipantId` is absent while settlement is
pending and absent on a void `[types]`.

### Arithmetic the service must not get subtly wrong

**Odds are raw integers**: `182` means `1.82` `[types]` `[design]`. Keep them integers server-side.
The underround guard is exact integer arithmetic with no division and no epsilon `[design]`:

```
eligible  ⟺  100 * (aRawOdd + bRawOdd) >= aRawOdd * bRawOdd
```

Equality is permitted — a perfectly fair pair makes the pot exactly equal the winner's take, which
displays consistently `[duel]`. Note the guard must **fail closed**: an odd the integer test cannot
evaluate is refused, not allowed. Task 8c.3 `[tasks]` records this exact bug being fixed client-side,
where a malformed odd made the conjunction false and returned the market *eligible*.

**Money is 2dp**, and the order of operations is load-bearing `[duel]` `[design]`:

```
payout = round(creatorStake × creatorOdd, 2)
entry  = floor(payout / opponentOdd, 2)        // floor, from the ROUNDED payout
pot    = creatorStake + entry
```

Flooring the exact product instead of the rounded payout gives `0.61` where the correct path gives
`0.60` `[design]`. The entry is floored rather than rounded so the opponent's own return never exceeds
the stated winner's take `[duel]`.

Use integer minor units or a decimal type. **Never `float64`** — `[design]` records that `8.87 * 100`
is `886.9999999999999` and floors to `8.86`.

The gap between `pot` and `payout` is the book's existing margin on the two prices. It is not a fee,
and the room never takes it `[duel]`.

---

## 4. `BetRoomType` — the response shape `[types]`

Returned by routes 1, 3, 4, 5 and 6.

```jsonc
{
  "id": "room-4f21",
  "eventId": "evt-ucl-2026-09-15",
  "strategy": "duel",
  "status": "open",
  "capacity": 2,
  "participants": [
    {
      "id": "p-1",
      "label": "Maksym K.",
      "initials": "MK",
      "balances": { "eur": 75.5 },
      "betRef": { "id": "bet-8812", "number": 12345 }
    }
  ],
  "viewerParticipantId": "p-1",
  "inviteCode": "d4k9xq",
  "inviteUrl": "https://sportsbook.example/event/evt-ucl-2026-09-15?betRoomId=room-4f21&betRoomInvite=d4k9xq",
  "createdAt": 1757596400000,
  "expiresAt": 1757598200000,
  "payload": { "…": "…" },
  "rematchOfRoomId": "room-3a08"
}
```

| Field | Notes |
|---|---|
| `status` | `open` \| `reserved` \| `filled` \| `expired` \| `settled` \| `void`. See below. |
| `capacity` | Declared by the strategy, not by the request. 2 for `duel`. |
| `participants` | **Confirmed participants only.** A live hold is not listed here `[types]`. |
| `viewerParticipantId` | Omitted for a code holder who has not taken a seat `[types]`. |
| `inviteCode` | Omitted unless the caller may hold it — see §2.5 for when they may. `inviteUrl` embeds the same code as `betRoomInvite`, so the two are disclosed together or withheld together. |
| `inviteUrl` | A **host** URL, not a service URL. See §5.3. |
| `createdAt`, `expiresAt` | Epoch **milliseconds** (§1.3). |
| `rematchOfRoomId` | Present only on a rematch. |

### Status is one explicit value

This replaces the skeleton's `isFilled` / `isStarted` booleans. `[lifecycle]` requires a single
explicit value "rather than a set of independent booleans, so that mutually exclusive states cannot
be represented at once" — a room is never simultaneously filled and expired.

A room counts as filled in `filled`, `settled` and `void`, and **never** in `expired` — that is where
a room which did *not* fill ends up. A room that filled before its expiry instant is unaffected by
that instant passing `[types]` `[lifecycle]`.

**Whether the event has started is deliberately not room state** `[lifecycle]` `[types]`. That is a
question for the sportsbook feed, and duplicating it here would let the two disagree. Do not add it.

### Data minimisation is a commitment, not a convention

`label` and `initials` arrive **already derived** — `"Maksym K."`, `"MK"` — and the client never sends
the raw `firstName` / `lastName` they came from, never an email address, never an account number and
never a credential `[lifecycle]` `[design]`.

The reason is explicit in `[design]`: a service that is not a profile store never holds a full legal
name, and a future change of labelling policy does not require re-deriving from PII the service should
not have kept. **The service must not start asking for more.**

`balances` is opaque: stored and returned unchanged, never computed with, never used to authorise
anything `[lifecycle]`.

`label` and `initials` are untrusted display text originating from another player's account `[types]`.

---

## 5. What the service must own

None of this has a precedent in the current skeleton.

### 5.1 Caller identity from the token

The payload carries no user id by design (§1.6), so the service must extract a **subject** from the
bearer token and store it on each participant — **never returning it**. Four requirements depend on
it and none can be met without it:

| Requirement | Source |
|---|---|
| `viewerParticipantId` on every read | `[lifecycle]` |
| The creator cannot occupy a second seat (409) | `[lifecycle]` |
| `GET /rooms` excludes the caller's own rooms | `[lifecycle]` |
| Rematch is joinable only by the original opponent | `[duel]` |

### 5.2 The seat claim must be atomic

> "Seat availability SHALL be evaluated atomically, so that two simultaneous requests for a room with
> one open seat result in exactly one hold." — `[lifecycle]`

This is a requirement, not an optimisation. **Embed the hold in the room document** so a single
`FindOneAndUpdate` decides it. `AddParticipant` in `internal/store/room.go` is the existing template:
every precondition lives in the filter, so a rejected claim never writes.

```
filter:  _id            = roomId
    AND  inviteCode     = <code>
    AND  status         ∈ (open, reserved)
    AND  expiresAt      > now
    AND  $expr: size(participants) < capacity
    AND  (hold = null OR hold.expiresAt <= now)      // a lapsed hold is not a hold
    AND  participants.subject != <caller>            // creator cannot take a second seat

update:  set hold = { id, subject, expiresAt: now + holdTtl }, status = "reserved"
```

Note `status ∈ (open, reserved)` with the lapsed-hold clause, not `status = open`: a room whose hold
lapsed is still `reserved` until something rewrites it, and refusing there would strand the seat.

On no match, follow with a read to disambiguate the reason and return the right status — 403 vs 409 vs
410 — rather than collapsing everything to 409. The existing `join` handler is the precedent for
exactly this two-query disambiguation, including its final "the room changed between the two queries,
please retry" fallback for the race.

**A hold is not a participant.** It lapses on its own and leaves no trace beyond the seat reopening
`[types]` `[lifecycle]`.

### 5.3 `inviteUrl` is a host URL, not a service URL

Pinned by `[invite]`: the **host event page** URL carrying two query parameters.

```
INVITE_ROOM_ID_PARAM = 'betRoomId'
INVITE_CODE_PARAM    = 'betRoomInvite'
```

Both are required — `readRoom` takes a room id, and the contract has no resolve-by-code-alone route,
so a half-formed link is treated as no invite at all `[invite]`.

This **supersedes** the older ask in `[design]`'s proposal for a `GET /join/{id}` route on the
service. Do not build both. It also resolves the README's standing gotcha that `roomUrl` 404s in a
browser: the link was never the service's to serve.

> `PublicBaseURL` in `internal/config/config.go` is the **service's own** base and today builds
> `roomUrl = PublicBaseURL + "/join/" + id`. That is the wrong base for `inviteUrl`. Add a separate
> host-event-URL template to config — stage and production differ, and `[invite]` names itself as the
> single place to change if the service picks different parameter names.

### 5.4 Expiry is evaluated, not merely stored

A sweeper that flips `open → expired` is useful, but **reads and the list query must also evaluate
`expiresAt` against now**. Otherwise a room reads as `open` until the sweeper catches up and the
widget offers a seat that cannot be taken — the exact "two sources disagree" failure `[lifecycle]`
avoids elsewhere by keeping event start out of room state.

Treat lazy evaluation as the source of truth; the sweeper is a convenience for the settlement worker.

An unconfirmed hold does not survive room expiry: confirmation is refused and the room reports its
original participant only `[lifecycle]`.

### 5.5 Deferred — server-side eligibility re-checks

`[design]` requires the service to re-check **two-way, prematch and underround on both create and
join**, because the widget's view of kick-off can be stale and the client is not a trust boundary.
`[duel]` adds that the eligibility guard is evaluated at acceptance as well as creation, since a pair
that was eligible when a duel opened can drift into an underround while it waits.

Prematch and underround need a live event start time and live prices. **What this service can reach
for market data is not answerable from either repository**, so it is named here as the blocking
dependency rather than designed.

**Consequence while it is open:** a crafted request can open a duel on a live event, on a three-way
market, or on an underround price pair. The widget's own guards are real but client-side, and
`isDuelEligible` `[tasks]` runs in the browser.

**Statuses these refusals would take**, so §1.5 is complete when they land: a market that is not
two-way at create → **400, proposed rather than spec-backed** — `[duel]` gives "no single opposite
side" as the *reason*, but the status is a judgement call here, since a malformed selection is
closest to a validation failure; the opposite outcome suspended or removed at acceptance → 409,
rendering as "the market is no longer available" `[duel]`; an underround pair → 409, which is already
in §1.5 because the guard is pure arithmetic on stored odds and is the one of the three the service
can evaluate without a feed.

Options for whoever picks this up, in order of preference:

1. The same feed/odds API the sportsbook serves the widget from — full re-check.
2. Accept the client's price and market snapshot as sent, and enforce only `expiresAt ≤ eventStart`
   from a lighter event lookup. Bounds the worst case (a duel accepted after kick-off) without a
   full feed dependency.
3. Ship without it and record the trust gap explicitly.

### 5.6 Deferred — settlement

`status: settled | void` and `payload.winnerParticipantId` are in the contract and the widget renders
three presentations from them — settled (won/lost), closed (void), and the rematch entry point that
follows settlement `[widget]`. **Nothing in the client ever writes them, and nothing can.**

This is a background worker resolving each participant's `betRef` against the sportsbook's settlement:

| Condition | Result |
|---|---|
| Both bets settled, one outcome won | `status = settled`, `winnerParticipantId` = that participant `[duel]` |
| Market voided / event cancelled | `status = void`, no winner; each stake returned by the sportsbook's own void handling `[duel]` |
| Either bet still unsettled | Unchanged — the duel reports no winner and stays matched `[duel]` |

Because the market is two-way, exactly one participant is the winner `[duel]`. The duel **does not
pay** the winner: the sportsbook's settlement of that participant's own bet is the payment. A void
counts towards neither participant's record nor breaks a streak `[duel]`.

It needs a settlement-lookup dependency the service does not have.

**Consequence while it is open: every filled duel stays in `matched` forever.** The settled, closed
and rematch states are unreachable end to end, and a duel's underlying bets settle normally with the
players paid — they simply never see the result inside the widget.

---

## 6. Gaps between the specs and the shipped client

G1, G2 and G4 are correctness defects with a named frontend owner. They are the part of this document
the widget team needs to act on.

### G1 — `confirmSeat` cannot populate the joiner's placement *(contract + frontend)*

Body and reasoning in §2.3. `{participant, betRef}` carries no `stake`, `lineItemId` or `dataVersion`,
but all three are required on `DuelSidePlacementType` `[types]`, and the service cannot derive them
because the joiner is placed at the repriced acceptance-time odd.

**Service side: done.** `odd` and `placement` are accepted and used when present. When they are
absent the room still fills, recording the derived entry as the joiner's stake and leaving
`lineItemId`/`dataVersion` empty — visibly incomplete rather than invented, and logged.

**Frontend fix still needed:** `roomClient.ts` `confirmSeat`, plus the call site in
`epics/room/acceptDuel.ts`. All four values are already in hand there — `snapshot.odd`,
`currentFigures.entry`, `snapshot.lineItemId`, `snapshot.dataVersion` — and are passed to
`PlaceDuelBet` immediately before the confirm call. They are simply not forwarded.

### G2 — `sendRematch` silently drops `betRef` and `expiresAt` *(frontend)*

`SendRematchRequestType` declares both `[client-types]`, `sendRematchEpic` passes both, and
`roomClient.ts` `sendRematch` destructures only `{roomId, participant, payload}` — so neither reaches
the wire.

A rematch room would hold a participant with **no link to money**. That is precisely the defect task
8c.2 `[tasks]` fixed for create, where the mock fabricated `{id: '', number: 0}` and hid it — and the
mock room client sends both fields correctly here too, which is why the tests pass.

**Service side: `betRef` is required and the route 400s without it** (§2.0 explains why failing
loudly beats the alternative). `expiresAt` is optional and defaults to the invite window.

**Frontend fix still needed:** `roomClient.ts` `sendRematch`. Until it lands, rematch cannot succeed
from the widget.

### G3 — the open-duels list vs. the invite secret — **RESOLVED**

The list returns `inviteCode` to a signed-in caller, and omits it for an anonymous one. Decision and
its consequence in §2.5.

### G4 — no release-seat route *(service + frontend)*

`[lifecycle]` requires explicit release and `[duel]` requires the seat to reopen when a joiner's
placement is rejected. Today `acceptDuel.ts` on `placementFailed` abandons the hold and waits for the
TTL, and `RoomClientType` has no method to do otherwise.

**Service side: done** — `DELETE /rooms/{roomId}/seat/{holdId}`, idempotent, tested.

**Frontend fix still needed:** add `releaseSeat` to `RoomClientType` and call it on the
placement-failure branch.

This compounds the unsized TTL in §2.2: with no explicit release, a generously-sized TTL blocks the
seat for its full duration after every failed placement — the two problems make each other worse.

### G5 — duel limits have no source anywhere *(service)*

`[widget]` requires the limits detail and the two refusals; task 7.11 `[tasks]` records that
`perDuelMax`, `dailyLimit` and `playedToday` have no data source anywhere, so the sheet omits those
rows rather than fabricating zeros. `checkDuelLimits` implements all three checks but only the balance
check is wired, for the same reason `[tasks]`.

**Service side: done** — `GET /rooms/limits`, with `playedToday` counted from the rooms the caller
is a participant of since UTC midnight.

**Frontend fix still needed:** add a client method. The sheet already renders the rows once values
exist.

### G6 — create is not idempotent *(service)*

The creator's bet is placed *before* the room exists (§2.1), so a `POST /rooms` that times out and is
retried opens a **second room for one bet**. The first room then waits out its invite window against a
bet that another room also claims.

**Done.** A unique index over `participants.betRef.id` means one bet can belong to at most one room.
A repeat by the same caller returns the existing room with a 200 rather than an error, so a retry is
safe; another caller's bet is a 409.

---

## 7. What the implementation looks like

| Area | Before | Now |
|---|---|---|
| `internal/api/respond.go` | `{"error": …}` | `{"message": …, "code": …}`, unwrapped success bodies, HTML escaping off (§1.2) |
| `internal/api/router.go` | 6 routes, no auth | the eight routes; `recovering → logging → cors → authenticating` |
| `internal/api/middleware.go` | CORS `*` | CORS allowlist, or `*` when configured with it; bearer auth putting the caller's subject on the request context, with `/ping` and `/openapi.yaml` public and the two read routes accepting an anonymous caller |
| `internal/auth/` | — | **new**: HS256 verification (stdlib only, algorithm pinned) plus the insecure development verifier |
| `internal/duel/` | — | **new**: the entry arithmetic and the underround guard, pure and integer-only |
| `internal/models/money.go` | — | **new**: `Amount`, money in minor units, JSON as a two-decimal number |
| `internal/models/room.go` | `Participant{name, balance}`, two bools, no timestamps | identity + balances + `betRef` + unexported `subject`; the `status` enum; `strategy`, `capacity`, `inviteCode`, `inviteUrl`, `createdAt`, `expiresAt`, `payload`, `rematchOfRoomId`, `permittedSubject`, embedded `hold`. `name` and `roomUrl` are gone |
| `internal/store/room.go` | 5 methods, one conditional write | the atomic seat claim (§5.2), confirm, release, the event listing, the daily count, the expiry sweep, and `EnsureIndexes` |
| indexes | none | `eventId + status`, `expiresAt`, `participants.subject + createdAt`, and unique `participants.betRef.id` (G6) |
| `internal/config/config.go` | 4 settings | + auth secret, CORS origins, host event URL template, invite window, seat-hold TTL, duel limits |
| `cmd/api/main.go` | linear wiring | + index creation at startup, the verifier choice with its warning, and the expiry sweeper goroutine |
| `docker/mongo-init.js` | rooms in the old shape | three seeded duels: one open, one filled, one expired |
| `scripts/smoke.sh`, `postman/` | the old routes and error shape | a full duel end to end, including the rematch restriction |

**Two decisions worth knowing about.**

**The room's `payload` is typed as the duel payload**, not carried as a raw document. The strategy
seam is real at the request boundary — `payload` decodes as `json.RawMessage` so an unknown field is
dropped rather than 400'd (§1.7) — but the stored field is a struct, because the service genuinely
writes into it on confirm. A second strategy turns this into a union and a per-strategy decode; with
one strategy that indirection would have no second consumer to justify it.

**The rematch's permitted opponent is resolved when the rematch is created**, not when the seat is
claimed, and stored on the new room as an unexported `permittedSubject`. The claim has to remain one
atomic document update (§5.2) and cannot consult a second room inside it.

---

## 8. How it is verified

`make test` starts the compose Mongo and runs the suite in a container. There is no mock store, and
deliberately so: §5.2's whole claim is a property of the database's conditional update, and a fake
would assert the test's own logic instead. The tests skip when Mongo is unreachable, so `go test ./...`
still works on a machine with nothing running.

| Layer | What it covers |
|---|---|
| `internal/duel` | the worked example, floor-from-the-rounded-payout, the underround guard's fail-closed cases, and a property test over 700 stake/price combinations asserting the opponent's return never exceeds the payout |
| `internal/models` | `Amount` parsing and rendering, including the float artefacts a client can emit and the `8.87 * 100` trap |
| `internal/auth` | a valid token, and the refusals — wrong secret, `alg: none`, `alg: HS512`, expired, not-yet-valid, no subject, swapped claims under a valid signature |
| `internal/store` (integration) | the expiry sweep, which no request path reaches — it runs only from the sweeper goroutine, so without a test its update document would never execute and a misspelled field in it would be invisible |
| `internal/api` (integration) | every route against real Mongo: the wire-format facts in §1, the status for each refusal scenario in the capability specs, expiry driven by an injected clock, and **twelve simultaneous claims on one seat producing exactly one hold** |

Beyond the Go suite: `make smoke` drives a full duel against the running stack, and the Postman
collection does the same with assertions on the wire format (`newman run postman/…collection.json -e
postman/…environment.json`).

**What is NOT verified here**, because it cannot be from this side: that the shipped widget works
against this service end to end. The widget's own tasks 10.1–10.5 `[tasks]` are those checks, and
they need G1, G2 and G4 fixed first.

---

## 9. Open questions

1. **What can the room service reach for live event and market data?** (§5.5) Until this is answered,
   server-side eligibility is a recorded trust gap rather than a check.
2. **Is there a sportsbook settlement API or feed a worker can consume?** (§5.6) Blocks the settled,
   closed and rematch presentations end to end.
3. **How long should a seat hold live?** (§2.2) Never measured; 90 seconds is a placeholder. Needs a
   real `GetOutcomesV2` load plus a real placement round-trip.

Product questions `[design]` leaves open and this contract does not resolve: the invite window length
(30 minutes in the prototype), where `perDuelMax` and `dailyLimit` are administered, and whether the
open-duels list needs paging.
