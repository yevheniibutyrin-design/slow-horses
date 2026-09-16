package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

// maxBodyBytes caps request bodies so a runaway client cannot exhaust memory.
const maxBodyBytes = 1 << 20 // 1 MiB

// Handlers holds everything the HTTP handlers need.
type Handlers struct {
	Rooms       *store.Rooms
	OpenAPISpec []byte

	// HostEventURLTemplate builds the invite link. See config.Config.
	HostEventURLTemplate string
	InviteWindowMS       int64
	SeatHoldTTLMS        int64

	PerDuelMax models.Amount
	DailyLimit int
	Currency   string

	// Now is injectable so tests can drive expiry without sleeping.
	Now func() time.Time
}

// nowMS is the single source of "now" in this package, in the epoch
// milliseconds the contract uses everywhere.
func (h *Handlers) nowMS() int64 {
	now := h.Now
	if now == nil {
		now = time.Now
	}
	return now().UnixMilli()
}

// ping answers "is the server up". Deliberately does not touch MongoDB.
func (h *Handlers) ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// openAPI serves the embedded spec so external tooling can pull it off a
// running server.
func (h *Handlers) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.OpenAPISpec)
}

// roomView prepares a room for one specific reader.
//
// Three things happen here and each is part of the contract rather than
// housekeeping:
//
//   - Expiry is applied lazily, so a room past its instant never reads as open
//     just because the sweeper has not run yet.
//   - ViewerParticipantID is filled in for this caller. The client sends no
//     identifier of its own and cannot work out its own side any other way —
//     matching on the display label is unsafe because labels are not unique.
//   - The seat hold and the permitted-subject restriction are stripped. A hold
//     belongs to its joiner alone; to everyone else it is visible only as the
//     reserved status.
//   - showInviteCode gates the join secret in BOTH the places it appears: the
//     inviteCode field and the inviteUrl that embeds it. Pass false for a reader
//     who is neither a participant nor already holding the code.
//
// Participant subjects are `json:"-"` and so never serialise; a test asserts it.
func (h *Handlers) roomView(room models.Room, subject string, showInviteCode bool) models.Room {
	view := room
	view.Status = room.EffectiveStatus(h.nowMS())
	view.Hold = nil
	view.PermittedSubject = ""

	if p, ok := room.ParticipantBySubject(subject); ok {
		view.ViewerParticipantID = p.ID
	}
	if !showInviteCode {
		view.InviteCode = ""
		// The invite URL carries the same code as its betRoomInvite parameter, so
		// clearing one without the other hands the secret over anyway. Both are
		// omitempty, so they simply vanish from the response.
		view.InviteURL = ""
	}
	if view.Participants == nil {
		view.Participants = []models.Participant{}
	}
	// The selection keys' lists are normalised on every read, not only on write:
	// a nil slice marshals to JSON null, and the widget matches these lists with
	// JSON.stringify, where null never equals []. A document that reached the
	// collection by any other route than this service's own writes would
	// otherwise serve a room that cannot resolve its own market.
	view.Payload.MarketItemID.MarketParameters = stringList(view.Payload.MarketItemID.MarketParameters)
	view.Payload.CreatorSide.OutcomeID = outcome(view.Payload.CreatorSide.OutcomeID)
	view.Payload.OpponentSide.OutcomeID = outcome(view.Payload.OpponentSide.OutcomeID)
	return view
}

// inviteCodeAlphabet omits I, L, O, 0 and 1, so a code read aloud or retyped
// from a screenshot does not turn into a different room.
const inviteCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// inviteCodeLength is short enough to share and long enough that guessing is not
// a strategy: 31^8 is about 8.5e11.
const inviteCodeLength = 8

// newInviteCode returns a short, human-shareable join secret.
//
// Generated from crypto/rand and stored alongside the room, so it is NOT
// derivable from the room id — a caller holding an id must not be able to work
// out the code that authorises taking a seat in it.
func newInviteCode() (string, error) {
	buf := make([]byte, inviteCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, inviteCodeLength)
	for i, b := range buf {
		out[i] = inviteCodeAlphabet[int(b)%len(inviteCodeAlphabet)]
	}
	return string(out), nil
}

// buildInviteURL renders the link a joiner follows.
//
// It points at the HOST's event page, never at this service: the widget reads
// betRoomId and betRoomInvite off the query string once the host has landed the
// joiner on the event. Both parameters are required — the client has no
// resolve-by-code-alone route, so a half-formed link is treated as no invite.
func buildInviteURL(template, eventID, roomID, inviteCode string) string {
	base := strings.ReplaceAll(template, "{eventId}", url.PathEscape(eventID))
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + separator +
		"betRoomId=" + url.QueryEscape(roomID) +
		"&betRoomInvite=" + url.QueryEscape(inviteCode)
}

// decodeJSON reads a JSON body and rejects unknown fields, so typos in a request
// surface as a 400 instead of being silently ignored.
//
// Note what this means for a strategy payload: the flag applies to nested
// structs too, so `payload` is decoded as json.RawMessage at this level and
// validated per-strategy afterwards. Decoding it into a typed struct here would
// turn any field a future widget version adds into a 400 on the one route whose
// job is to carry strategy data opaquely.
func decodeJSON(r *http.Request, dst any) error {
	if err := decodeInto(r, dst); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// decodeOptionalJSON is decodeJSON for a body that may legitimately be absent —
// a participant reads a room without supplying an invite code, and sends nothing.
func decodeOptionalJSON(r *http.Request, dst any) error {
	err := decodeInto(r, dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// decodeInto returns the decoder's own error, so callers can tell an empty body
// (io.EOF) from a malformed one.
func decodeInto(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// notFound is the one "no such room" response, so the wording cannot drift
// between the routes that produce it.
func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, codeNotFound, "room not found")
}

// isNotFound reports a store miss without every handler importing the sentinel.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
