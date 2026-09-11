package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/yevheniibutyrin-design/slow-horses/internal/duel"
	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

// ---------------------------------------------------------------------------
// Request shapes
//
// Every field a client sends must be declared here: decodeJSON rejects unknown
// fields, so an undeclared field is a 400 rather than a silent no-op.
// ---------------------------------------------------------------------------

type participantRequest struct {
	Label    string         `json:"label"`
	Initials string         `json:"initials"`
	Balances map[string]any `json:"balances"`
}

// validate returns the stored form, or the message explaining the refusal.
//
// Note what is NOT accepted: there is no name field, no email, no account number
// and no identifier. The label arrives already derived, and the bearer token
// identifies the caller, so the payload needs nothing else. A service that is
// not a profile store must not start accepting more.
func (p participantRequest) validate() (models.ParticipantPayload, string) {
	label, initials := trimmed(p.Label), trimmed(p.Initials)
	if label == "" {
		return models.ParticipantPayload{}, "participant.label is required"
	}
	if initials == "" {
		return models.ParticipantPayload{}, "participant.initials is required"
	}
	balances := p.Balances
	if balances == nil {
		// Stored as supplied, and an absent payload is an empty one rather than
		// null, so a reader never has to special-case it.
		balances = map[string]any{}
	}
	return models.ParticipantPayload{Label: label, Initials: initials, Balances: balances}, ""
}

type betRefRequest struct {
	ID     string `json:"id"`
	Number int64  `json:"number"`
}

func (b betRefRequest) validate(field string) (models.BetRef, string) {
	id := trimmed(b.ID)
	if id == "" {
		return models.BetRef{}, field + ".id is required"
	}
	return models.BetRef{ID: id, Number: b.Number}, ""
}

type placementRequest struct {
	Stake       models.Amount `json:"stake"`
	LineItemID  string        `json:"lineItemId"`
	DataVersion int           `json:"dataVersion"`
}

type sideRequest struct {
	OutcomeID string            `json:"outcomeId"`
	Odd       int               `json:"odd"`
	Placement *placementRequest `json:"placement"`
}

type figuresRequest struct {
	Payout models.Amount `json:"payout"`
	Entry  models.Amount `json:"entry"`
	Pot    models.Amount `json:"pot"`
}

type duelPayloadRequest struct {
	MarketID     string         `json:"marketId"`
	MarketItemID string         `json:"marketItemId"`
	CreatorSide  sideRequest    `json:"creatorSide"`
	OpponentSide sideRequest    `json:"opponentSide"`
	Figures      figuresRequest `json:"figures"`
	// WinnerParticipantID is accepted and ignored: it is written only by
	// settlement, which no route performs.
	WinnerParticipantID string `json:"winnerParticipantId"`
}

