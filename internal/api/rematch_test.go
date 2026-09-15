package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// settledDuel returns a filled room between alice (creator) and bob (joiner).
func (e *env) filledDuel(eventID string) models.Room {
	e.t.Helper()
	room := e.createRoom("alice", eventID)
	hold := e.reserve("bob", room)

	var filled models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
		confirmBody("Ivan P.", "IP", 205, "8.87", true)), http.StatusOK, &filled)
	return filled
}

// rematchBody is what the widget's epic composes. betRef and expiresAt are the
// two fields its client currently drops (gap G2).
func rematchBody(withBetRef bool) map[string]any {
	body := map[string]any{
		"participant": participant("Maksym K.", "MK"),
		"payload": map[string]any{
			"marketId":     marketID("total_goals"),
			"marketItemId": map[string]any{"marketParameters": []string{"2.5"}},
			"creatorSide": map[string]any{
				"outcomeId": outcomeID(3),
				"odd":       182,
				"placement": map[string]any{
					"stake":       json.RawMessage("10.00"),
					"lineItemId":  "li-77",
					"dataVersion": 3,
				},
			},
			"opponentSide": map[string]any{"outcomeId": outcomeID(4), "odd": 205},
			"figures":      map[string]any{"payout": 0, "entry": 0, "pot": 0},
		},
	}
	if withBetRef {
		body["betRef"] = map[string]any{"id": uniqueBetID(), "number": 12400}
	}
	return body
}

func TestRematchOpensARestrictedRoom(t *testing.T) {
	e := newEnv(t)
	original := e.filledDuel("evt-1")

	var rematch models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "alice", rematchBody(true)),
		http.StatusCreated, &rematch)

	if rematch.RematchOfRoomID != original.ID {
		t.Errorf("rematchOfRoomId = %q, want %q", rematch.RematchOfRoomID, original.ID)
	}
	// Inherited from the room it descends from, not taken from the request.
	if rematch.EventID != original.EventID || rematch.Strategy != original.Strategy ||
		rematch.Capacity != original.Capacity {
		t.Errorf("rematch did not inherit event/strategy/capacity: %+v", rematch)
	}
	if rematch.Status != models.StatusOpen {
		t.Errorf("status = %q, want open", rematch.Status)
	}
	if len(rematch.Participants) != 1 {
		t.Errorf("participants = %d, want only the sender", len(rematch.Participants))
	}
}

// A rematch is joinable only by the other participant of the duel it came from,
// not by whoever opens the link first. The service resolves that opponent itself:
// the payload carries no cross-room identity for it to be told.
func TestRematchIsRestrictedToTheOriginalOpponent(t *testing.T) {
	e := newEnv(t)
	original := e.filledDuel("evt-1")

	var rematch models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "alice", rematchBody(true)),
		http.StatusCreated, &rematch)

	// Carol has the code — it came back in the response — and still cannot take it.
	res := e.do(http.MethodPost, "/rooms/"+rematch.ID+"/seat", "carol",
		map[string]any{"inviteCode": rematch.InviteCode})
	if res.status != http.StatusForbidden {
		t.Fatalf("a non-opponent got status %d, want 403; body: %s", res.status, res.body)
	}

	// Bob, the original opponent, can.
	e.reserve("bob", rematch)
}

// A rematch must never appear in the public list for its event: it is not an
// open offer to whoever is viewing.
func TestRematchIsNeverPubliclyListed(t *testing.T) {
	e := newEnv(t)
	original := e.filledDuel("evt-rematch-list")

	e.decode(e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "alice", rematchBody(true)),
		http.StatusCreated, nil)

	var listed []models.Room
	e.decode(e.do(http.MethodGet, "/rooms?eventId=evt-rematch-list&status=open", "carol", nil),
		http.StatusOK, &listed)

	for _, room := range listed {
		if room.RematchOfRoomID != "" {
			t.Errorf("a rematch room (%s) was listed publicly", room.ID)
		}
	}
	if len(listed) != 0 {
		t.Errorf("listed %d rooms, want none — the only open room on this event is a rematch", len(listed))
	}
}

func TestRematchRefusals(t *testing.T) {
	e := newEnv(t)

	t.Run("only a participant can open a rematch", func(t *testing.T) {
		original := e.filledDuel("evt-1")
		res := e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "carol", rematchBody(true))
		if res.status != http.StatusForbidden {
			t.Errorf("status = %d, want 403; body: %s", res.status, res.body)
		}
	})

	t.Run("unknown room is not found", func(t *testing.T) {
		res := e.do(http.MethodPost, "/rooms/no-such-room/rematch", "alice", rematchBody(true))
		if res.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body: %s", res.status, res.body)
		}
	})

	t.Run("a duel with no opponent has nothing to rematch against", func(t *testing.T) {
		unfilled := e.createRoom("alice", "evt-1")
		res := e.do(http.MethodPost, "/rooms/"+unfilled.ID+"/rematch", "alice", rematchBody(true))
		if res.status != http.StatusConflict {
			t.Errorf("status = %d, want 409; body: %s", res.status, res.body)
		}
	})

	// Gap G2: the widget's client drops betRef before sending. This fails loudly
	// rather than storing a participant with no link to money — which is also the
	// only sane option, since every such room would collide on the unique index
	// over an empty bet id.
	t.Run("no betRef is a validation failure naming the gap", func(t *testing.T) {
		original := e.filledDuel("evt-1")
		res := e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "alice", rematchBody(false))
		if res.status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", res.status, res.body)
		}
		var errBody struct{ Message string }
		_ = json.Unmarshal(res.body, &errBody)
		if errBody.Message == "" {
			t.Error("the refusal carries no message explaining what is missing")
		}
	})
}

// The rematch's expiry is bounded exactly as a create's is, whether or not the
// client sends one.
func TestRematchExpiryIsBounded(t *testing.T) {
	e := newEnv(t)
	original := e.filledDuel("evt-1")

	t.Run("absent expiry defaults to the invite window", func(t *testing.T) {
		var rematch models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "alice", rematchBody(true)),
			http.StatusCreated, &rematch)
		if want := e.nowMS() + testInviteWindowMS; rematch.ExpiresAt != want {
			t.Errorf("expiresAt = %d, want %d", rematch.ExpiresAt, want)
		}
	})

	t.Run("an over-long expiry is clamped", func(t *testing.T) {
		body := rematchBody(true)
		body["expiresAt"] = e.nowMS() + 7*24*60*60*1000

		var rematch models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+original.ID+"/rematch", "alice", body),
			http.StatusCreated, &rematch)
		if want := e.nowMS() + testInviteWindowMS; rematch.ExpiresAt > want {
			t.Errorf("expiresAt = %d, want it clamped to at most %d", rematch.ExpiresAt, want)
		}
	})
}
