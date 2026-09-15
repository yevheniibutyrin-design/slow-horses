# Poll-to-bet votes — API contract

The free "Who wins?" vote behind `@sport-widgets/poll-to-bet`. A player picks an outcome for
nothing, sees the crowd split beside the odds-implied ("traders") split, and may then back the same
outcome in the betslip as an ordinary single bet.

This service owns **vote counts and the vote write, and nothing else.** Events, odds, live scores,
market closing and translations all reach the widget already, through
`GET /v0/navigation/sport/widgets/sport-line-horizontal`, `GetRichEventsByIds` and
`GetMainMarketsByProfileAndEventIds`. None of that is duplicated here.

Nothing in this document touches a room, a bet or money. The vote is free by definition; the betslip
is where a pick becomes a stake, and that is the host's, not this service's.

Read alongside [`bet-room-api.md`](bet-room-api.md): §1 of that document — no response envelope,
`message` rather than `error`, epoch-millisecond timestamps — applies here unchanged, because both
surfaces are served by one service and a second wire convention inside it would be a bug waiting to
happen.

---

## 1. The vote key

The part the service implements directly, so the field set is exact.

A vote is cast on one **outcome** inside one **market**. The market is the widget's `TMarketModel`
and the outcome its `TOutcomeKey`:

```jsonc
"market":  { "eventId": "…", "marketType": 1, "period": 0, "resultKind": 2, "subPeriod": 1 },
"outcome": { "type": 7, "values": ["home"] }
```

**The vote key is `(eventId, marketType, period, resultKind, subPeriod ?? null)` plus the outcome's
`(type, values)`.**

### 1.1 `layout` is accepted and ignored

`layout` is presentational. Keying on it would fragment one market's votes across the layouts it is
rendered in — two players picking the same outcome would land in different buckets because one of
them saw a different card.

It is nonetheless **declared** in the request shape, because `decodeJSON` rejects unknown fields: a
client that sends the whole `TMarketModel`, which is the natural thing to do, would otherwise get a
`400` on the entire batch. Same treatment as `winnerParticipantId` on the duel payload.

#### Why the market key is decoded strictly, where `payload` is not

`decodeDuelPayload` decodes **leniently** and says why: a field a future widget version adds would
otherwise `400` the one route whose job is to carry a strategy's data opaquely. The market key is a
similar-looking object — feed-shaped, passed through whole — and it is decoded **strictly** anyway.

The difference is what the object is *for*. `payload` is data the room carries; an unknown field in
it is data the service does not use. The market key is an **identity**, and an unknown field in it
may well be identity-bearing — a new dimension the feed started sending. Dropping it silently would
merge two genuinely different markets into one bucket, with no error anywhere and no way to
un-merge the votes afterwards. A `400` is loud, and recoverable.

The cost is real and worth stating: a new optional field on `TMarketModel` in `sport-space` `400`s
every widget mount until slow-horses redeploys. **Adding a field to the market key is a coordinated
deploy**, service first. That is the trade this route makes and the duel payload does not.

### 1.2 Structured fields, never a client-supplied string

The client indexes its own store by `serializeKey(marketId)`, which is plain `JSON.stringify`. That
string depends on the **runtime property order** of the object the feed handed over, not on any type
declaration — and that order is load-bearing on the client. Commit `c971a538f` exists because a
reordered outcome key turned a betslip re-add into a silent no-op.

An opaque string on the wire would make that order load-bearing **here** too: a reorder, an added
optional field, or a serializer change on either side would split one market's votes into two
buckets with no error anywhere.

So the wire carries structured fields and the server derives the identity
(`models.CanonicalMarketKey`), with two properties:

- **Fixed field order**, written out literally rather than ranged over.
- **Injective**: every string field is quoted, so a separator inside an event id cannot forge the
  boundary between two fields and collide two markets.

`values` is sorted for the stored key, so `["1","2"]` and `["2","1"]` are one bucket. It is sorted
on a **copy** — see §1.4.

