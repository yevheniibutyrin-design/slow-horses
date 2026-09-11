package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// ---------------------------------------------------------------------------
// POST /rooms
// ---------------------------------------------------------------------------

func TestCreateRoom(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-ucl")

	if room.Status != models.StatusOpen {
		t.Errorf("status = %q, want open", room.Status)
	}
	// Capacity is declared by the strategy, never read from the request.
	if room.Capacity != 2 {
		t.Errorf("capacity = %d, want 2 (declared by the duel strategy)", room.Capacity)
	}
	if len(room.Participants) != 1 {
		t.Fatalf("participants = %d, want exactly the creator", len(room.Participants))
	}
	if room.Participants[0].Label != "Maksym K." || room.Participants[0].Initials != "MK" {
		t.Errorf("participant identity = %+v, want the label and initials as supplied", room.Participants[0])
	}
	if room.Participants[0].BetRef.ID == "" {
		t.Error("the creator's bet reference is empty; it is the room's only link to money")
	}
	// The reader has to be told which participant they are: labels are not unique.
	if room.ViewerParticipantID != room.Participants[0].ID {
		t.Errorf("viewerParticipantId = %q, want the creator's id %q", room.ViewerParticipantID, room.Participants[0].ID)
	}
	if room.InviteCode == "" {
		t.Fatal("no invite code was issued")
	}
	if strings.Contains(room.ID, room.InviteCode) || strings.Contains(room.InviteCode, room.ID) {
		t.Error("the invite code looks derivable from the room id; it must not be")
	}
}

// The invite link points at the HOST's event page, carrying both parameters the
// widget reads. A half-formed link is treated as no invite at all.
func TestCreateRoomInviteURL(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-ucl")

	if !strings.HasPrefix(room.InviteURL, "https://sportsbook.example/event/evt-ucl") {
		t.Errorf("inviteUrl = %q, want it to point at the host's event page", room.InviteURL)
	}
	if !strings.Contains(room.InviteURL, "betRoomId="+room.ID) {
		t.Errorf("inviteUrl = %q, missing betRoomId", room.InviteURL)
	}
	if !strings.Contains(room.InviteURL, "betRoomInvite="+room.InviteCode) {
		t.Errorf("inviteUrl = %q, missing betRoomInvite", room.InviteURL)
	}
}

// Figures are derived here, not taken from the request: the service never stores
// a client's claim about money it can compute itself.
func TestCreateRoomDerivesFiguresRatherThanTrustingThem(t *testing.T) {
	e := newEnv(t)
	body := createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00")
	body["payload"].(map[string]any)["figures"] = map[string]any{
		"payout": json.RawMessage("999.00"), "entry": json.RawMessage("1.00"), "pot": json.RawMessage("2.00"),
	}

	var room models.Room
	e.decode(e.do(http.MethodPost, "/rooms", "alice", body), http.StatusCreated, &room)

	if got := room.Payload.Figures.Payout.String(); got != "18.20" {
		t.Errorf("payout = %s, want 18.20 — the client's figures were trusted", got)
	}
	if got := room.Payload.Figures.Entry.String(); got != "8.87" {
		t.Errorf("entry = %s, want 8.87", got)
	}
	// The service issues participant ids, so the creator's placement carries one.
	if room.Payload.CreatorSide.Placement.ParticipantID != room.Participants[0].ID {
		t.Error("the creator's placement is not attributed to the creator participant")
	}
}

