# Slow Horses — Go + MongoDB API boilerplate

A Go + MongoDB service, running entirely in Docker, serving two widget surfaces.

**Bet rooms**, for `@sport-widgets/bet-room`. A bet room turns one two-way market into a
head-to-head contest between two players; the room coordinates them and **never holds, moves or
settles money** — both sides are ordinary sportsbook single bets and the room stores a reference to
each.

**Votes**, for `@sport-widgets/poll-to-bet`. The free "Who wins?" poll: a player picks an outcome
for nothing and sees the crowd split beside the odds-implied one. Counts and the vote write are the
entire surface — events, odds and market state already reach the widget from the feed. The contract
is [`docs/votes-api.md`](docs/votes-api.md).

No framework, two dependencies (MongoDB driver + `google/uuid`).

## The `rooms` contract

| Field | Type | Notes |
|---|---|---|
| `id` | uuid string | Stored directly as Mongo's `_id` — no second identifier |
| `eventId` | string | The sportsbook event; rooms are discoverable by it |
| `strategy` | `"duel"` | Declares capacity, eligibility and settlement interpretation |
| `status` | enum | `open` · `reserved` · `filled` · `expired` · `settled` · `void` — one explicit value, never independent booleans |
| `capacity` | int | Declared by the strategy (2 for `duel`), never read from the request |
| `participants` | array | `{ id, label, initials, balances, betRef }`. `balances` is free-form and opaque; `betRef` is the room's only link to money |
| `viewerParticipantId` | string | Which participant the *reading caller* is. Computed per request; labels are not unique, so a reader cannot work this out itself |
| `inviteCode` | string | The join secret, not derivable from `id` |
| `inviteUrl` | string | The **host's** event page plus `?betRoomId=…&betRoomInvite=…` — not a URL on this service |
| `createdAt`, `expiresAt` | int64 | Epoch **milliseconds**, never RFC3339 |
| `payload` | object | The strategy's own data: the market triple, both sides, and the figures |
| `rematchOfRoomId` | string | Set on a rematch; the seat is restricted to the original opponent |

Money crosses the wire as a two-decimal JSON number and is stored in minor units. Odds are the feed's
raw integers — `182` means `1.82`.

## Read this before changing anything

**[`docs/votes-api.md`](docs/votes-api.md)** is the same thing for the votes surface: why the vote
key is structured fields rather than a serialized string, why a client-minted `userId` is not an
identity, and what the counts response must never start carrying.

**[`docs/bet-room-api.md`](docs/bet-room-api.md)** is the contract and the reasoning behind it:
every route, the wire-format facts that break the widget *silently* if you get them wrong, what the
service owns, and the gaps where the shipped widget and the specs disagree.
[`docs/openapi.yaml`](docs/openapi.yaml) is the machine-readable form, served at `GET /openapi.yaml`.

The paths are not open for improvement — they are fixed by frontend code that is already merged.

Four things to know up front:

1. **Success bodies are not enveloped, and error bodies use `message`, not `error`.** The widget does
   `response.json() as T` with no unwrapping, and reads `body.message`; the wrong key silently drops
   every stated reason and shows an HTTP status line instead.
2. **Timestamps are epoch milliseconds as JSON numbers**, never RFC3339. Go's `time.Time` default
   would be a silent break.
3. **`410 Gone` means expired**, including a lapsed seat hold. `409` is the instinct and produces
   "already taken" where the truth is "no longer open".
4. **The invite link points at the host's event page**, not at this service — which is why the old
   `roomUrl` 404'd in a browser. That link was never this service's to serve.

**Two things are deliberately not implemented**, and the document says so rather than hiding it:
server-side eligibility re-checks (needs a sportsbook feed) and settlement (needs a settlement
lookup, so a filled duel stays filled forever).

## Prerequisites

**Docker Desktop running, with `docker compose` v2. That's it.** No local Go install and no `go` on
your `PATH` — the module is compiled inside the image. `curl` (built into macOS) is enough for the
examples below; `jq` is optional for pretty output.

## Bootstrap

```bash
cp .env.example .env          # optional — compose already has working defaults
                              # (on macOS, see the port-5000 gotcha at the bottom)
docker compose up -d --build  # builds the API image, starts Mongo, seeds `rooms`
docker compose ps             # api, mongo, swagger-ui all up
```

Then check it:

```bash
curl localhost:5000/ping      # -> OK
curl -s localhost:5000/rooms
open http://localhost:8081    # Swagger UI, with "Try it out" wired to the running API
```

That first `up --build` is the only bootstrap step. Everything else:

```bash
docker compose logs -f api    # follow the API logs
docker compose down           # stop, keep the data
docker compose down -v        # stop and wipe the database volume
```

Or via `make` (every target is a Docker command — run `make help` for the list):

```bash
make up
```

## Iterating on the code

Still Docker, still no host toolchain:

```bash
docker compose up -d --build api   # rebuild + restart just the API after an edit  (make restart)
docker compose watch               # auto-rebuild on save                          (make watch)
```

`docker compose watch` uses the `develop.watch` block on the `api` service. It is a full rebuild on
change rather than true hot reload — that would mean adding a tool like `air`, which this boilerplate
deliberately skips.

## Go commands without installing Go

For `go mod tidy`, `go vet`, or adding a dependency, run the toolchain in a throwaway container with
the repo mounted:

```bash
docker run --rm -v "$PWD":/src -w /src -v slow-horses-gomod:/go/pkg/mod golang:1.27-alpine go mod tidy
```

Wrapped as `make tidy`, `make vet`, `make build`. The named volume keeps the module cache warm
between runs.

## Reseeding the dummy data

`docker/mongo-init.js` runs through Mongo's `docker-entrypoint-initdb.d`, which executes **only when
the data directory is empty**. After the first boot, `docker compose up` reuses the volume and
silently skips seeding — so to get the three dummy duels back (one open, one filled, one expired):

```bash
make reseed        # = docker compose down -v && docker compose up -d --build
```

Inspect the data directly with `make mongosh` (`docker compose exec mongo mongosh slowhorses`).

## Endpoints

