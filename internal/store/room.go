// Package store contains the MongoDB queries for rooms, keeping bson out of the
// HTTP handlers.
package store

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// CollectionName is the collection every query below runs against.
const CollectionName = "rooms"

var (
	// ErrNotFound means no room matched the query. For the conditional writes
	// below it does NOT mean "no such room": every precondition lives in the
	// filter, so a refusal and a missing room are indistinguishable here on
	// purpose. The caller disambiguates with a follow-up read and decides whether
	// that is a 404, a 403, a 409 or a 410.
	ErrNotFound = errors.New("room not found")
	// ErrDuplicateBet means a room already references this bet. Creation places
	// the bet before the room exists, so a retried create must not open a second
	// room for one bet.
	ErrDuplicateBet = errors.New("a room already references this bet")
)

// Rooms is the data access layer for the rooms collection.
type Rooms struct {
	coll *mongo.Collection
}

// NewRooms wires a store to the "rooms" collection of the given database.
func NewRooms(db *mongo.Database) *Rooms {
	return &Rooms{coll: db.Collection(CollectionName)}
}

// EnsureIndexes creates the indexes the room queries depend on. Called once at
// startup; CreateMany is idempotent for identical specifications.
func (r *Rooms) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// The open-rooms-for-an-event query.
			Keys: bson.D{{Key: "eventId", Value: 1}, {Key: "status", Value: 1}},
		},
		{
			// The expiry sweeper, and expiry evaluation on reads.
			Keys: bson.D{{Key: "expiresAt", Value: 1}},
		},
		{
			// "which rooms is this caller in", for the list exclusion and the
			// daily count.
			Keys: bson.D{{Key: "participants.subject", Value: 1}, {Key: "createdAt", Value: -1}},
		},
		{
			// Idempotent creation: one bet can belong to at most one room, so a
			// retried POST /rooms after a timeout cannot open a second.
			Keys:    bson.D{{Key: "participants.betRef.id", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uniq_participant_betref"),
		},
	})
	if err != nil {
		return fmt.Errorf("create room indexes: %w", err)
	}
	return nil
}

// Create inserts a fully populated room, reporting ErrDuplicateBet when one of
// its participants' bets is already referenced by another room.
func (r *Rooms) Create(ctx context.Context, room models.Room) error {
	if _, err := r.coll.InsertOne(ctx, room); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrDuplicateBet
		}
		return fmt.Errorf("insert room: %w", err)
	}
	return nil
}

// FindByID looks a room up by its UUID (stored as _id).
func (r *Rooms) FindByID(ctx context.Context, id string) (models.Room, error) {
	var room models.Room
	err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&room)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Room{}, ErrNotFound
	}
	if err != nil {
		return models.Room{}, fmt.Errorf("find room %s: %w", id, err)
	}
	return room, nil
}

// FindByBetRefID finds the room that already references a bet, so a duplicate
// create can return the room the caller's first attempt made rather than an error.
func (r *Rooms) FindByBetRefID(ctx context.Context, betID string) (models.Room, error) {
	var room models.Room
	err := r.coll.FindOne(ctx, bson.D{{Key: "participants.betRef.id", Value: betID}}).Decode(&room)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Room{}, ErrNotFound
	}
	if err != nil {
		return models.Room{}, fmt.Errorf("find room by bet %s: %w", betID, err)
	}
	return room, nil
}

// ListOpenForEvent returns the rooms on an event that are neither filled nor
// expired, excluding the caller's own rooms and excluding rematch rooms — those
// are joinable by one named opponent only and must never be publicly listed.
//
// The slice is always non-nil, so an event with no open rooms answers "[]"
// rather than "null" and never a 404.
func (r *Rooms) ListOpenForEvent(ctx context.Context, eventID, subject string, now int64) ([]models.Room, error) {
	filter := bson.D{
		{Key: "eventId", Value: eventID},
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{models.StatusOpen, models.StatusReserved}}}},
		// Expiry is evaluated here rather than trusted to the sweeper, or a room
		// reads as open and the widget offers a seat that cannot be taken.
		{Key: "expiresAt", Value: bson.D{{Key: "$gt", Value: now}}},
		{Key: "participants.subject", Value: bson.D{{Key: "$ne", Value: subject}}},
		// nil matches both a missing field and an explicit null.
		{Key: "rematchOfRoomId", Value: nil},
		{Key: "$expr", Value: bson.D{{Key: "$lt", Value: bson.A{
			bson.D{{Key: "$size", Value: "$participants"}}, "$capacity",
		}}}},
	}
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})

	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find open rooms for event %s: %w", eventID, err)
	}
	rooms := make([]models.Room, 0)
	if err := cur.All(ctx, &rooms); err != nil {
		return nil, fmt.Errorf("decode open rooms: %w", err)
	}
	return rooms, nil
}