func TestCreateRoomRefusals(t *testing.T) {
	e := newEnv(t)
	valid := func() map[string]any {
		return createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00")
	}

	cases := []struct {
		name    string
		mutate  func(map[string]any)
		status  int
		code    string
		comment string
	}{
		{"unknown strategy", func(b map[string]any) { b["strategy"] = "poker" },
			http.StatusBadRequest, "unknownStrategy", "creation is rejected and no room is stored"},
		{"no eventId", func(b map[string]any) { b["eventId"] = "" },
			http.StatusBadRequest, "invalidRequest", ""},
		{"no betRef", func(b map[string]any) { b["betRef"] = map[string]any{"id": "", "number": 0} },
			http.StatusBadRequest, "invalidRequest", "a participant with no link to money"},
		{"no label", func(b map[string]any) { b["participant"] = map[string]any{"label": "", "initials": "MK"} },
			http.StatusBadRequest, "invalidRequest", ""},
		{"underround prices", func(b map[string]any) {
			p := b["payload"].(map[string]any)
			p["creatorSide"].(map[string]any)["odd"] = 333
			p["opponentSide"].(map[string]any)["odd"] = 143
		}, http.StatusConflict, "underroundPrices", "the pot would come out below the winner's take"},
		{"odd below 1.00", func(b map[string]any) {
			b["payload"].(map[string]any)["opponentSide"].(map[string]any)["odd"] = 99
		}, http.StatusBadRequest, "invalidRequest", "the guard cannot evaluate it, so it fails closed"},
		{"same outcome both sides", func(b map[string]any) {
			b["payload"].(map[string]any)["opponentSide"].(map[string]any)["outcomeId"] = "over"
		}, http.StatusBadRequest, "invalidRequest", ""},
		{"no market item", func(b map[string]any) {
			b["payload"].(map[string]any)["marketItemId"] = ""
		}, http.StatusBadRequest, "invalidRequest", "a market id alone cannot tell Over 2.5 from Over 3.5"},
		{"no creator placement", func(b map[string]any) {
			delete(b["payload"].(map[string]any)["creatorSide"].(map[string]any), "placement")
		}, http.StatusBadRequest, "invalidRequest", "the creator's bet always exists by now"},
		{"stake above the per-duel maximum", func(b map[string]any) {
			b["payload"].(map[string]any)["creatorSide"].(map[string]any)["placement"].(map[string]any)["stake"] =
				json.RawMessage("501.00")
		}, http.StatusConflict, "perDuelMaxExceeded", ""},
		{"expiry in the past", func(b map[string]any) { b["expiresAt"] = e.nowMS() - 1000 },
			http.StatusBadRequest, "invalidRequest", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := valid()
			c.mutate(body)
			res := e.do(http.MethodPost, "/rooms", "alice", body)
			if res.status != c.status {
				t.Fatalf("status = %d, want %d (%s); body: %s", res.status, c.status, c.comment, res.body)
			}
			var errBody struct{ Code string }
			if err := json.Unmarshal(res.body, &errBody); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if errBody.Code != c.code {
				t.Errorf("code = %q, want %q", errBody.Code, c.code)
			}
		})
	}
}

// An unknown field is a 400 rather than a silent no-op, so a typo surfaces.
func TestCreateRoomRejectsUnknownTopLevelFields(t *testing.T) {
	e := newEnv(t)
	body := createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00")
	body["nmae"] = "typo"

	if res := e.do(http.MethodPost, "/rooms", "alice", body); res.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", res.status, res.body)
	}
}

// The payload is the one place unknown fields are DROPPED rather than rejected:
// it carries a strategy's own data, and a field a future widget adds must not
// 400 the route.
func TestCreateRoomToleratesUnknownPayloadFields(t *testing.T) {
	e := newEnv(t)
	body := createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00")
	body["payload"].(map[string]any)["somethingTheWidgetAddedLater"] = map[string]any{"a": 1}

	if res := e.do(http.MethodPost, "/rooms", "alice", body); res.status != http.StatusCreated {
		t.Errorf("status = %d, want 201; an unknown payload field must not fail the route. body: %s",
			res.status, res.body)
	}
}