// decodeDuelPayload reads the strategy payload out of the raw message the outer
// request carried.
//
// Decoded LENIENTLY on purpose — unknown fields are dropped rather than
// rejected. The outer decoder forbids unknown fields, which is right for a fixed
// request shape and wrong for a payload whose whole job is to carry a strategy's
// own data: a field a future widget version adds would otherwise 400 the route.
// Dropping is the lesser failure, and it is the reason `payload` is a
// json.RawMessage rather than a typed struct at the request level.
func decodeDuelPayload(raw json.RawMessage) (models.DuelPayload, string) {
	if len(raw) == 0 {
		return models.DuelPayload{}, "payload is required"
	}
	var req duelPayloadRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return models.DuelPayload{}, "payload is not valid for strategy duel: " + err.Error()
	}

	switch {
	case trimmed(req.MarketID) == "":
		return models.DuelPayload{}, "payload.marketId is required"
	case trimmed(req.MarketItemID) == "":
		// The selection is a triple. A market holds many items, so a market id
		// alone cannot tell Over 2.5 from Over 3.5.
		return models.DuelPayload{}, "payload.marketItemId is required"
	case trimmed(req.CreatorSide.OutcomeID) == "":
		return models.DuelPayload{}, "payload.creatorSide.outcomeId is required"
	case trimmed(req.OpponentSide.OutcomeID) == "":
		return models.DuelPayload{}, "payload.opponentSide.outcomeId is required"
	case req.CreatorSide.OutcomeID == req.OpponentSide.OutcomeID:
		return models.DuelPayload{}, "the two sides of a duel must be different outcomes"
	case req.CreatorSide.Odd < duel.MinRawOdd:
		return models.DuelPayload{}, "payload.creatorSide.odd must be a raw integer price of at least 100 (1.00)"
	case req.OpponentSide.Odd < duel.MinRawOdd:
		return models.DuelPayload{}, "payload.opponentSide.odd must be a raw integer price of at least 100 (1.00)"
	case req.CreatorSide.Placement == nil:
		// Always placed: a duel room is created from a bet that already exists,
		// because the creator's stake is only known once the slip has placed it.
		return models.DuelPayload{}, "payload.creatorSide.placement is required"
	case req.CreatorSide.Placement.Stake <= 0:
		return models.DuelPayload{}, "payload.creatorSide.placement.stake must be positive"
	}

	payload := models.DuelPayload{
		MarketID:     trimmed(req.MarketID),
		MarketItemID: trimmed(req.MarketItemID),
		CreatorSide: models.Side{
			SideOffer: models.SideOffer{OutcomeID: trimmed(req.CreatorSide.OutcomeID), Odd: req.CreatorSide.Odd},
			Placement: &models.SidePlacement{
				Stake:       req.CreatorSide.Placement.Stake,
				LineItemID:  trimmed(req.CreatorSide.Placement.LineItemID),
				DataVersion: req.CreatorSide.Placement.DataVersion,
			},
		},
		OpponentSide: models.Side{
			SideOffer: models.SideOffer{OutcomeID: trimmed(req.OpponentSide.OutcomeID), Odd: req.OpponentSide.Odd},
		},
	}
	// Figures are not taken from the request. They are derived from the stake and
	// the two prices by the one implementation of that arithmetic, so the service
	// never stores a client's claim about money it can compute itself.
	return payload, ""
}

// ---------------------------------------------------------------------------
// POST /rooms
// ---------------------------------------------------------------------------

type createRoomRequest struct {
	EventID     string             `json:"eventId"`
	Strategy    models.Strategy    `json:"strategy"`
	Participant participantRequest `json:"participant"`
	BetRef      betRefRequest      `json:"betRef"`
	ExpiresAt   int64              `json:"expiresAt"`
	Payload     json.RawMessage    `json:"payload"`
}

func (h *Handlers) createRoom(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	subject := subjectFrom(r)

	eventID := trimmed(req.EventID)
	if eventID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "eventId is required")
		return
	}
	capacity, known := req.Strategy.Capacity()
	if !known {
		writeError(w, http.StatusBadRequest, codeUnknownStrategy,
			"unknown strategy "+string(req.Strategy))
		return
	}
	participant, msg := req.Participant.validate()
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	betRef, msg := req.BetRef.validate("betRef")
	if msg != "" {
		// Required, not optional. Creation places the bet before the room exists,
		// so the reference is always in hand — and a participant whose only link
		// to money is empty is the thing this contract exists to prevent.
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	payload, msg := decodeDuelPayload(req.Payload)
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	figures, err := duel.DeriveFigures(
		payload.CreatorSide.Placement.Stake, payload.CreatorSide.Odd, payload.OpponentSide.Odd)
	if err != nil {
		h.writeFiguresError(w, err)
		return
	}

	now := h.nowMS()
	if refused := h.checkDuelLimits(r, subject, payload.CreatorSide.Placement.Stake, now); refused != nil {
		writeError(w, refused.status, refused.code, refused.message)
		return
	}

	expiresAt := h.clampExpiry(req.ExpiresAt, now)
	if expiresAt <= now {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"expiresAt is in the past; a room must be open for some time to be joinable")
		return
	}

	inviteCode, err := newInviteCode()
	if err != nil {
		internalError(w, "generate invite code", err)
		return
	}

	roomID := uuid.NewString()
	creatorID := uuid.NewString()
	payload.CreatorSide.Placement.ParticipantID = creatorID
	payload.Figures = figures

	room := models.Room{
		ID:       roomID,
		EventID:  eventID,
		Strategy: req.Strategy,
		Status:   models.StatusOpen,
		Capacity: capacity,
		Participants: []models.Participant{{
			ID:                 creatorID,
			ParticipantPayload: participant,
			BetRef:             betRef,
			Subject:            subject,
		}},
		InviteCode: inviteCode,
		InviteURL:  buildInviteURL(h.HostEventURLTemplate, eventID, roomID, inviteCode),
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
		Payload:    payload,
	}

	switch err := h.Rooms.Create(r.Context(), room); {
	case err == nil:
		writeJSON(w, http.StatusCreated, h.roomView(room, subject, true))
	case errors.Is(err, store.ErrDuplicateBet):
		// A retried create after a timeout must not open a second room for one
		// bet, so hand back the room the first attempt made.
		h.respondToDuplicateBet(w, r, betRef.ID, subject)
	default:
		internalError(w, "create room", err)
	}
}

