// Package models holds the documents stored in MongoDB.
package models

// Participant is a single player inside a room. Balance is deliberately
// free-form: the contract says Object<any>, so we store whatever comes in.
type Participant struct {
	Name    string         `json:"name" bson:"name"`
	Balance map[string]any `json:"balance" bson:"balance"`
}

// Room is a document in the "rooms" collection. The room's UUID is used
// directly as Mongo's _id, so there is no second identifier to keep in sync.
type Room struct {
	ID           string        `json:"id"           bson:"_id"`
	Name         string        `json:"name"         bson:"name"`
	Participants []Participant `json:"participants" bson:"participants"`
	IsFilled     bool          `json:"isFilled"     bson:"isFilled"`
	IsStarted    bool          `json:"isStarted"    bson:"isStarted"`
	EventID      string        `json:"eventId"      bson:"eventId"`
	RoomURL      string        `json:"roomUrl"      bson:"roomUrl"`
}