### 1.3 `subPeriod` is a pointer

Absent and `null` are the same market. `subPeriod: 0` is a **different** market. A plain `int` would
collapse all three into one bucket, so the field is `*int` and the three required numbers
(`marketType`, `period`, `resultKind`) are pointers too: for a value whose whole job is to identify a
bucket, "absent" and "zero" must not be the same request. A missing one is a `400`.

### 1.4 The response echoes the caller's own keys, unchanged

The client looks a count up by `JSON.stringify` of the key object **it sent**. So every key in a
response is the key that arrived — same `values` order, same `eventId` spelling, one entry per
requested outcome in request order.

Normalising the response is the `c971a538f` failure again: a card with votes renders zeros and
nothing errors. The one normalisation that does happen is internal — the `eventId` is trimmed for the
stored key, so a stray space cannot open a second bucket, and echoed untrimmed.

---

## 2. Routes

Both are POSTs. Three to five structured market keys do not serialize sanely into a query string —
the same reason `POST /rooms/{roomId}/read` is a POST.

### 2.1 `POST /votes/counts` — read counts, batched

**One request per widget mount, not one per card.** The client type already takes an array, but
`loadVotes$` is invoked once per card from `PollCard`'s task and passes a single-element array, so
with three to five cards each response overwrites `votesStore.votedToday` and the header counter
races against itself. **Batching is a client change that must land with this endpoint** — it cannot
be fixed here, and it lives in `sport-space`, not in this repository.

```jsonc
// request
{ "markets": [ { "market": { … }, "outcomes": [ { … }, { … } ] } ] }

// 200
{
  "markets": [
    { "market": { … }, "outcomes": [ { "key": { … }, "count": 41 }, { "key": { … }, "count": 7 } ] }
  ],
  "votedToday": 1243
}
```

- Anonymous and cacheable. A signed-in reader is served exactly what a signed-out one is.
- A market with no votes yet is **not** an error: every requested outcome is answered, unknown ones
  with `0`.
- Bounded at 50 markets and 50 outcomes per market, so one request cannot become an unbounded
  aggregation. The widget asks for three to five.

### 2.2 `POST /votes` — cast a vote

```jsonc
// request — `outcomes` and `deviceToken` are optional
{
  "market":  { … },
  "outcome": { "type": 7, "values": ["home"] },
  "outcomes": [ { … }, { … } ],
  "deviceToken": "…"
}

// 200
{ "market": { … }, "outcomes": [ { "key": { … }, "count": 42 } ], "deviceToken": "…", "votedToday": 1244 }
```

| Status | Meaning |
|---|---|
| `200` | Recorded — or a retry of the same pick, which is the same answer |
| `400` | Malformed body, an unknown field, or a market key missing an identifying number |
| `409` `alreadyVoted` | This voter already voted in this market, for a **different** outcome |
| `429` `voteRateLimited` | The anonymous per-address daily limit — see §4 |

**One vote per market per identity, enforced by the database.** The unique index
`uniq_market_voter` is compound over `(marketKey, voterId)` — the **outcome is deliberately not in
it**, or one voter could vote for every outcome in the market. The refusal comes from the index
rather than from a preceding read, because two simultaneous first votes would both pass a
check-then-write and one of them has to lose. A test casts twelve simultaneous votes and asserts
exactly one is recorded.

**Votes are final**: no re-vote, no change of pick, no withdrawal.

**A repeat of the same pick is a success, not a conflict.** A retried request after a timeout must
not read as a failure — the same tolerance `POST /rooms` already has, and not a re-vote: the stored
vote is untouched and the count does not move. Only a *different* outcome in the same market is a
`409`, and it carries its own code so the client can reconcile local state it got wrong instead of
swallowing the refusal as a logged error, which is what `vote$` does today.

**The fresh counts come back from the write**, which is what lets the client stop optimistically
incrementing its own store.

---