Base URL `http://localhost:5000`. **Every room route below needs `Authorization: Bearer <token>`**
except `GET /ping`, `GET /openapi.yaml`, and the two read routes — `GET /rooms` and
`POST /rooms/{roomId}/read` — where a token is optional so a room is observable without signing in.
**Both vote routes take an optional token too**, and for the vote write that is the feature: the
poll is free and a signed-out player must be able to use it.
Sending one there still matters: only a participant or a caller who presents the correct invite code
gets `inviteCode` and `inviteUrl` back. Every route that writes still requires a token, because an
anonymous caller has no identity to attribute a seat to. With `AUTH_JWT_SECRET` unset the token
itself is taken as the caller's identity, so `Bearer alice` is a user called alice — see
[Authentication](#authentication).

| Route | What it does |
|---|---|
| `POST /rooms` | Open a room from a bet the creator has already placed |
| `GET /rooms?eventId=&status=open` | Open rooms on an event, minus the caller's own and every rematch |
| `GET /rooms/limits` | The caller's duel limits and how many they have played today |
| `POST /rooms/{roomId}/read` | Read room state — the route the widget polls |
| `POST /rooms/{roomId}/seat` | Hold a seat, before the joiner commits anything |
| `DELETE /rooms/{roomId}/seat/{holdId}` | Release a held seat rather than waiting out its TTL |
| `POST /rooms/{roomId}/seat/{holdId}/confirm` | Turn the hold into a participant, with the bet placed |
| `POST /rooms/{roomId}/rematch` | Open a rematch, joinable only by the original opponent |
| `POST /votes/counts` | Vote counts for every card on screen, in one request |
| `POST /votes` | Cast a free vote, and get the market's fresh counts back |

Full request and response shapes: [`docs/bet-room-api.md`](docs/bet-room-api.md) §2.

### The shape of a duel

Creation and acceptance have **opposite orderings**, and that is the load-bearing part:

```
  CREATION                                  ACCEPTANCE
  place the bet (the player chose it)       hold the seat   (short TTL)
        |                                         |
        v                                         v
  create the room with betRef               place the bet (derived stake)
        |                                         |
        v                                         v
  waiting                                   confirm the seat with betRef
                                                  |
                                                  v
                                             matched
```

A creator's bet that fails to become a duel is an ordinary single bet they chose to place — which is
exactly what an unaccepted duel decays into anyway. A joiner's bet that fails to become a duel is a
stake they never asked to place on its own, so their seat must be theirs first.

### `POST /rooms`

The creator's bet already exists, so `betRef` is required. The server derives `capacity` from the
strategy and the figures from the stake and the two prices — it never stores a client's claim about
money it can compute itself.

```bash
curl -s -X POST localhost:5000/rooms \
  -H 'authorization: Bearer alice' -H 'content-type: application/json' \
  -d '{"eventId":"evt-ucl-2026-09-15","strategy":"duel",
       "participant":{"label":"Maksym K.","initials":"MK","balances":{"eur":75.5}},
       "betRef":{"id":"bet-8812","number":12345},
       "expiresAt":'"$(( ($(date +%s) + 1800) * 1000 ))"',
       "payload":{"marketId":"total_goals","marketItemId":"total_goals_2.5",
         "creatorSide":{"outcomeId":"over","odd":182,
           "placement":{"stake":10.00,"lineItemId":"li-12","dataVersion":7}},
         "opponentSide":{"outcomeId":"under","odd":205},
         "figures":{"payout":0,"entry":0,"pot":0}}}' | jq
```

`10.00` at `1.82` against `2.05` gives `payout 18.20`, `entry 8.87`, `pot 18.87`. The gap between the
pot and the payout is the book's existing margin on the two prices — not a fee, and the room never
takes it.

| Status | Meaning |
|---|---|
| `201` | Room created |
| `200` | This bet already opened a room of yours — a retried create is idempotent |
| `400` | Unknown strategy, malformed body, missing `betRef`, or an expiry in the past |
| `409` | Underround prices, a stake over the per-duel max, the daily limit, or another caller's bet |

### `POST /rooms/{roomId}/read`

A POST rather than a GET on purpose: the invite code is a join secret and a query string is the one
place it must never appear. A participant sends no body; a code holder sends the code.

```bash
curl -s -X POST localhost:5000/rooms/<id>/read \
  -H 'authorization: Bearer alice' -H 'content-type: application/json' -d '{}' | jq
```

`200` with the room, or `403` for a caller who is neither a participant nor a code holder.

### `POST /rooms/{roomId}/seat` and `/confirm`

Seat availability is evaluated **atomically**: two simultaneous requests for a room with one open
seat result in exactly one hold, because every precondition lives in a single conditional update.

```bash
# hold a seat — returns {"id":"…","expiresAt":<epoch ms>}
curl -s -X POST localhost:5000/rooms/<id>/seat \
  -H 'authorization: Bearer bob' -H 'content-type: application/json' \
  -d '{"inviteCode":"<code>"}' | jq

# confirm it with the bet that was placed
curl -s -X POST localhost:5000/rooms/<id>/seat/<holdId>/confirm \
  -H 'authorization: Bearer bob' -H 'content-type: application/json' \
  -d '{"participant":{"label":"Ivan P.","initials":"IP","balances":{"eur":40}},
       "betRef":{"id":"bet-8813","number":12346},
       "odd":205,
       "placement":{"stake":8.87,"lineItemId":"li-99","dataVersion":9}}' | jq
```

| Status | Meaning |
|---|---|
| `403` | The invite code does not match, or the hold is someone else's |
| `409` | Filled, already held, or the creator asking for a second seat |
| `410` | The room expired, or the hold lapsed — **not** a conflict |

A hold is **not** a participant: it lapses on its own and leaves no trace beyond the seat reopening.

### `POST /rooms/{roomId}/rematch`

A new duel restricted to the other participant of the one it came from. The service resolves that
opponent itself — the payload carries no cross-room identity for it to be told — so the invite code
alone does not get a stranger in.

### `POST /votes/counts` and `POST /votes`

The free vote. Both are POSTs because a structured market key does not serialize sanely into a query
string.

```bash
# counts for every card on screen — ONE request per mount, not one per card
curl -s -X POST localhost:5000/votes/counts \
  -H 'content-type: application/json' \
  -d '{"markets":[{"market":{"eventId":"evt-ucl-2026-09-15","marketType":1,"period":0,"resultKind":2},
                   "outcomes":[{"type":7,"values":["home"]},{"type":7,"values":["away"]}]}]}' | jq

# cast one — no token needed; the response carries a device token to send back next time
curl -s -X POST localhost:5000/votes \
  -H 'content-type: application/json' \
  -d '{"market":{"eventId":"evt-ucl-2026-09-15","marketType":1,"period":0,"resultKind":2},
       "outcome":{"type":7,"values":["home"]},
       "outcomes":[{"type":7,"values":["home"]},{"type":7,"values":["away"]}]}' | jq
```

| Status | Meaning |
|---|---|
| `200` | Recorded — or a retry of the same pick, which answers the same |
| `400` | Malformed body, an unknown field, or a market key missing an identifying number |
| `409` | Already voted in this market, for a **different** outcome. Votes are final |
| `429` | The anonymous per-address daily limit |

Three things about the key that are easy to get wrong, all covered in `docs/votes-api.md` §1:
`layout` is accepted and **ignored**; `values` order does not matter for the bucket but is **echoed
back as sent**; and `subPeriod` absent is the same market as `null` but **not** the same as `0`.

### `GET /ping`, `GET /openapi.yaml`

Liveness and the served spec. The only two routes that need no credential.

### Smoke test

`./scripts/smoke.sh` (or `make smoke`) drives a full duel against a running stack: create, list,
reserve, release, confirm, rematch, plus the `401`/`403`/`409` negative cases, failing loudly on the
first unexpected status. It then drives the free vote on a fresh market: zeros on an unvoted market,
a vote, the same vote retried, a changed pick refused, an anonymous vote and its device token, and
`layout` being ignored. The Postman collection does the same with assertions on the wire format.

## Authentication

The room contract carries **no user identifier in any payload** — not an id, not an email, not an
account number. The bearer token is the service's only way to know who is calling, and four rules are
unenforceable without it: which participant a reader is, that a creator cannot take a second seat,
that the open-rooms list excludes the caller's own, and that a rematch is joinable only by the
original opponent.

- **`AUTH_JWT_SECRET` set** — HS256 tokens are verified and the caller is the `sub` claim. The
  algorithm is pinned rather than read from the token's own header, which is the classic JWT bypass.
- **unset** — the server runs an **insecure** verifier that takes the token itself as the caller's
  identity, so `Bearer alice` is a user called alice and anyone can impersonate anyone. It exists so
  local development, the smoke script and the Postman collection work without a token issuer, and the
  server logs a warning saying exactly that at startup.

## Swagger UI

http://localhost:8081 — served by the `swagger-ui` container, reading `docs/openapi.yaml` from a
read-only mount. "Try it out" posts to `http://localhost:5000` (the single `servers:` entry in the
spec), which works because `CORS_ALLOWED_ORIGINS` defaults to `*`. **That default answers every
origin and is for local development only** — set it to a real comma-separated allowlist before this
goes anywhere real. `Access-Control-Allow-Credentials` is never sent, which is what keeps `*` legal
for a browser and keeps an ambient cookie from ever authenticating a cross-origin caller.

The spec is hand-written and is the single source of truth: there is no code generation, so **edit
`docs/openapi.yaml` by hand whenever a route changes**. The `servers:` dropdown lists `:5000` and
`:5001` — pick the one matching your `API_HOST_PORT`; add an entry for any other host.

## Postman

1. Postman → **Import** → **Files**, and pick *both*
   `postman/slow-horses.postman_collection.json` and `postman/slow-horses.postman_environment.json`.
2. Select the **slow-horses (local docker)** environment (top-right).
3. Run the collection top to bottom, or open it and hit **Run**.

The collection drives a full duel in the order the widget does — alice opens a room, bob holds a
seat and confirms it, alice offers a rematch only bob can take — saving what each step needs into the
environment. A pre-request script on the first request mints fresh bet references per run, because
one bet can belong to at most one room.

The `V1`–`V8` requests at the end cover the free vote, on a market id minted per run for the same
reason: one vote per market per identity is permanent, so a re-run against the same market would be
a `409` everywhere.

`aliceToken`, `bobToken` and `carolToken` are three different callers: in the server's insecure mode
the token *is* the identity. Set `AUTH_JWT_SECRET` and they need to be real HS256 tokens.

Beyond the status codes, the tests assert the three wire-format facts that break the widget silently:
success bodies are not enveloped, error bodies use `message`, and timestamps are epoch milliseconds.
The vote requests add the two that break `poll-to-bet` the same way: a key comes back exactly as it
was sent, and the counts response carries nothing about the caller.

With `newman` installed (`npx newman` works too):

```bash
newman run postman/slow-horses.postman_collection.json -e postman/slow-horses.postman_environment.json
```

## Environment variables

| Variable | Default | Notes |
|---|---|---|
| `API_HOST_PORT` | `5000` | Host port mapped to the container's 5000. Override if something owns 5000 |
| `MONGO_URI` | `mongodb://mongo:27017` | The compose service name; use `localhost` only from the host |
| `MONGO_DB` | `slowhorses` | Also drives the seed script's target database, so the two cannot drift. Changing it needs `make reseed` — the existing volume will not seed into a new name |
| `PUBLIC_BASE_URL` | `http://localhost:5000` | This service's own base. **Not** the base for invite links — see below |
| `AUTH_JWT_SECRET` | *(unset)* | HS256 signing secret. Unset selects the insecure verifier — see [Authentication](#authentication) |
| `CORS_ALLOWED_ORIGINS` | `*` | Comma-separated allowlist, or `*` for every origin (the MVP default). Narrow it before this serves anything private |
| `HOST_EVENT_URL_TEMPLATE` | `http://localhost:3000/event/{eventId}` | Builds `inviteUrl`. The **host's** event page, not this service |
| `INVITE_WINDOW_MS` | `1800000` | A room's expiry is clamped to it, and the clamp can only shorten what a client asked for |
| `SEAT_HOLD_TTL_MS` | `90000` | **Unsized** — it must cover a subscription load plus a placement round-trip, and that has never been measured |
| `DUEL_PER_DUEL_MAX` | `500.00` | Maximum stake per duel |
| `DUEL_DAILY_LIMIT` | `10` | Duels one caller may open per UTC day |
| `DUEL_CURRENCY` | `EUR` | Reported by `GET /rooms/limits` |
| `VOTE_DEVICE_SECRET` | *(unset)* | Signs anonymous voters' device tokens. Falls back to `AUTH_JWT_SECRET`, then to a per-process secret that does not survive a restart — the server warns when it comes to that |
| `VOTE_ANON_IP_DAILY_LIMIT` | `50` | Anonymous votes per address per UTC day. `0` disables it. This, not the per-device rule, is the real ceiling on anonymous voting |
| `TRUSTED_CLIENT_IP_HEADER` | *(unset)* | Forwarded-address header to believe. Unset believes none. **Behind a proxy this must be set**, or every player collapses onto the proxy's address |

`.env.example` documents these. Compose interpolates them, so editing `.env` and re-running
`docker compose up -d` is enough — and the `Makefile` reads the same file, so `make smoke` targets the
right port. `.env` is gitignored; never commit real values.

The port *inside* the container is fixed at 5000 (the Dockerfile `EXPOSE` and the compose healthcheck
assume it) — change that in `docker-compose.yml`, not `.env`.

## Project layout

```
cmd/api/main.go        # wiring: config -> mongo -> router -> server + graceful shutdown
internal/config/       # environment variables with defaults
internal/db/           # Mongo connect with a bounded ping retry
internal/models/       # Room, Participant, the duel payload, Amount (money in minor units),
                       #   and the vote keys with their canonical form
internal/duel/         # the entry arithmetic and the underround guard — pure, integer-only
internal/auth/         # bearer verification: HS256, plus the insecure development fallback
                       #   (device.go: the identity an ANONYMOUS voter is issued)
internal/store/        # the rooms and votes queries, including the atomic seat claim and the
                       #   unique index that IS the one-vote-per-market rule
                       #   (its own test covers the expiry sweep, which no request path reaches)
internal/api/          # router, handlers, middleware, JSON helpers, and the integration tests
docs/bet-room-api.md   # THE CONTRACT: every route, every wire-format constraint, and why
docs/votes-api.md      # the same for votes: the vote key, identity, caching and the limits
docs/openapi.yaml      # hand-written spec, embedded and served at /openapi.yaml
docker/mongo-init.js   # first-boot seed for the rooms collection
postman/               # collection + environment
scripts/smoke.sh       # end-to-end curl check: a full duel
```

## Adding an endpoint

1. `internal/models/` — the document, with matching `json` and `bson` tags.
2. `internal/store/` — the query, returning `store.ErrNotFound` where it applies.
3. `internal/api/handlers.go` — the handler, using `writeJSON` / `writeError`.
4. `internal/api/router.go` — register it: `mux.HandleFunc("GET /thing/{id}", h.getThing)`.
5. `internal/api/*_test.go` — an integration test against real Mongo. `make test` runs them.
6. `docs/bet-room-api.md`, `docs/openapi.yaml`, the Postman collection and `scripts/smoke.sh` — all
   kept in sync by hand.

Two things that are easy to get wrong: declare **every** field a client sends (`decodeJSON` rejects
unknown ones), and put every precondition of a conditional write into the **filter**, so a request
that should be refused never writes.

## Known gotchas

- **`inviteUrl` points somewhere this service does not serve, and that is correct.** It is the
  *host's* event page plus `?betRoomId=…&betRoomInvite=…`; the widget reads those off the query
  string once the host has landed the joiner on the event. Set `HOST_EVENT_URL_TEMPLATE` to something
  real or the link goes nowhere.
- **Port 27017 on the host** collides with a local `mongod`. Change the host side of the mapping in
  `docker-compose.yml` or stop the local instance.
- **Port 5000 on macOS is taken by AirPlay Receiver** (System Settings → General → AirDrop &
  Handoff → AirPlay Receiver). Docker binds 5000 without error, but requests to `localhost:5000` are
  answered by AirPlay (`403`, `Server: AirTunes`) instead of the API. Either switch AirPlay Receiver
  off, or map a different host port — the container always listens on 5000:

  ```bash
  echo 'API_HOST_PORT=5001' >> .env && docker compose up -d
  ```

  This was hit on the machine this was built on — see *Verified on* below.
- **A bet can belong to at most one room.** A unique index makes a retried create idempotent, so
  re-running the smoke script or the Postman collection needs fresh bet references; both generate
  them per run.
- **`POST /rooms/{id}/read` is a POST, not a GET.** The invite code is a join secret and a query
  string is the one place it must never appear.
- **Do not give each test its own database.** It reads as the tidy choice and it takes the server
  down: forty-odd databases per run is forty-odd sets of WiredTiger files, dropped asynchronously
  while the storage engine holds idle file handles for ten minutes. Past the container's file-
  descriptor limit mongod does not slow down — it panics mid-`createIndexes` and aborts, which
  surfaces in Go as `connection closed unexpectedly by the other side`, a message that says nothing
  about the real cause. Each test binary uses **one** database and empties the collection per test
  (`TestMain` in `internal/api/testenv_test.go`), and `docker-compose.yml` raises the container's
  `nofile` limit to the 64000 mongod's own startup warning asks for.

## Verified on

Run end to end on macOS (Apple silicon) with Colima as the Docker runtime: all three containers
healthy, three seeded duels in `slowhorses.rooms`, `go test ./...` green against a real Mongo
(including twelve simultaneous claims on one seat producing exactly one hold, and twelve
simultaneous votes from one caller producing exactly one vote) over ten consecutive runs, `make test`
green in a container, `scripts/smoke.sh` green across `200 / 201 / 204 / 400 / 401 / 403 / 409`, and
the Postman collection green at 26 requests and 58 assertions. The API was reached on host port 5001 because AirPlay Receiver held 5000 (see *Known
gotchas*).

## Not included yet

- **Server-side eligibility re-checks.** The service cannot tell whether an event has started or
  whether a market is two-way — that needs a sportsbook feed it does not have. The underround guard
  *is* enforced, on create and on acceptance, because it is pure arithmetic over stored odds.
- **Settlement.** Nothing writes `status: settled | void` or `payload.winnerParticipantId`, so a
  filled duel stays filled forever. That is a worker resolving each participant's `betRef` against
  the sportsbook's settlement.
- **Rejecting a vote on a closed, removed or settled market.** Same missing feed as above: the
  service cannot tell whether a market is still open, so a card that closes between render and click
  still records a vote. `docs/votes-api.md` §7.1.
- **A read of the player's own picks.** An open product decision, not an omission — it changes the
  rule from per-device to per-account and needs a merge rule for a vote cast while logged out.
  `docs/votes-api.md` §7.2.
- **Pagination** on the open-rooms list, and **CI**.

`docs/bet-room-api.md` §5.5, §5.6 and §9 size the room ones and say what they block;
`docs/votes-api.md` §7 does the same for the vote ones.

One more, and it is **not in this repository**: `loadVotes$` in the widget must be called once per
mount with every card's market key, not once per card, or the header's `votedToday` races against
itself. That change lives in `sport-space` and has to land with these endpoints.
