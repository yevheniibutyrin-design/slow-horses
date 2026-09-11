package api_test

// Integration tests for the bet room API, run against a REAL MongoDB.
//
// There is no mock, and deliberately so: the seat claim's whole correctness
// claim is that two simultaneous requests for one seat produce exactly one hold,
// and that is a property of the database's conditional update. A fake would
// assert the test's own logic instead.
//
// The tests skip when Mongo is not reachable, so `go test ./...` still works on
// a machine with nothing running. `make test` starts the compose Mongo first.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/yevheniibutyrin-design/slow-horses/internal/api"
	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

const (
	testInviteWindowMS = 30 * 60 * 1000
	testSeatHoldTTLMS  = 90 * 1000
	testPerDuelMax     = models.Amount(50000) // 500.00
	testDailyLimit     = 10
)

// env is one isolated server: its own database, its own clock.
type env struct {
	t      *testing.T
	server *httptest.Server
	now    *atomic.Int64 // epoch ms, so expiry is testable without sleeping
	rooms  *store.Rooms
}

func mongoURI() string {
	if uri := os.Getenv("MONGO_TEST_URI"); uri != "" {
		return uri
	}
	return "mongodb://localhost:27017"
}

// ONE client and ONE database for the whole test binary, not one per test.
//
// A database per test looks tidier and is a trap: 40-odd databases per run is
// 40-odd sets of WiredTiger files, dropped asynchronously while the storage
// engine keeps idle file handles open for ten minutes. Against the compose
// container's 1024-descriptor soft limit that exhausts the server's file
// handles, and mongod does not degrade — it panics mid-createIndexes and
// aborts, surfacing here as "connection closed unexpectedly by the other side".
// docker-compose.yml raises that limit as well; this keeps the demand sane.
//
// Tests here never run in parallel, so emptying the collection per test gives
// the same isolation for one set of files.
var (
	testClient   *mongo.Client
	testDB       *mongo.Database
	mongoSkipMsg string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run exists so the deferred cleanup below actually runs: os.Exit in TestMain
// would skip it and leave a database behind on every run.
func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI()))
	if err != nil {
		mongoSkipMsg = fmt.Sprintf("mongo not configured (%v); run `make test` or set MONGO_TEST_URI", err)
		return m.Run()
	}
	if err := client.Ping(ctx, nil); err != nil {
		mongoSkipMsg = fmt.Sprintf("mongo not reachable at %s (%v); run `make test` to start it", mongoURI(), err)
		_ = client.Disconnect(context.Background())
		return m.Run()
	}

	// Named per run, and per package: `go test ./...` builds one binary per
	// package and may run them at the same time.
	db := client.Database(fmt.Sprintf("slowhorses_api_test_%d", time.Now().UnixNano()))
	if err := store.NewRooms(db).EnsureIndexes(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ensure indexes: %v\n", err)
		_ = client.Disconnect(context.Background())
		return 1
	}
	testClient, testDB = client, db

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = db.Drop(cleanupCtx)
		_ = client.Disconnect(cleanupCtx)
	}()

	return m.Run()
}

var betCounter atomic.Int64

// uniqueBetID keeps every room's bet reference distinct, since a unique index
// deliberately stops one bet belonging to two rooms.
func uniqueBetID() string {
	return fmt.Sprintf("bet-%d-%d", time.Now().UnixNano(), betCounter.Add(1))
}

func newEnv(t *testing.T) *env {
	t.Helper()

	if testDB == nil {
		t.Skip(mongoSkipMsg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start from an empty collection. The indexes were created once in TestMain
	// and survive this, which is the point — every test still runs against the
	// unique bet-reference index that makes creation idempotent.
	if _, err := testDB.Collection(store.CollectionName).DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatalf("empty the rooms collection: %v", err)
	}
	rooms := store.NewRooms(testDB)

	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixMilli())

	handlers := &api.Handlers{
		Rooms:                rooms,
		OpenAPISpec:          []byte("openapi: 3.0.3\n"),
		HostEventURLTemplate: "https://sportsbook.example/event/{eventId}",
		InviteWindowMS:       testInviteWindowMS,
		SeatHoldTTLMS:        testSeatHoldTTLMS,
		PerDuelMax:           testPerDuelMax,
		DailyLimit:           testDailyLimit,
		Currency:             "EUR",
		Now:                  func() time.Time { return time.UnixMilli(clock.Load()) },
	}

	// InsecureVerifier means the token IS the subject, so "alice" and "bob" below
	// are two different callers. Token verification has its own unit tests.
	server := httptest.NewServer(api.NewRouter(handlers, auth.InsecureVerifier{}, []string{"https://allowed.example"}))

	t.Cleanup(server.Close)

	return &env{t: t, server: server, now: clock, rooms: rooms}
}