func (h *Handlers) respondToDuplicateBet(w http.ResponseWriter, r *http.Request, betID, subject string) {
	existing, err := h.Rooms.FindByBetRefID(r.Context(), betID)
	if err != nil {
		// The other room went away between the insert and this read, so the
		// caller's retry can now succeed.
		writeError(w, http.StatusConflict, codeInvalidRequest, "this bet is already used by another room, please retry")
		return
	}
	if _, ours := existing.ParticipantBySubject(subject); !ours {
		writeError(w, http.StatusConflict, codeInvalidRequest, "this bet already belongs to another room")
		return
	}
	writeJSON(w, http.StatusOK, h.roomView(existing, subject, true))
}

// clampExpiry bounds what a room's expiry may be.
//
// The client sends its own min(createdAt + inviteWindow, eventStart) so that
// "cannot be accepted after kick-off" is a property of the data rather than a
// check someone forgets. The client is not a trust boundary, so the window is
// re-applied here and the result may only ever be SHORTER than what was asked.
//
// The eventStart half cannot be enforced here: it needs a sportsbook feed this
// service does not have. See `docs/bet-room-api.md` §5.5 — until that lands, a
// crafted request can open a duel on an event that has already started.
func (h *Handlers) clampExpiry(requested, now int64) int64 {
	ceiling := now + h.InviteWindowMS
	if requested <= 0 || requested > ceiling {
		return ceiling
	}
	return requested
}

type refusal struct {
	status  int
	code    string
	message string
}

// checkDuelLimits enforces the two ceilings a room can refuse a duel for. The
// room service is the only component that knows how many duels a caller opened
// today, so the daily count is enforced here regardless of what the widget knows.
func (h *Handlers) checkDuelLimits(r *http.Request, subject string, stake models.Amount, now int64) *refusal {
	if h.PerDuelMax > 0 && stake > h.PerDuelMax {
		return &refusal{http.StatusConflict, codePerDuelMax,
			"this stake is above the maximum of " + h.PerDuelMax.String() + " per duel"}
	}
	if h.DailyLimit <= 0 {
		return nil
	}
	played, err := h.Rooms.CountRoomsForSubjectSince(r.Context(), subject, startOfUTCDay(now))
	if err != nil {
		// A limit that cannot be checked is not a reason to refuse a legitimate
		// duel; log it and let the duel through.
		log.Printf("api: count duels for daily limit: %v", err)
		return nil
	}
	if played >= int64(h.DailyLimit) {
		return &refusal{http.StatusConflict, codeDailyLimit, "you have reached today's duel limit"}
	}
	return nil
}

// startOfUTCDay is the boundary the daily duel count resets on. UTC rather than
// a player's local day, because the service has no timezone for them.
func startOfUTCDay(nowMS int64) int64 {
	t := time.UnixMilli(nowMS).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
}

func (h *Handlers) writeFiguresError(w http.ResponseWriter, err error) {
	if errors.Is(err, duel.ErrUnderround) {
		// The arithmetic stays correct — each side is an independent single bet —
		// but the pot would come out below the winner's take, which reads as
		// broken to a player. Refused rather than presented.
		writeError(w, http.StatusConflict, codeUnderround,
			"these two prices leave the pot below the winner's take, so this market cannot be duelled")
		return
	}
	writeError(w, http.StatusBadRequest, codeInvalidRequest,
		"the duel figures cannot be derived from this stake and these prices")
}

// ---------------------------------------------------------------------------
// GET /rooms?eventId=...&status=open
// ---------------------------------------------------------------------------

