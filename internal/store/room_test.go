package store_test

// Integration tests for the store layer, run against a REAL MongoDB.
//
// internal/api's tests already drive every query the HTTP surface reaches, so
// this file covers only what no request path touches: ExpireStale, which runs
// exclusively from the sweeper goroutine in cmd/api. Without a test here its
// update document has never executed, and a misspelled field name in it would
// fail silently — UpdateMany reports zero modified rather than an error.

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

var betCounter atomic.Int64

func mongoURI() string {
	if uri := os.Getenv("MONGO_TEST_URI"); uri != "" {
		return uri
	}
	return "mongodb://localhost:27017"
}

// One client and one database for the whole test binary, for the reasons spelled
// out in internal/api/testenv_test.go: a database per test exhausts the compose
// container's file descriptors and panics the server rather than failing a test.
var (
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

	db := client.Database(fmt.Sprintf("slowhorses_store_test_%d", time.Now().UnixNano()))
	if err := store.NewRooms(db).EnsureIndexes(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ensure indexes: %v\n", err)
		_ = client.Disconnect(context.Background())
		return 1
	}
	testDB = db

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = db.Drop(cleanupCtx)
		_ = client.Disconnect(cleanupCtx)
	}()

	return m.Run()
}

// newStore hands back a store over an empty collection. It skips rather than
// fails when Mongo is not running, so `go test ./...` still works on a machine
// with nothing up; `make test` starts the compose Mongo first.
func newStore(t *testing.T) *store.Rooms {
	t.Helper()

	if testDB == nil {
		t.Skip(mongoSkipMsg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := testDB.Collection(store.CollectionName).DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatalf("empty the rooms collection: %v", err)
	}
	return store.NewRooms(testDB)
}

// room builds a stored room. Bet references stay unique because the unique index
// deliberately stops one bet belonging to two rooms.
func room(id string, status models.RoomStatus, expiresAt int64, hold *models.SeatHold) models.Room {
	return models.Room{
		ID:       id,
		EventID:  "evt-store-test",
		Strategy: models.StrategyDuel,
		Status:   status,
		Capacity: 2,
		Participants: []models.Participant{{
			ID:                 "p-" + id,
			ParticipantPayload: models.ParticipantPayload{Label: "Maksym K.", Initials: "MK"},
			BetRef:             models.BetRef{ID: fmt.Sprintf("bet-%s-%d", id, betCounter.Add(1)), Number: 1},
			Subject:            "alice",
		}},
		InviteCode: "CODE" + id,
		CreatedAt:  expiresAt - 1000,
		ExpiresAt:  expiresAt,
		Hold:       hold,
	}
}

func TestExpireStale(t *testing.T) {
	rooms := newStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	seed := []models.Room{
		room("lapsed-open", models.StatusOpen, now-1, nil),
		room("lapsed-held", models.StatusReserved, now-1, &models.SeatHold{
			ID: "hold-1", Subject: "bob", ExpiresAt: now + 60_000,
		}),
		room("still-open", models.StatusOpen, now+60_000, nil),
		// A room that filled before its expiry instant is unaffected by that
		// instant passing: it is running to settlement, not abandoned.
		room("filled-past-expiry", models.StatusFilled, now-1, nil),
	}
	for _, r := range seed {
		if err := rooms.Create(ctx, r); err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}

	n, err := rooms.ExpireStale(ctx, now)
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n != 2 {
		t.Fatalf("ExpireStale modified %d rooms, want 2 (the two lapsed ones)", n)
	}

	// Read the rooms back rather than trusting the count: a misspelled field in
	// the update document is exactly the failure this test exists to catch, and
	// ModifiedCount would not reveal it.
	want := map[string]models.RoomStatus{
		"lapsed-open":        models.StatusExpired,
		"lapsed-held":        models.StatusExpired,
		"still-open":         models.StatusOpen,
		"filled-past-expiry": models.StatusFilled,
	}
	for id, wantStatus := range want {
		got, err := rooms.FindByID(ctx, id)
		if err != nil {
			t.Fatalf("FindByID %s: %v", id, err)
		}
		if got.Status != wantStatus {
			t.Errorf("room %s stored status = %q, want %q", id, got.Status, wantStatus)
		}
	}

	// An unconfirmed hold does not survive room expiry — the seat it held is
	// gone with the room, so leaving it stored would misreport the room as
	// reserved to anything querying the collection directly.
	expired, err := rooms.FindByID(ctx, "lapsed-held")
	if err != nil {
		t.Fatalf("FindByID lapsed-held: %v", err)
	}
	if expired.Hold != nil {
		t.Errorf("the hold survived expiry: %+v", expired.Hold)
	}

	// Idempotent: a second sweep over the same instant has nothing left to do.
	n, err = rooms.ExpireStale(ctx, now)
	if err != nil {
		t.Fatalf("second ExpireStale: %v", err)
	}
	if n != 0 {
		t.Errorf("second sweep modified %d rooms, want 0", n)
	}
}