// The client's expiry is re-applied against the invite window here, and may only
// ever be shortened.
func TestCreateRoomClampsExpiryToTheInviteWindow(t *testing.T) {
	e := newEnv(t)
	// Ask for a week.
	body := createBody("evt-1", e.nowMS()+7*24*60*60*1000, 182, 205, "10.00")

	var room models.Room
	e.decode(e.do(http.MethodPost, "/rooms", "alice", body), http.StatusCreated, &room)

	if want := e.nowMS() + testInviteWindowMS; room.ExpiresAt > want {
		t.Errorf("expiresAt = %d, want it clamped to at most %d", room.ExpiresAt, want)
	}
}

func TestCreateRoomKeepsAShorterExpiry(t *testing.T) {
	e := newEnv(t)
	shorter := e.nowMS() + 60_000
	body := createBody("evt-1", shorter, 182, 205, "10.00")

	var room models.Room
	e.decode(e.do(http.MethodPost, "/rooms", "alice", body), http.StatusCreated, &room)

	if room.ExpiresAt != shorter {
		t.Errorf("expiresAt = %d, want the client's shorter %d kept", room.ExpiresAt, shorter)
	}
}

// The bet is placed before the room exists, so a retried create after a timeout
// must not open a second room for one bet.
func TestCreateRoomIsIdempotentForOneBet(t *testing.T) {
	e := newEnv(t)
	body := createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00")

	var first models.Room
	e.decode(e.do(http.MethodPost, "/rooms", "alice", body), http.StatusCreated, &first)

	res := e.do(http.MethodPost, "/rooms", "alice", body)
	var second models.Room
	e.decode(res, http.StatusOK, &second)

	if second.ID != first.ID {
		t.Errorf("the retry opened a second room (%s) for one bet (first was %s)", second.ID, first.ID)
	}
}

// Someone else's bet reference is a conflict, not a room handed to the wrong caller.
func TestCreateRoomRefusesAnotherCallersBet(t *testing.T) {
	e := newEnv(t)
	body := createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00")
	e.decode(e.do(http.MethodPost, "/rooms", "alice", body), http.StatusCreated, nil)

	if res := e.do(http.MethodPost, "/rooms", "bob", body); res.status != http.StatusConflict {
		t.Errorf("status = %d, want 409; body: %s", res.status, res.body)
	}
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/read
// ---------------------------------------------------------------------------

func TestReadRoom(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	t.Run("participant reads without a code", func(t *testing.T) {
		var got models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &got)
		if got.ViewerParticipantID != room.Participants[0].ID {
			t.Errorf("viewerParticipantId = %q, want the creator's", got.ViewerParticipantID)
		}
	})

	t.Run("code holder reads and is not a participant", func(t *testing.T) {
		var got models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "carol",
			map[string]any{"inviteCode": room.InviteCode}), http.StatusOK, &got)
		if got.ViewerParticipantID != "" {
			t.Errorf("viewerParticipantId = %q, want it absent for a reader who has not taken a seat",
				got.ViewerParticipantID)
		}
	})

	t.Run("stranger without a code is forbidden", func(t *testing.T) {
		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "carol", nil)
		if res.status != http.StatusForbidden {
			t.Errorf("status = %d, want 403; body: %s", res.status, res.body)
		}
	})

	t.Run("wrong code is forbidden", func(t *testing.T) {
		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "carol",
			map[string]any{"inviteCode": "WRONGCOD"})
		if res.status != http.StatusForbidden {
			t.Errorf("status = %d, want 403; body: %s", res.status, res.body)
		}
	})

	t.Run("unknown room is not found", func(t *testing.T) {
		res := e.do(http.MethodPost, "/rooms/no-such-room/read", "alice", nil)
		if res.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body: %s", res.status, res.body)
		}
	})
}

// A room past its expiry reads as expired without waiting for the sweeper.
func TestReadRoomAppliesExpiryLazily(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	e.advance(testInviteWindowMS + 1)

	var got models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &got)
	if got.Status != models.StatusExpired {
		t.Errorf("status = %q, want expired — the sweeper has not run, so this must be evaluated on read", got.Status)
	}
}