func (h *Handlers) listOpenRooms(w http.ResponseWriter, r *http.Request) {
	eventID := trimmed(r.URL.Query().Get("eventId"))
	if eventID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "eventId is required")
		return
	}
	if status := trimmed(r.URL.Query().Get("status")); status != "" && status != string(models.StatusOpen) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"only status=open can be listed for an event")
		return
	}

	subject := subjectFrom(r)
	rooms, err := h.Rooms.ListOpenForEvent(r.Context(), eventID, subject, h.nowMS())
	if err != nil {
		internalError(w, "list open rooms", err)
		return
	}

	// The invite code IS returned here. Listing a duel on an event is the offer
	// to anyone viewing that event, so the disclosure is deliberate: without it
	// the idle list could show a duel nobody on the list could take. The code
	// stays the secret for the invite-link flow, and rematch rooms are excluded
	// from this query entirely, so the one genuinely restricted case is
	// unaffected. See `docs/bet-room-api.md` §2.5.
	views := make([]models.Room, 0, len(rooms))
	for _, room := range rooms {
		views = append(views, h.roomView(room, subject, true))
	}
	writeJSON(w, http.StatusOK, views)
}

// ---------------------------------------------------------------------------
// GET /rooms/limits
// ---------------------------------------------------------------------------

type limitsResponse struct {
	PerDuelMax  models.Amount `json:"perDuelMax"`
	DailyLimit  int           `json:"dailyLimit"`
	PlayedToday int           `json:"playedToday"`
	Currency    string        `json:"currency"`
}

func (h *Handlers) roomLimits(w http.ResponseWriter, r *http.Request) {
	subject := subjectFrom(r)
	played, err := h.Rooms.CountRoomsForSubjectSince(r.Context(), subject, startOfUTCDay(h.nowMS()))
	if err != nil {
		internalError(w, "count duels played today", err)
		return
	}
	writeJSON(w, http.StatusOK, limitsResponse{
		PerDuelMax:  h.PerDuelMax,
		DailyLimit:  h.DailyLimit,
		PlayedToday: int(played),
		Currency:    h.Currency,
	})
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/read
// ---------------------------------------------------------------------------

type readRoomRequest struct {
	InviteCode string `json:"inviteCode"`
}

// readRoom is a POST, not a GET, and deliberately so: the invite code is a join
// secret, and a query string is the one place it must never appear.
func (h *Handlers) readRoom(w http.ResponseWriter, r *http.Request) {
	var req readRoomRequest
	// A participant reads their own room without sending anything at all.
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	room, err := h.Rooms.FindByID(r.Context(), r.PathValue("roomId"))
	if isNotFound(err) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, "read room", err)
		return
	}

	subject := subjectFrom(r)
	_, isParticipant := room.ParticipantBySubject(subject)
	holdsCode := room.InviteCode != "" && trimmed(req.InviteCode) == room.InviteCode
	if !isParticipant && !holdsCode {
		writeError(w, http.StatusForbidden, codeForbidden,
			"you are neither a participant of this room nor a holder of its invite code")
		return
	}
	// A code holder already has the code, and a participant needs it to share the
	// invite, so returning it here tells neither of them anything new.
	writeJSON(w, http.StatusOK, h.roomView(room, subject, true))
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/seat
// ---------------------------------------------------------------------------

type reserveSeatRequest struct {
	InviteCode string `json:"inviteCode"`
}

// reserveSeat holds a seat before the joiner commits anything.
//
// Joining is two steps because a joiner's entry is placed on their behalf at a
// fixed amount: the seat has to be theirs before any bet exists, or a rejected
// join leaves a stake they never asked to place on its own.
func (h *Handlers) reserveSeat(w http.ResponseWriter, r *http.Request) {
	var req reserveSeatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	inviteCode := trimmed(req.InviteCode)
	if inviteCode == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "inviteCode is required")
		return
	}

	roomID := r.PathValue("roomId")
	subject := subjectFrom(r)
	now := h.nowMS()
	hold := models.SeatHold{
		ID:        uuid.NewString(),
		Subject:   subject,
		ExpiresAt: now + h.SeatHoldTTLMS,
	}

	_, err := h.Rooms.ClaimSeat(r.Context(), roomID, inviteCode, subject, hold, now)
	switch {
	case err == nil:
		// Returned to this joiner only. Every other reader sees the hold as the
		// room's reserved status and nothing more.
		writeJSON(w, http.StatusCreated, models.SeatHold{ID: hold.ID, ExpiresAt: hold.ExpiresAt})
	case isNotFound(err):
		h.explainSeatRefusal(w, r, roomID, subject, inviteCode, now)
	default:
		internalError(w, "reserve seat", err)
	}
}