// ClaimSeat holds a seat for a joiner, atomically.
//
// "Two simultaneous requests for a room with one open seat result in exactly one
// hold" is a requirement, not an optimisation, so every precondition lives in
// this one filter and a rejected claim never writes:
//
//   - the invite code must match — it is the sole authorisation to take a seat;
//   - the room must not have expired;
//   - confirmed participants must be below capacity;
//   - there must be no LIVE hold (a lapsed one is not a hold);
//   - the caller must not already be a participant, which is what stops a creator
//     occupying a second seat in their own room;
//   - a rematch room's seat is restricted to the one permitted subject.
//
// ErrNotFound means none of that held. The caller reads the room to say which.
func (r *Rooms) ClaimSeat(ctx context.Context, roomID, inviteCode, subject string, hold models.SeatHold, now int64) (models.Room, error) {
	filter := bson.D{
		{Key: "_id", Value: roomID},
		{Key: "inviteCode", Value: inviteCode},
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{models.StatusOpen, models.StatusReserved}}}},
		{Key: "expiresAt", Value: bson.D{{Key: "$gt", Value: now}}},
		{Key: "participants.subject", Value: bson.D{{Key: "$ne", Value: subject}}},
		{Key: "$expr", Value: bson.D{{Key: "$lt", Value: bson.A{
			bson.D{{Key: "$size", Value: "$participants"}}, "$capacity",
		}}}},
		// Two independent disjunctions, so they cannot share one "$or" key.
		{Key: "$and", Value: bson.A{
			bson.D{{Key: "$or", Value: bson.A{
				bson.D{{Key: "hold", Value: nil}},
				bson.D{{Key: "hold.expiresAt", Value: bson.D{{Key: "$lte", Value: now}}}},
			}}},
			bson.D{{Key: "$or", Value: bson.A{
				bson.D{{Key: "permittedSubject", Value: nil}},
				bson.D{{Key: "permittedSubject", Value: subject}},
			}}},
		}},
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "hold", Value: hold},
		{Key: "status", Value: models.StatusReserved},
	}}}

	return r.findOneAndUpdate(ctx, filter, update, "claim seat in room "+roomID)
}

// ReleaseHold drops a hold the caller owns, reopening the seat immediately.
//
// Idempotent by design at the handler level: this reports ErrNotFound when there
// is nothing to release, including a hold that already lapsed.
func (r *Rooms) ReleaseHold(ctx context.Context, roomID, holdID, subject string) (models.Room, error) {
	filter := bson.D{
		{Key: "_id", Value: roomID},
		{Key: "hold.id", Value: holdID},
		{Key: "hold.subject", Value: subject},
		// Only a room that is still taking seats can be reopened. Without this,
		// releasing a stale hold id would set a filled, settled or void room back
		// to open — the update below sets the status unconditionally.
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{models.StatusOpen, models.StatusReserved}}}},
	}
	update := bson.D{
		{Key: "$unset", Value: bson.D{{Key: "hold", Value: ""}}},
		{Key: "$set", Value: bson.D{{Key: "status", Value: models.StatusOpen}}},
	}
	return r.findOneAndUpdate(ctx, filter, update, "release hold in room "+roomID)
}

