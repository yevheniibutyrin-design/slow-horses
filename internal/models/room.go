// Package models holds the documents stored in MongoDB.
//
// The shapes here implement `docs/bet-room-api.md`. Two things about them are
// deliberate and easy to undo by accident:
//
//   - Timestamps are epoch MILLISECONDS as int64, never time.Time. Go marshals
//     time.Time to RFC3339 and the widget parses these fields as numbers.
//   - A participant's `subject` (the caller identity resolved from their bearer
//     token) is stored but never serialised. It is the only identity the service
//     has, because the client deliberately sends none.
package models

// RoomStatus is the room's lifecycle, as one explicit value rather than a set of
// independent booleans — mutually exclusive states must not be representable at
// once. This replaces the boilerplate's isFilled/isStarted pair.
//
// A room counts as filled in Filled, Settled and Void, and never in Expired:
// that is where a room which did *not* fill ends up, and a room that filled
// before its expiry instant is unaffected by that instant passing.
//
// Whether the *event* has started is deliberately absent. That is a question for
// the sportsbook feed, and duplicating it here would let the two disagree.
type RoomStatus string

const (
	StatusOpen     RoomStatus = "open"
	StatusReserved RoomStatus = "reserved"
	StatusFilled   RoomStatus = "filled"
	StatusExpired  RoomStatus = "expired"
	StatusSettled  RoomStatus = "settled"
	StatusVoid     RoomStatus = "void"
)

// Strategy governs a room: it declares capacity, eligibility rules, entry
// arithmetic and settlement interpretation. Lifecycle behaviour is identical
// across strategies.
type Strategy string

const StrategyDuel Strategy = "duel"

// Capacity reports the confirmed-participant count the strategy accepts, and
// whether the strategy is one this service recognises at all. An unrecognised
// strategy is a validation failure, not a room with a guessed capacity.
func (s Strategy) Capacity() (int, bool) {
	switch s {
	case StrategyDuel:
		return 2, true
	default:
		return 0, false
	}
}

// BetRef references a participant's own ordinary sportsbook single bet — the
// only link the room keeps to money. The bet's stake, odds and payout are
// deliberately not carried here: the room is a coordinator, never a wallet.
type BetRef struct {
	ID     string `json:"id"     bson:"id"`
	Number int64  `json:"number" bson:"number"`
}

// ParticipantPayload is what the client sends about the acting user on create
// and on join.
//
// The label arrives ALREADY DERIVED ("Maksym K." plus initials "MK"). The client
// never sends raw name fields, an email address, an account number or a
// credential — the bearer token identifies the caller, so the payload needs no
// identifier at all. A service that is not a profile store must not start asking
// for more.
type ParticipantPayload struct {
	Label    string `json:"label"    bson:"label"`
	Initials string `json:"initials" bson:"initials"`
	// Balances is opaque: stored and returned unchanged, never computed with and
	// never used to authorise anything. It is carried so a later balance-aware
	// strategy needs no second lookup.
	Balances map[string]any `json:"balances" bson:"balances"`
}

// Participant is a CONFIRMED participant. BetRef is required: a participant
// exists only once their commitment is confirmed, so a hold that lapsed or was
// released cannot be represented as one.
type Participant struct {
	ID                 string `json:"id" bson:"id"`
	ParticipantPayload `bson:",inline"`
	BetRef             BetRef `json:"betRef" bson:"betRef"`
	// Subject is the caller identity from the bearer token. Never serialised:
	// it is the service's own bookkeeping, not part of the contract.
	Subject string `json:"-" bson:"subject"`
}

// SeatHold is a seat held for a joiner who has not confirmed it yet.
//
// A hold is NOT a participant. It lapses on its own at ExpiresAt and leaves no
// trace beyond the seat reopening. It is returned to its own joiner only; every
// other reader sees a held seat as StatusReserved and nothing more.
type SeatHold struct {
	ID        string `json:"id"        bson:"id"`
	Subject   string `json:"-"         bson:"subject"`
	ExpiresAt int64  `json:"expiresAt" bson:"expiresAt"`
}