// explainSeatRefusal turns "the atomic claim matched nothing" into an honest
// status. The claim cannot report WHICH precondition failed — that is the price
// of putting them all in one filter — so one follow-up read answers it.
//
// The order matters. A wrong invite code is answered first and identically
// whether or not the room could have been joined, so a caller without the code
// learns nothing about the room's state from the status they get back.
func (h *Handlers) explainSeatRefusal(w http.ResponseWriter, r *http.Request, roomID, subject, inviteCode string, now int64) {
	room, err := h.Rooms.FindByID(r.Context(), roomID)
	if isNotFound(err) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, "explain seat refusal", err)
		return
	}

	switch {
	case room.InviteCode != inviteCode:
		writeError(w, http.StatusForbidden, codeForbidden, "that invite code does not match this room")
	case room.PermittedSubject != "" && room.PermittedSubject != subject:
		writeError(w, http.StatusForbidden, codeForbidden,
			"this rematch is open only to the opponent of the duel it came from")
	case room.EffectiveStatus(now) == models.StatusExpired:
		writeError(w, http.StatusGone, codeRoomExpired, "this duel is no longer open")
	case isTerminal(room.Status):
		writeError(w, http.StatusConflict, codeRoomFilled, "this duel is no longer accepting players")
	default:
		if _, already := room.ParticipantBySubject(subject); already {
			writeError(w, http.StatusConflict, codeAlreadyJoined, "you are already in this duel")
			return
		}
		if len(room.Participants) >= room.Capacity {
			writeError(w, http.StatusConflict, codeRoomFilled, "this duel has already been taken")
			return
		}
		if room.HasLiveHold(now) {
			writeError(w, http.StatusConflict, codeSeatHeld, "someone else is taking this duel right now")
			return
		}
		// The room changed between the claim and this read; the client can retry.
		writeError(w, http.StatusConflict, codeSeatHeld, "could not take this seat, please retry")
	}
}

func isTerminal(status models.RoomStatus) bool {
	switch status {
	case models.StatusFilled, models.StatusSettled, models.StatusVoid:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// DELETE /rooms/{roomId}/seat/{holdId}
// ---------------------------------------------------------------------------

// releaseSeat drops a hold the caller owns, reopening the seat at once rather
// than waiting out its TTL.
//
// Idempotent: releasing a hold that already lapsed, or that was already
// released, is still a 204, because the post-condition the caller wants — that
// seat is not held by me — is already true. A hold that belongs to someone else
// is a 403, since that post-condition is not theirs to ask for.
func (h *Handlers) releaseSeat(w http.ResponseWriter, r *http.Request) {
	roomID, holdID := r.PathValue("roomId"), r.PathValue("holdId")
	subject := subjectFrom(r)

	_, err := h.Rooms.ReleaseHold(r.Context(), roomID, holdID, subject)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
		return
	case !isNotFound(err):
		internalError(w, "release seat", err)
		return
	}

	room, err := h.Rooms.FindByID(r.Context(), roomID)
	if isNotFound(err) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, "release seat lookup", err)
		return
	}
	if room.Hold != nil && room.Hold.ID == holdID && room.Hold.Subject != subject {
		writeError(w, http.StatusForbidden, codeForbidden, "that seat is held by someone else")
		return
	}
	// No such hold on this room: already released, already confirmed, or swept
	// away with the room's expiry. Nothing left to do.
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/seat/{holdId}/confirm
// ---------------------------------------------------------------------------

type confirmSeatRequest struct {
	Participant participantRequest `json:"participant"`
	BetRef      betRefRequest      `json:"betRef"`
	// Odd and Placement are the proposed addition for gap G1, and are optional
	// until the widget's client sends them.
	//
	// Without them the room cannot record what the joiner actually staked or at
	// what price, and the service cannot derive either: the joiner is placed at
	// the REPRICED acceptance-time odd, not the price stored at creation. When
	// they are absent this handler falls back to the amount the joiner was told
	// to stake and logs that it did — see confirmSeat.
	Odd       *int              `json:"odd"`
	Placement *placementRequest `json:"placement"`
}