// ConfirmSeat turns the caller's live hold into a confirmed participant, and
// records the opponent side at the price it was actually placed at.
//
// The joiner's bet exists by now, so this is the point the room learns what was
// staked and at what price — the entry was repriced at acceptance, so neither is
// derivable from what was stored at creation.
//
// The room becomes filled when this confirmation reaches capacity. That is
// decided by two mutually exclusive conditional updates rather than an
// aggregation pipeline: each is a plain filter-plus-$set the database evaluates
// literally, and only the caller's own hold can confirm, so no other writer can
// change the participant count between them.
func (r *Rooms) ConfirmSeat(
	ctx context.Context,
	roomID, holdID, subject string,
	now int64,
	participant models.Participant,
	opponentOdd int,
	placement models.SidePlacement,
	figures models.Figures,
) (models.Room, error) {
	base := bson.D{
		{Key: "_id", Value: roomID},
		{Key: "hold.id", Value: holdID},
		{Key: "hold.subject", Value: subject},
		// A hold that lapsed is not a hold, and a room past its expiry takes no
		// more participants — both are a 410, not a silent success.
		{Key: "hold.expiresAt", Value: bson.D{{Key: "$gt", Value: now}}},
		{Key: "expiresAt", Value: bson.D{{Key: "$gt", Value: now}}},
	}

	set := bson.D{
		{Key: "payload.opponentSide.odd", Value: opponentOdd},
		{Key: "payload.opponentSide.placement", Value: placement},
		{Key: "payload.figures", Value: figures},
	}
	update := func(status models.RoomStatus) bson.D {
		return bson.D{
			{Key: "$push", Value: bson.D{{Key: "participants", Value: participant}}},
			{Key: "$set", Value: append(append(bson.D{}, set...), bson.E{Key: "status", Value: status})},
			{Key: "$unset", Value: bson.D{{Key: "hold", Value: ""}}},
		}
	}

	// This confirmation fills the room.
	fills := append(append(bson.D{}, base...), bson.E{
		Key: "$expr", Value: bson.D{{Key: "$eq", Value: bson.A{
			bson.D{{Key: "$size", Value: "$participants"}},
			bson.D{{Key: "$subtract", Value: bson.A{"$capacity", 1}}},
		}}},
	})
	room, err := r.findOneAndUpdate(ctx, fills, update(models.StatusFilled), "confirm seat in room "+roomID)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return room, err
	}

	// A strategy with capacity above two still has seats afterwards, so the room
	// goes back to open rather than filled.
	hasRoomLeft := append(append(bson.D{}, base...), bson.E{
		Key: "$expr", Value: bson.D{{Key: "$lt", Value: bson.A{
			bson.D{{Key: "$size", Value: "$participants"}},
			bson.D{{Key: "$subtract", Value: bson.A{"$capacity", 1}}},
		}}},
	})
	return r.findOneAndUpdate(ctx, hasRoomLeft, update(models.StatusOpen), "confirm seat in room "+roomID)
}

// CountRoomsForSubjectSince counts the rooms a caller is a participant of that
// were created at or after the given instant — "duels played today", and the
// number the daily limit is enforced against.
func (r *Rooms) CountRoomsForSubjectSince(ctx context.Context, subject string, since int64) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.D{
		{Key: "participants.subject", Value: subject},
		{Key: "createdAt", Value: bson.D{{Key: "$gte", Value: since}}},
	})
	if err != nil {
		return 0, fmt.Errorf("count rooms for subject: %w", err)
	}
	return n, nil
}

// ExpireStale marks unfilled rooms past their expiry instant as expired and
// drops any hold still on them, since an unconfirmed hold does not survive room
// expiry.
//
// Reads do not depend on this having run — models.Room.EffectiveStatus applies
// expiry lazily — so this is housekeeping that keeps stored state honest for
// anything querying by status directly.
func (r *Rooms) ExpireStale(ctx context.Context, now int64) (int64, error) {
	res, err := r.coll.UpdateMany(ctx,
		bson.D{
			{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{models.StatusOpen, models.StatusReserved}}}},
			{Key: "expiresAt", Value: bson.D{{Key: "$lte", Value: now}}},
		},
		bson.D{
			{Key: "$set", Value: bson.D{{Key: "status", Value: models.StatusExpired}}},
			{Key: "$unset", Value: bson.D{{Key: "hold", Value: ""}}},
		},
	)
	if err != nil {
		return 0, fmt.Errorf("expire stale rooms: %w", err)
	}
	return res.ModifiedCount, nil
}

func (r *Rooms) findOneAndUpdate(ctx context.Context, filter, update bson.D, what string) (models.Room, error) {
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var room models.Room
	err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&room)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Room{}, ErrNotFound
	}
	if err != nil {
		return models.Room{}, fmt.Errorf("%s: %w", what, err)
	}
	return room, nil
}
