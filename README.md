# Slow Horses — Go + MongoDB API boilerplate

A minimal Go backend with MongoDB, running entirely in Docker. Five endpoints exist to prove the
setup works end to end: the server boots, Mongo is reachable, and reads/writes against the `rooms`
collection succeed. No framework, two dependencies (MongoDB driver + `google/uuid`).

## The `rooms` contract

| Field | Type | Notes |
|---|---|---|
| `id` | uuid string | Stored directly as Mongo's `_id` — no second identifier |
| `name` | string | |
| `participants` | array of `{ name: string, balance: object }` | `balance` is free-form; stored as sent |
| `isFilled` | boolean | A stored flag, not derived — there is no capacity field in the contract |
| `isStarted` | boolean | |
| `eventId` | string | |
| `roomUrl` | string | Generated on create as `{PUBLIC_BASE_URL}/join/{id}`; acts as the invite secret |

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
silently skips seeding — so to get the three dummy rooms back:

```bash
make reseed        # = docker compose down -v && docker compose up -d --build
```

Inspect the data directly with `make mongosh` (`docker compose exec mongo mongosh slowhorses`).

## Endpoints

Base URL `http://localhost:5000`.

### `GET /ping`

Liveness check. Returns `200` with the plain-text body `OK`. Does not touch MongoDB.

```bash
curl -i localhost:5000/ping
```

### `GET /rooms`

All rooms as a JSON array (`[]` when empty, never `null`). `200`, or `500` on a database error.

```bash
curl -s localhost:5000/rooms | jq
```

### `PUT /room`

Creates a room. `id` and `roomUrl` are generated server-side. `201` with the created room; `400` if
the body is invalid JSON, contains unknown fields, or is missing `name` / `eventId`.

```bash
curl -s -X PUT localhost:5000/room \
  -H 'content-type: application/json' \
  -d '{"name":"Friday Night Table","eventId":"evt-2026-09-04"}' | jq
```

### `DELETE /room/{id}`

Deletes by room UUID. `204` on success, `404` if there was nothing to delete.

```bash
curl -i -X DELETE localhost:5000/room/<id>
```

### `POST /join`

Adds a participant to a room. **`roomUrl` is the invite secret**: the participant is only added when
the `id` *and* the `roomUrl` match the stored room and the room is not already filled. All three
conditions are part of the database query, so a rejected join never writes anything.

| Status | Meaning |
|---|---|
| `200` | Participant added; returns the updated room |
| `400` | Invalid body, or missing `id` / `roomUrl` / `name` |
| `403` | `roomUrl` does not match this room's invite |
| `404` | No room with that `id` |
| `409` | Room is already filled |

```bash
curl -s -X POST localhost:5000/join \
  -H 'content-type: application/json' \
  -d '{"id":"<id>","roomUrl":"<roomUrl>","name":"Alice","balance":{"usd":100,"chips":20}}' | jq
```

### `GET /openapi.yaml`

The spec embedded in the binary, for external tooling.

### Smoke test

`./scripts/smoke.sh` (or `make smoke`) runs the whole sequence above against a running stack,
including the `403` and `404` negative cases, and fails loudly on the first unexpected status.

## Swagger UI

http://localhost:8081 — served by the `swagger-ui` container, reading `docs/openapi.yaml` from a
read-only mount. "Try it out" posts to `http://localhost:5000` (the single `servers:` entry in the
spec), which works because the API ships a permissive CORS middleware. **That CORS policy is
`Access-Control-Allow-Origin: *` and is for local development only** — tighten it before this goes
anywhere real.

The spec is hand-written and is the single source of truth: there is no code generation, so **edit
`docs/openapi.yaml` by hand whenever a route changes**. The `servers:` dropdown lists `:5000` and
`:5001` — pick the one matching your `API_HOST_PORT`; add an entry for any other host.

## Postman

1. Postman → **Import** → **Files**, and pick *both*
   `postman/slow-horses.postman_collection.json` and `postman/slow-horses.postman_environment.json`.
2. Select the **slow-horses (local docker)** environment (top-right).
3. Run the collection top to bottom, or open it and hit **Run**.

*Create Room* saves `roomId` and `roomUrl` into the environment, so *Join Room*, *Join Room (wrong
roomUrl → 403)* and *Delete Room* need no copy-pasting. Every request asserts its expected status
code, so the Collection Runner is a pass/fail check on the whole API.

With `newman` installed:

```bash
newman run postman/slow-horses.postman_collection.json -e postman/slow-horses.postman_environment.json
```

## Environment variables

| Variable | Default | Notes |
|---|---|---|
| `API_HOST_PORT` | `5000` | Host port mapped to the container's 5000. Override if something owns 5000 |
| `MONGO_URI` | `mongodb://mongo:27017` | The compose service name; use `localhost` only from the host |
| `MONGO_DB` | `slowhorses` | Also drives the seed script's target database, so the two cannot drift. Changing it needs `make reseed` — the existing volume will not seed into a new name |
| `PUBLIC_BASE_URL` | `http://localhost:5000` | Used to build each room's `roomUrl`; follows `API_HOST_PORT` under compose |

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
internal/models/       # Room and Participant documents (json + bson tags)
internal/store/        # the rooms queries; keeps bson out of the handlers
internal/api/          # router, handlers, middleware, JSON helpers
docs/openapi.yaml      # hand-written spec, embedded and served at /openapi.yaml
docker/mongo-init.js   # first-boot seed for the rooms collection
postman/               # collection + environment
scripts/smoke.sh       # end-to-end curl check
```

## Adding an endpoint

1. `internal/models/` — the document, with matching `json` and `bson` tags.
2. `internal/store/` — the query, returning `store.ErrNotFound` where it applies.
3. `internal/api/handlers.go` — the handler, using `writeJSON` / `writeError`.
4. `internal/api/router.go` — register it: `mux.HandleFunc("GET /thing/{id}", h.getThing)`.
5. `docs/openapi.yaml` and the Postman collection — kept in sync by hand.

## Known gotchas

- **The invite link 404s in a browser.** `roomUrl` is `{PUBLIC_BASE_URL}/join/{id}`, but there is no
  `GET /join/{id}` route — the link is for a future frontend. The API entry point is `POST /join`.
- **Port 27017 on the host** collides with a local `mongod`. Change the host side of the mapping in
  `docker-compose.yml` or stop the local instance.
- **Port 5000 on macOS is taken by AirPlay Receiver** (System Settings → General → AirDrop &
  Handoff → AirPlay Receiver). Docker binds 5000 without error, but requests to `localhost:5000` are
  answered by AirPlay (`403`, `Server: AirTunes`) instead of the API. Either switch AirPlay Receiver
  off, or map a different host port — the container always listens on 5000:

  ```bash
  echo 'API_HOST_PORT=5001' >> .env && docker compose up -d
  ```

  `PUBLIC_BASE_URL` follows `API_HOST_PORT`, so generated `roomUrl` values stay correct. This was hit
  on the machine this boilerplate was built on — see *Verified on* below.
- **`PUT /room` is not REST-conventional** (POST would be). It matches the agreed contract on purpose.

## Verified on

Everything above was run end to end on macOS (Apple silicon) with Colima as the Docker runtime:
all three containers healthy, three seeded rooms in `slowhorses.rooms`, and `scripts/smoke.sh` green
across `200 / 201 / 204 / 400 / 403 / 404 / 405 / 409`. The API was reached on host port 5001 because
AirPlay Receiver held 5000 (see *Known gotchas*).

## Not included yet

No tests, no auth, no pagination, no CI — deliberate omissions for a first pass.