func (h *Handlers) confirmSeat(w http.ResponseWriter, r *http.Request) {
	var req confirmSeatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	participant, msg := req.Participant.validate()
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	betRef, msg := req.BetRef.validate("betRef")
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	roomID, holdID := r.PathValue("roomId"), r.PathValue("holdId")
	subject := subjectFrom(r)
	now := h.nowMS()

	room, err := h.Rooms.FindByID(r.Context(), roomID)
	if isNotFound(err) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, "confirm seat lookup", err)
		return
	}

	// The price the joiner was actually placed at. The creator's own price is
	// locked — their bet already exists — so the pair to evaluate is (placed
	// creator odd, accepted opponent odd), not two current prices.
	opponentOdd := room.Payload.OpponentSide.Odd
	if req.Odd != nil {
		opponentOdd = *req.Odd
	}
	if opponentOdd < duel.MinRawOdd {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"odd must be a raw integer price of at least 100 (1.00)")
		return
	}

	// The eligibility guard runs at acceptance as well as creation: a pair that
	// was eligible when the duel opened can drift into an underround while it
	// waits, and then acceptance is refused rather than repriced.
	figures, err := duel.DeriveFigures(room.Payload.CreatorSide.Placement.Stake, room.Payload.CreatorSide.Odd, opponentOdd)
	if err != nil {
		h.writeFiguresError(w, err)
		return
	}

	participantID := uuid.NewString()
	placement := models.SidePlacement{ParticipantID: participantID}
	if req.Placement != nil {
		placement.Stake = req.Placement.Stake
		placement.LineItemID = trimmed(req.Placement.LineItemID)
		placement.DataVersion = req.Placement.DataVersion
	} else {
		// Gap G1: the shipped client sends neither the stake nor the two
		// feed-sourced fields. The derived entry is what the joiner was told to
		// place, so it is the best available stand-in — but lineItemId and
		// dataVersion are simply unknown, and a reader can tell because they are
		// empty rather than plausible.
		placement.Stake = figures.Entry
		log.Printf("api: confirm seat in room %s: no placement supplied, recording the derived entry %s"+
			" and leaving lineItemId/dataVersion empty (gap G1)", roomID, figures.Entry)
	}
	if placement.Stake <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "placement.stake must be positive")
		return
	}

	confirmed, err := h.Rooms.ConfirmSeat(r.Context(), roomID, holdID, subject, now,
		models.Participant{
			ID:                 participantID,
			ParticipantPayload: participant,
			BetRef:             betRef,
			Subject:            subject,
		}, opponentOdd, placement, figures)

	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, h.roomView(confirmed, subject, true))
	case isNotFound(err):
		h.explainConfirmRefusal(w, r, roomID, holdID, subject, now)
	case errors.Is(err, store.ErrDuplicateBet):
		writeError(w, http.StatusConflict, codeInvalidRequest, "this bet already belongs to another room")
	default:
		internalError(w, "confirm seat", err)
	}
}

// explainConfirmRefusal turns "the atomic confirm matched nothing" into an
// honest status, the same way explainSeatRefusal does for a claim.
//
// It re-reads rather than reusing the copy confirmSeat already has: a hold that
// lapsed between that read and the write still looks live in the stale copy, and
// the caller would be told the duel was taken when the truth is that their hold
// expired. The two are different sentences and different widget states.
func (h *Handlers) explainConfirmRefusal(w http.ResponseWriter, r *http.Request, roomID, holdID, subject string, now int64) {
	room, err := h.Rooms.FindByID(r.Context(), roomID)
	if isNotFound(err) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, "explain confirm refusal", err)
		return
	}

	switch {
	case room.Hold == nil || room.Hold.ID != holdID:
		// Never existed, already confirmed, released, or swept away with expiry.
		writeError(w, http.StatusGone, codeHoldExpired, "that seat hold is no longer valid")
	case room.Hold.Subject != subject:
		writeError(w, http.StatusForbidden, codeForbidden, "that seat is held by someone else")
	case room.Hold.ExpiresAt <= now:
		writeError(w, http.StatusGone, codeHoldExpired, "that seat hold has lapsed and the seat has reopened")
	case room.ExpiresAt <= now:
		writeError(w, http.StatusGone, codeRoomExpired, "this duel is no longer open")
	default:
		writeError(w, http.StatusConflict, codeRoomFilled, "this duel is no longer accepting players")
	}
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/rematch
// ---------------------------------------------------------------------------