// ---------------------------------------------------------------------------
// GET /rooms
// ---------------------------------------------------------------------------

func TestListOpenRooms(t *testing.T) {
	e := newEnv(t)
	alices := e.createRoom("alice", "evt-list")
	e.createRoom("bob", "evt-list")
	e.createRoom("bob", "evt-other")

	var listed []models.Room
	e.decode(e.do(http.MethodGet, "/rooms?eventId=evt-list&status=open", "alice", nil), http.StatusOK, &listed)

	if len(listed) != 1 {
		t.Fatalf("listed %d rooms, want 1 (bob's, on this event, excluding alice's own)", len(listed))
	}
	if listed[0].ID == alices.ID {
		t.Error("the caller's own room was listed")
	}
	// The decision in the contract: listing a duel on an event IS the offer, so
	// the code comes with it — otherwise nobody on the list could take the seat.
	if listed[0].InviteCode == "" {
		t.Error("a listed room carries no invite code, so nothing on the list can be taken")
	}
}

func TestListOpenRoomsEmptyIsAnArrayNotAnError(t *testing.T) {
	e := newEnv(t)
	res := e.do(http.MethodGet, "/rooms?eventId=evt-empty&status=open", "alice", nil)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.status, res.body)
	}
	if got := strings.TrimSpace(string(res.body)); got != "[]" {
		t.Errorf("body = %s, want [] — an event with no open rooms is not an error and not null", got)
	}
}

func TestListOpenRoomsExcludesExpired(t *testing.T) {
	e := newEnv(t)
	e.createRoom("bob", "evt-list")
	e.advance(testInviteWindowMS + 1)

	var listed []models.Room
	e.decode(e.do(http.MethodGet, "/rooms?eventId=evt-list&status=open", "alice", nil), http.StatusOK, &listed)
	if len(listed) != 0 {
		t.Errorf("listed %d expired rooms, want none", len(listed))
	}
}

func TestListOpenRoomsRequiresAnEvent(t *testing.T) {
	e := newEnv(t)
	if res := e.do(http.MethodGet, "/rooms?status=open", "alice", nil); res.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", res.status, res.body)
	}
}

// ---------------------------------------------------------------------------
// GET /rooms/limits
// ---------------------------------------------------------------------------

func TestRoomLimits(t *testing.T) {
	e := newEnv(t)

	var limits struct {
		PerDuelMax  json.RawMessage `json:"perDuelMax"`
		DailyLimit  int             `json:"dailyLimit"`
		PlayedToday int             `json:"playedToday"`
		Currency    string          `json:"currency"`
	}
	e.decode(e.do(http.MethodGet, "/rooms/limits", "alice", nil), http.StatusOK, &limits)

	if string(limits.PerDuelMax) != "500.00" {
		t.Errorf("perDuelMax = %s, want 500.00", limits.PerDuelMax)
	}
	if limits.DailyLimit != testDailyLimit {
		t.Errorf("dailyLimit = %d, want %d", limits.DailyLimit, testDailyLimit)
	}
	if limits.PlayedToday != 0 {
		t.Errorf("playedToday = %d, want 0 before anything is created", limits.PlayedToday)
	}
	if limits.Currency != "EUR" {
		t.Errorf("currency = %q, want EUR", limits.Currency)
	}

	e.createRoom("alice", "evt-1")
	e.decode(e.do(http.MethodGet, "/rooms/limits", "alice", nil), http.StatusOK, &limits)
	if limits.PlayedToday != 1 {
		t.Errorf("playedToday = %d after one duel, want 1", limits.PlayedToday)
	}

	// Scoped to the caller: another player's duels are not on this count.
	e.decode(e.do(http.MethodGet, "/rooms/limits", "bob", nil), http.StatusOK, &limits)
	if limits.PlayedToday != 0 {
		t.Errorf("playedToday = %d for a different caller, want 0", limits.PlayedToday)
	}
}