// Room is a document in the "rooms" collection.
type Room struct {
	ID string `json:"id" bson:"_id"`
	// EventID is the sportsbook event the room concerns. Rooms are discoverable
	// by it.
	EventID  string     `json:"eventId"  bson:"eventId"`
	Strategy Strategy   `json:"strategy" bson:"strategy"`
	Status   RoomStatus `json:"status"   bson:"status"`
	// Capacity is declared by the strategy, never read from the request.
	Capacity int `json:"capacity" bson:"capacity"`
	// Participants lists confirmed participants only. A live hold is not here.
	Participants []Participant `json:"participants" bson:"participants"`

	// ViewerParticipantID tells the reading caller which participant they are.
	// Computed per request, never stored: the client sends no identifier of its
	// own, and matching on the display label is unsafe because labels are not
	// unique.
	ViewerParticipantID string `json:"viewerParticipantId,omitempty" bson:"-"`

	// InviteCode is the join secret and is not derivable from ID. It is omitted
	// from a response unless the caller may hold it — see api.roomView.
	InviteCode string `json:"inviteCode,omitempty" bson:"inviteCode"`
	// InviteURL points at the HOST event page, not at this service. See
	// config.HostEventURLTemplate.
	InviteURL string `json:"inviteUrl,omitempty" bson:"inviteUrl"`

	CreatedAt int64 `json:"createdAt" bson:"createdAt"`
	// ExpiresAt is when an unfilled room stops accepting seats, epoch ms. Reads
	// and the list query evaluate it against now rather than trusting the
	// sweeper to have run.
	ExpiresAt int64 `json:"expiresAt" bson:"expiresAt"`

	// Payload is the strategy's own data. Typed as the duel payload because duel
	// is the only strategy; a second one turns this into a raw document plus a
	// per-strategy decode, which is why handlers already treat it opaquely at the
	// request boundary.
	Payload DuelPayload `json:"payload" bson:"payload"`

	// RematchOfRoomID is set on a rematch: the room this one descends from.
	RematchOfRoomID string `json:"rematchOfRoomId,omitempty" bson:"rematchOfRoomId,omitempty"`
	// PermittedSubject restricts the open seat to one caller. It is resolved from
	// RematchOfRoomID when the rematch is created, rather than looked up when the
	// seat is claimed, because the claim has to stay a single atomic document
	// update and cannot consult a second room inside it.
	PermittedSubject string `json:"-" bson:"permittedSubject,omitempty"`

	// Hold is the live seat hold, embedded so one FindOneAndUpdate can decide a
	// claim. Never serialised; its existence shows as StatusReserved.
	Hold *SeatHold `json:"-" bson:"hold,omitempty"`
}

// ParticipantBySubject returns the participant belonging to a caller, if any.
func (r Room) ParticipantBySubject(subject string) (Participant, bool) {
	if subject == "" {
		return Participant{}, false
	}
	for _, p := range r.Participants {
		if p.Subject == subject {
			return p, true
		}
	}
	return Participant{}, false
}

// HasLiveHold reports whether a hold exists and has not lapsed at now (epoch ms).
func (r Room) HasLiveHold(now int64) bool {
	return r.Hold != nil && r.Hold.ExpiresAt > now
}

// EffectiveStatus applies expiry lazily. A room that passed its expiry instant
// without filling is expired from that instant, whatever the stored status says
// and whether or not the sweeper has run — otherwise a room reads as open and
// the widget offers a seat that cannot be taken.
//
// A room that filled before its expiry instant is unaffected by that instant
// passing, so only Open and Reserved can decay.
func (r Room) EffectiveStatus(now int64) RoomStatus {
	switch r.Status {
	case StatusOpen, StatusReserved:
		if now >= r.ExpiresAt {
			return StatusExpired
		}
		// A lapsed hold leaves no trace beyond the seat reopening, so a room
		// still marked reserved reads as open again once its hold is gone.
		if r.Status == StatusReserved && !r.HasLiveHold(now) {
			return StatusOpen
		}
		return r.Status
	default:
		return r.Status
	}
}