// advance moves the server's clock forward by that many MILLISECONDS, so expiry
// is reached without waiting. Milliseconds rather than a time.Duration because
// every instant in this contract is epoch ms, and a Duration invites passing a
// raw ms constant that silently means nanoseconds.
func (e *env) advance(ms int64) { e.now.Add(ms) }

func (e *env) nowMS() int64 { return e.now.Load() }

type response struct {
	status int
	body   []byte
	header http.Header
}

// do performs one request. An empty token sends no Authorization header at all.
func (e *env) do(method, path, token string, body any) response {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return response{status: res.StatusCode, body: raw, header: res.Header}
}

// decode unmarshals a successful response, failing the test on an unexpected status.
func (e *env) decode(res response, wantStatus int, dst any) {
	e.t.Helper()
	if res.status != wantStatus {
		e.t.Fatalf("status = %d, want %d; body: %s", res.status, wantStatus, res.body)
	}
	if dst == nil {
		return
	}
	if err := json.Unmarshal(res.body, dst); err != nil {
		e.t.Fatalf("decode body %s: %v", res.body, err)
	}
}

// ---------------------------------------------------------------------------
// Request builders — the shipped client's shapes
// ---------------------------------------------------------------------------

func participant(label, initials string) map[string]any {
	return map[string]any{
		"label":    label,
		"initials": initials,
		"balances": map[string]any{"eur": 75.5},
	}
}

// createBody is the body roomClient.createRoom sends.
func createBody(eventID string, expiresAt int64, creatorOdd, opponentOdd int, stake string) map[string]any {
	return map[string]any{
		"eventId":     eventID,
		"strategy":    "duel",
		"participant": participant("Maksym K.", "MK"),
		"betRef":      map[string]any{"id": uniqueBetID(), "number": 12345},
		"expiresAt":   expiresAt,
		"payload": map[string]any{
			"marketId":     "total_goals",
			"marketItemId": "total_goals_2.5",
			"creatorSide": map[string]any{
				"outcomeId": "over",
				"odd":       creatorOdd,
				"placement": map[string]any{
					"stake":       json.RawMessage(stake),
					"lineItemId":  "li-12",
					"dataVersion": 7,
				},
			},
			"opponentSide": map[string]any{"outcomeId": "under", "odd": opponentOdd},
			"figures":      map[string]any{"payout": 0, "entry": 0, "pot": 0},
		},
	}
}

// createRoom opens a standard 10.00 @ 1.82 vs 2.05 duel and returns it.
func (e *env) createRoom(token, eventID string) models.Room {
	e.t.Helper()
	res := e.do(http.MethodPost, "/rooms", token,
		createBody(eventID, e.nowMS()+testInviteWindowMS, 182, 205, "10.00"))
	var room models.Room
	e.decode(res, http.StatusCreated, &room)
	return room
}

type seatHold struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expiresAt"`
}

// confirmBody is the PROPOSED confirm body — the one that carries the joiner's
// placement. `withPlacement=false` sends what the client ships today.
func confirmBody(label, initials string, odd int, stake string, withPlacement bool) map[string]any {
	body := map[string]any{
		"participant": participant(label, initials),
		"betRef":      map[string]any{"id": uniqueBetID(), "number": 12346},
	}
	if withPlacement {
		body["odd"] = odd
		body["placement"] = map[string]any{
			"stake":       json.RawMessage(stake),
			"lineItemId":  "li-99",
			"dataVersion": 9,
		}
	}
	return body
}

// doWithRawAuth sends a literal Authorization header, to test malformed ones.
func (e *env) doWithRawAuth(method, path, authorization string) response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, nil)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", authorization)
	return e.send(req)
}

// doWithOrigin sends a request from a browser origin, to test the CORS allowlist.
func (e *env) doWithOrigin(method, path, origin string) response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, nil)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", origin)
	return e.send(req)
}

func (e *env) send(req *http.Request) response {
	e.t.Helper()
	res, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return response{status: res.StatusCode, body: raw, header: res.Header}
}