## 3. Identity

The current client mints `crypto.randomUUID()` into `localStorage` and sends it as `userId` on every
`postVote`. **That identifier is not accepted.** A caller-minted id is free to mint, so keying "one
vote per market per identity" on it enforces nothing — and worse, a client-supplied id can be
*someone else's*: vote with a stolen one and the real owner is locked out of that market.

Identity is therefore resolved server-side, and namespaced so the two populations cannot collide:

| Caller | `voterId` | Rule |
|---|---|---|
| Bearer token present | `user:<sub>` | One vote per market per **account** |
| No token | `anon:<deviceId>` | One vote per market per **device token** |

A bearer token wins outright; a device token in a signed-in player's storage is ignored.

### A credential that does not verify is a 401, not an anonymous vote

Absence and invalidity are different answers on this route, which is why `POST /votes` sits on its
own list in the middleware (`anonymousWritePatterns`) rather than alongside the read routes.

**No credential** → an anonymous vote. That is the feature.

**An expired or unusable credential** → `401`. Falling through to anonymous would record the caller
as `anon:<device>`; once their session refreshes they would vote again in the same market as
`user:<sub>`, and both votes would count — the unique index is per voter id, and those are two
different ones. "One vote per market per account" has to hold across a credential that has just
lapsed, and a lapsed session is a routine event, not an edge case.

The read routes keep the opposite rule — on them an unusable credential reads as no credential —
because a read has nothing to double.

Worth knowing when reading the tests: `auth.InsecureVerifier`, which local development and most of
the suite run on, fails only on an empty token, and `BearerToken` already rejects those. So "a token
that does not verify" is unreachable under it. The three tests that cover this distinction build
their own server on a real HS256 verifier.

The device id is **issued by this service** (`internal/auth/device.go`): 128 random bits inside a
token signed with HMAC-SHA256, verified statelessly so no device registry is needed. Forging one
without the secret is not possible. Stealing a whole token still is — but that needs the victim's
storage, which is where their vote already lives.

A token that is absent, malformed or not signed by us reads as **no token**, and the caller is issued
a fresh one, rather than being refused. Refusing would strand every player whose token predates a
secret rotation.

The minted token is returned on the **vote response only** — see §5.

### What a device token does not claim

It bounds nothing on its own. A caller can discard it and be issued another, which is exactly what
clearing site data does — so an anonymous player can vote again in a market they already voted in.

That is the status-quo product rule for a device-local pick, and it is why §4 exists. Per-account
enforcement holds only for signed-in players.

---

## 4. Rate limiting

The actual ceiling on anonymous voting: **`VOTE_ANON_IP_DAILY_LIMIT` votes per address per UTC day**
(default 50), on the same day boundary the daily duel count resets. Over it, `429`.

Deliberately generous and configurable rather than one-vote-per-address: a shared office or
mobile-carrier NAT puts many genuine players behind one address, and a strict per-address rule would
silently disenfranchise all but the first of them.

The address is stored only as an HMAC (`ipHash`). The limit needs to **compare** addresses, not read
them.

**Behind a proxy, `TRUSTED_CLIENT_IP_HEADER` must be set** or every player collapses onto the
proxy's own address and the limit locks the whole site out after 50 votes. It is empty by default,
and the default believes no forwarded header at all — a header any caller can set is a header any
caller can vary to walk straight past a per-address limit. Only set it to a header a proxy you
control overwrites.

A limit that cannot be counted (a database error) lets the vote through and logs. Same call as the
daily duel limit: a limit that cannot be checked is not a reason to refuse a legitimate action.

---

## 5. Caching and privacy

Aggregate counts and `votedToday` are anonymous and cacheable. Anything caller-specific is served on
a **different response**.

Concretely: the minted `deviceToken` rides the **write** response, which is per-caller and uncached.
It never appears on `POST /votes/counts`, and neither does any per-caller flag such as "you voted
here" — that is what a picks read would be for (§7), on its own path.