type rematchRequest struct {
	Participant participantRequest `json:"participant"`
	// BetRef and ExpiresAt are declared by the widget's own request type and
	// passed by its epic, but dropped by its client before the request is sent
	// (gap G2). BetRef is required here anyway: a rematch participant with no
	// bet reference has no link to money, which is the defect the create path
	// already had fixed once — and every such room would collide on the unique
	// index over an empty bet id. Failing loudly beats storing that.
	BetRef    *betRefRequest  `json:"betRef"`
	ExpiresAt *int64          `json:"expiresAt"`
	Payload   json.RawMessage `json:"payload"`
}

// rematch opens a new duel restricted to the opponent of the duel it came from.
func (h *Handlers) rematch(w http.ResponseWriter, r *http.Request) {
	var req rematchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	participant, msg := req.Participant.validate()
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	if req.BetRef == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"betRef is required: a rematch is opened from a bet the sender has already placed")
		return
	}
	betRef, msg := req.BetRef.validate("betRef")
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	payload, msg := decodeDuelPayload(req.Payload)
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	subject := subjectFrom(r)
	original, err := h.Rooms.FindByID(r.Context(), r.PathValue("roomId"))
	if isNotFound(err) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, "rematch lookup", err)
		return
	}

	if _, ours := original.ParticipantBySubject(subject); !ours {
		writeError(w, http.StatusForbidden, codeForbidden,
			"only a participant of a duel can open a rematch of it")
		return
	}

	// Resolved from the original room at creation rather than looked up when the
	// seat is claimed: the claim has to stay one atomic document update and
	// cannot consult a second room inside it.
	opponent, found := otherParticipant(original, subject)
	if !found {
		writeError(w, http.StatusConflict, codeInvalidRequest,
			"that duel has no opponent to rematch against")
		return
	}

	figures, err := duel.DeriveFigures(
		payload.CreatorSide.Placement.Stake, payload.CreatorSide.Odd, payload.OpponentSide.Odd)
	if err != nil {
		h.writeFiguresError(w, err)
		return
	}

	now := h.nowMS()
	if refused := h.checkDuelLimits(r, subject, payload.CreatorSide.Placement.Stake, now); refused != nil {
		writeError(w, refused.status, refused.code, refused.message)
		return
	}

	requestedExpiry := int64(0)
	if req.ExpiresAt != nil {
		requestedExpiry = *req.ExpiresAt
	}
	expiresAt := h.clampExpiry(requestedExpiry, now)
	if expiresAt <= now {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "expiresAt is in the past")
		return
	}

	inviteCode, err := newInviteCode()
	if err != nil {
		internalError(w, "generate invite code", err)
		return
	}

	roomID := uuid.NewString()
	senderID := uuid.NewString()
	payload.CreatorSide.Placement.ParticipantID = senderID
	payload.Figures = figures

	room := models.Room{
		ID:       roomID,
		EventID:  original.EventID,
		Strategy: original.Strategy,
		Status:   models.StatusOpen,
		Capacity: original.Capacity,
		Participants: []models.Participant{{
			ID:                 senderID,
			ParticipantPayload: participant,
			BetRef:             betRef,
			Subject:            subject,
		}},
		InviteCode:       inviteCode,
		InviteURL:        buildInviteURL(h.HostEventURLTemplate, original.EventID, roomID, inviteCode),
		CreatedAt:        now,
		ExpiresAt:        expiresAt,
		Payload:          payload,
		RematchOfRoomID:  original.ID,
		PermittedSubject: opponent.Subject,
	}

	switch err := h.Rooms.Create(r.Context(), room); {
	case err == nil:
		writeJSON(w, http.StatusCreated, h.roomView(room, subject, true))
	case errors.Is(err, store.ErrDuplicateBet):
		h.respondToDuplicateBet(w, r, betRef.ID, subject)
	default:
		internalError(w, "create rematch room", err)
	}
}

// otherParticipant finds the one participant of a room who is not the caller.
func otherParticipant(room models.Room, subject string) (models.Participant, bool) {
	for _, p := range room.Participants {
		if p.Subject != subject {
			return p, true
		}
	}
	return models.Participant{}, false
}
