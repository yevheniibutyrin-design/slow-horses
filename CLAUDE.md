# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

A Go + MongoDB service serving the **bet room** API: one two-way sportsbook market turned into a
head-to-head duel between two players. The room coordinates them and never holds, moves or settles
money — both sides are ordinary single bets and the room stores only a `betRef` to each.

**[`docs/bet-room-api.md`](docs/bet-room-api.md) is the contract**, with the reasoning behind every
route and wire-format rule. Read the relevant section before changing behaviour; the paths and
response shapes are fixed by already-merged frontend code and are not open for improvement.

## Commands

Everything runs through Docker — **there is no Go toolchain on the host and no `go` on `PATH`.**
`make help` lists the targets; the `Makefile` does `-include .env`, so targets pick up
`API_HOST_PORT`.

| Task | Command |
|---|---|
| Start the stack (api + mongo + swagger-ui) | `make up` |
| Rebuild just the API after an edit | `make restart` (or `make watch` to auto-rebuild) |
| Follow API logs | `make logs` |
| **Lint** | `make vet` (`go vet ./...`) — there is no golangci-lint and no CI |
| **Typecheck / build** | `make build` |
| Full test suite | `make test` |
| End-to-end curl check of a full duel | `make smoke` |
| Ten open duels via the API | `make seed` (additive) / `make seed-fresh` |
| Wipe the DB volume and re-seed from `docker/mongo-init.js` | `make reseed` |
| Mongo shell | `make mongosh` |

### Running a single test

There is no make target; derive it from `test` — start Mongo, then run the toolchain in a container
joined to Mongo's network namespace:

```bash
docker compose up -d mongo
docker run --rm -v "$PWD":/src -w /src -v slow-horses-gomod:/go/pkg/mod \
  --network "container:$(docker compose ps -q mongo)" \
  -e MONGO_TEST_URI=mongodb://localhost:27017 \
  golang:1.27-alpine go test ./internal/api -run 'TestConfirmSeat$' -v
```

`--network container:` is why `MONGO_TEST_URI` is `localhost:27017` and not `mongo:27017`. `-run`
takes an unanchored regex, so drop the `$` to run a whole family of tests by prefix.

**The integration tests skip silently green when Mongo is unreachable** (`mongoSkipMsg` in
`internal/api/testenv_test.go`), so a passing run without `docker compose up -d mongo` proves
nothing — the tell is a `~30s` package time with no test names. `internal/api` and `internal/store`
need Mongo; `internal/duel`, `internal/models` and `internal/auth` are pure unit tests.

`internal/api` uses **one** database per test binary and empties the collection per test. Do not give
each test its own database: that exhausts mongod's file descriptors and it panics mid-`createIndexes`
rather than degrading — see the last bullet of the README's *Known gotchas*.

## Architecture

Stdlib-only HTTP: Go 1.22+ `ServeMux` method+path patterns, no router library. Two direct
dependencies — **mongo-driver v2** (`go.mongodb.org/mongo-driver/v2`, not v1) and `google/uuid`.
`cmd/api/main.go` wires config → mongo → store → handlers → server, plus index creation and the
expiry sweeper goroutine.

Five mechanisms span multiple files and are the things to understand first:

- **Conditional-write refusals are two-step.** `internal/store` puts *every* precondition into the
  filter of a single `FindOneAndUpdate`, so a refused request never writes — and returns an
  undifferentiated `store.ErrNotFound`. The handler then does a follow-up read to decide which status
  it was: `explainSeatRefusal`, `explainConfirmRefusal`, `respondToDuplicateBet` in
  `internal/api/rooms.go`. Atomicity is a requirement (two simultaneous claims on one seat must
  produce exactly one hold), not an optimisation.
- **Optional-auth routes are declared in two places that must agree.** `optionalAuthPatterns` in
  `internal/api/middleware.go` is compared against the pattern *the mux resolved* (which is why
  `authenticating` is handed the mux), so the strings must match those registered in `router.go`
  exactly. Only reads may be listed: anonymous callers share the empty subject, so an anonymous write
  route would let any of them act on another's seat hold.
- **Expiry is evaluated, never merely stored**, in three places: `Room.EffectiveStatus` applied
  lazily by `Handlers.roomView` on every read, `expiresAt > now` in the store filters, and the
  sweeper in `main.go` (housekeeping only — correctness does not depend on it having run). A read path
  that bypasses `roomView` breaks this and serves seats that cannot be taken.
- **Money never touches a float.** `models.Amount` is int64 minor units, marshalling as a bare
  two-decimal JSON number. `internal/duel` is integer-only, and the order is load-bearing: the entry
  is floored from the *rounded* payout, not the exact product (pinned by test).
- **Caller identity comes only from the bearer token.** No payload carries a user identifier. It is
  stored per participant as `subject` with `json:"-"` and never serialised;
  `Room.ParticipantBySubject` refusing to match `""` is what keeps anonymous reads safe. With
  `AUTH_JWT_SECRET` unset an insecure verifier takes the token itself as the identity, so
  `Bearer alice` is a user called alice.

## Wire-format rules that break the widget silently

Full detail in `docs/bet-room-api.md` §1. The widget does `response.json() as T` with no unwrapping
and maps status codes through a closed switch, so each of these fails quietly rather than loudly:

- **Success bodies are not enveloped** — a `{"data": …}` wrapper makes every field `undefined`.
- **Error bodies use `message`, not `error`** (`internal/api/respond.go`); the wrong key drops the
  stated reason and shows a bare HTTP status line.
- **Timestamps are epoch milliseconds as JSON numbers**, never RFC3339 — so `int64`, never
  `time.Time`.
- **`410 Gone` means expired**, for both a lapsed room and a lapsed seat hold. `409` is the instinct
  and renders "already taken" where the truth is "no longer open".

Two rules when adding to a request body: **enumerate every field the client sends**, because
`decodeJSON` sets `DisallowUnknownFields()`; and **keep `payload` as `json.RawMessage`** at the
decode boundary, validating per-strategy afterwards, or any field a future widget adds becomes a 400.

## Repo gotchas

- **`.claude/worktrees/` contains full checkouts of two other branches**, one with an entire `votes`
  API (`internal/api/votes.go`, `internal/models/vote.go`, `internal/auth/device.go`) that does not
  exist here. Any repo-root `grep`/`find` will hit them — exclude that path.
- **Nothing is generated.** `docs/openapi.yaml` (hand-written, embedded via `docs/docs.go`, served at
  `GET /openapi.yaml`), `docs/bet-room-api.md`, `postman/` and `scripts/smoke.sh` are kept in sync by
  hand. A route change touches all four.
- **Port 5000 is AirPlay Receiver on macOS** — it answers `403` with `Server: AirTunes` instead of the
  API. `.env` sets `API_HOST_PORT`; use `$BASE_URL`/`make smoke` rather than a hardcoded
  `localhost:5000`.
- **A bet can belong to at most one room** (unique index on `participants.betRef.id`), which is what
  makes a retried create idempotent. Re-runnable scripts must mint fresh bet references.
- **Not implemented, deliberately:** server-side eligibility re-checks (needs a sportsbook feed) and
  settlement (a filled duel stays filled forever). `docs/bet-room-api.md` §5.5, §5.6 and §9.
