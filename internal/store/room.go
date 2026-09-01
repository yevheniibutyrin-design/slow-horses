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

// ErrNotFound is returned when no room matches the query.
var ErrNotFound = errors.New("room not found")

// Rooms is the data access layer for the rooms collection.
type Rooms struct {
	coll *mongo.Collection
}

// NewRooms wires a store to the "rooms" collection of the given database.
func NewRooms(db *mongo.Database) *Rooms {
	return &Rooms{coll: db.Collection(CollectionName)}
}

// List returns every room. The slice is always non-nil so the API answers "[]"
// rather than "null" on an empty collection.
func (r *Rooms) List(ctx context.Context) ([]models.Room, error) {
	cur, err := r.coll.Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("find rooms: %w", err)
	}

	rooms := make([]models.Room, 0)
	if err := cur.All(ctx, &rooms); err != nil {
		return nil, fmt.Errorf("decode rooms: %w", err)
	}
	return rooms, nil
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

// Create inserts a fully populated room.
func (r *Rooms) Create(ctx context.Context, room models.Room) error {
	if _, err := r.coll.InsertOne(ctx, room); err != nil {
		return fmt.Errorf("insert room: %w", err)
	}
	return nil
}

// DeleteByID removes a room, returning ErrNotFound when there was nothing to delete.
func (r *Rooms) DeleteByID(ctx context.Context, id string) error {
	res, err := r.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("delete room %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// AddParticipant appends a participant to a room, but only when the id, the
// invite roomUrl and isFilled=false all match. Every condition lives in the
// filter, so a join that should be rejected never writes anything.
// ErrNotFound means "no room matched" — the caller decides whether that is a
// 404, a 403 or a 409.
func (r *Rooms) AddParticipant(ctx context.Context, id, roomURL string, p models.Participant) (models.Room, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "roomUrl", Value: roomURL},
		{Key: "isFilled", Value: false},
	}
	update := bson.D{{Key: "$push", Value: bson.D{{Key: "participants", Value: p}}}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var room models.Room
	err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&room)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Room{}, ErrNotFound
	}
	if err != nil {
		return models.Room{}, fmt.Errorf("add participant to room %s: %w", id, err)
	}
	return room, nil
}