Mixing caller identity into a response served from a shared cache path is a cohort-leak hazard this
codebase has already hit once on the feed transport. A test asserts the counts response carries
nothing but `markets` and `votedToday`.

---

## 6. `votedToday`

Every vote this service has recorded since the current UTC day began, **site-wide**, for the header's
"N voted today". It rides the counts response — it is the same anonymous aggregate, on the same
cacheable path, and a second round trip for one integer is not worth a second route.

It is a **mount-time snapshot**. A live counter needs a push channel and this service has none;
the widget's header does not tick on its own today either.

**It is counted, not cached**, on both routes — every counts read and every vote runs a
`CountDocuments` over the day's votes. That is an index scan whose cost grows through the UTC day and
resets at midnight, on the hottest path this service has. Fine at the volumes a poll widget
generates; the first thing to cache or maintain as a counter if it stops being fine.

Counts themselves are **lifetime-per-market**, not a rolling window: a market's split is about that
market, and a window would make an early vote disappear from a card the event is still running on.
Only `votedToday` resets.

---

## 7. Not included yet

### 7.1 Rejecting a vote on a closed, removed or settled market

**Not implemented.** The service cannot tell whether an event has started, whether a market is still
open, or whether it has been removed — that needs a sportsbook feed it does not have. It is the same
gap that stops it re-checking duel eligibility (`bet-room-api.md` §5.5).

What it costs: the widget hides closed cards, but a card can close between render and click, and
that vote is currently recorded. It skews a count on a market nobody can back any more — it cannot
place a bet, move money, or affect a room.

Closing it means the same dependency §5.5 names: a feed lookup on the market triple at vote time.

### 7.2 Reading the player's own picks

**Not implemented, and an open product decision** rather than an omission. With voting keyed on an
account for signed-in players, the widget could restore voted state at mount from a picks read — but
that read is caller-specific and must live on its own path, never on the counts response (§5).

It also needs a rule this service cannot invent: what happens to a vote cast while logged out and
then merged on login, or whether such a merge happens at all.

Until then the widget keeps its `localStorage` record of what the player picked. The service's
answer is authoritative for whether a *further* vote is allowed; the local record is what renders
the pick.

This is also the answer to what a `409` does and does not tell the client. It says *that* this voter
already voted in this market — enough to stop offering the vote and to correct local state that
thought otherwise — and not *what for*. Carrying the existing pick would mean a second shape for an
error body, and §1 of `bet-room-api.md` commits this service to exactly one (`message`, `code`). A
client that needs the pick itself needs the picks read above, on its own path.

### 7.3 The client's batching fix

`loadVotes$` must be called once per mount with every card's market key, not once per card (§2.1).
That change lives in `sport-space` and must land with this endpoint.

---

## 8. How it is verified

`internal/api/votes_test.go` runs against a real MongoDB, because the one-vote-per-market rule is a
unique index rather than a check in Go and a fake would assert the test's own logic. It covers:

- an unvoted market answering zeros; every requested outcome answered, in request order
- one request answering three markets, which is the batched read the header counter depends on
- keys echoed exactly as sent, `values` order included
- `layout` ignored; reordered `values` the same bucket; `subPeriod` absent ≡ null ≢ 0
- a changed pick refused with `409 alreadyVoted`, and the count unmoved
- the same pick retried answering `200` with the count unmoved
- twelve simultaneous votes from one caller producing exactly one vote
- an anonymous vote minting a token; that token refusing a changed pick; a client-minted token
  rejected; a discarded token being a new voter
- the anonymous per-address limit returning `429`, and not reaching a signed-in player
- the counts response carrying nothing caller-specific

`internal/models/vote_test.go` covers the canonical form — including that an event id containing the
separator cannot collide two markets, and that sorting `values` does not mutate the caller's slice.
`internal/auth/device_test.go` covers token round-tripping, uniqueness, and rejection of anything
this service did not sign.
