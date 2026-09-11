package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// reserve holds a seat and returns the hold, failing the test on any other status.
func (e *env) reserve(token string, room models.Room) seatHold {
	e.t.Helper()
	var hold seatHold
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", token,
		map[string]any{"inviteCode": room.InviteCode}), http.StatusCreated, &hold)
	return hold
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/seat
// ---------------------------------------------------------------------------

func TestReserveSeat(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	hold := e.reserve("bob", room)
	if hold.ID == "" {
		t.Error("no hold id was returned")
	}
	if want := e.nowMS() + testSeatHoldTTLMS; hold.ExpiresAt != want {
		t.Errorf("hold expiresAt = %d, want %d", hold.ExpiresAt, want)
	}

	// To everyone else the hold is visible only as the room's status.
	var seen models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
	if seen.Status != models.StatusReserved {
		t.Errorf("status = %q, want reserved", seen.Status)
	}
	if len(seen.Participants) != 1 {
		t.Errorf("participants = %d, want 1 — a hold is not a participant", len(seen.Participants))
	}
}

func TestReserveSeatRefusals(t *testing.T) {
	e := newEnv(t)

	t.Run("wrong invite code is forbidden", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", "bob",
			map[string]any{"inviteCode": "WRONGCOD"})
		if res.status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", res.status, res.body)
		}
		// And no seat is held.
		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if seen.Status != models.StatusOpen {
			t.Errorf("status = %q after a refused claim, want open", seen.Status)
		}
	})

	t.Run("unknown room is not found", func(t *testing.T) {
		res := e.do(http.MethodPost, "/rooms/no-such-room/seat", "bob",
			map[string]any{"inviteCode": "ANYCODE1"})
		if res.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body: %s", res.status, res.body)
		}
	})

	t.Run("the creator cannot occupy a second seat", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", "alice",
			map[string]any{"inviteCode": room.InviteCode})
		if res.status != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body: %s", res.status, res.body)
		}
		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if len(seen.Participants) != 1 {
			t.Errorf("participants = %d, want the list unchanged", len(seen.Participants))
		}
	})

	t.Run("a held seat is not offered to someone else", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		first := e.reserve("bob", room)

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", "carol",
			map[string]any{"inviteCode": room.InviteCode})
		if res.status != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body: %s", res.status, res.body)
		}

		// The existing hold is unaffected: bob can still confirm it.
		res = e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+first.ID+"/confirm", "bob",
			confirmBody("Ivan P.", "IP", 205, "8.87", true))
		if res.status != http.StatusOK {
			t.Errorf("the original hold was disturbed: status = %d; body: %s", res.status, res.body)
		}
	})

	t.Run("reserving after expiry is gone, not a conflict", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		e.advance(testInviteWindowMS + 1)

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", "bob",
			map[string]any{"inviteCode": room.InviteCode})
		if res.status != http.StatusGone {
			t.Fatalf("status = %d, want 410 — the client maps 410 to expired and 409 to already-taken; body: %s",
				res.status, res.body)
		}
		var errBody struct{ Code string }
		_ = json.Unmarshal(res.body, &errBody)
		if errBody.Code != "roomExpired" {
			t.Errorf("code = %q, want roomExpired", errBody.Code)
		}
	})

	t.Run("reserving in a filled room is a conflict", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
			confirmBody("Ivan P.", "IP", 205, "8.87", true)), http.StatusOK, nil)

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", "carol",
			map[string]any{"inviteCode": room.InviteCode})
		if res.status != http.StatusConflict {
			t.Errorf("status = %d, want 409; body: %s", res.status, res.body)
		}
	})
}

// The requirement this whole design exists for: seat availability is evaluated
// atomically, so two simultaneous requests for one seat produce exactly one hold.
//
// A sequential test would pass against a naive read-then-write implementation,
// which is why this one fires everything at once against a real database.
func TestConcurrentReservationsProduceExactlyOneHold(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	const claimants = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		other   = map[int]int{}
	)

	start := make(chan struct{})
	for i := range claimants {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together
			res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat", fmt.Sprintf("joiner-%d", i),
				map[string]any{"inviteCode": room.InviteCode})

			mu.Lock()
			defer mu.Unlock()
			if res.status == http.StatusCreated {
				created++
			} else {
				other[res.status]++
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if created != 1 {
		t.Errorf("%d claimants obtained a hold, want exactly 1 (others: %v)", created, other)
	}
	if got := other[http.StatusConflict]; got != claimants-1 {
		t.Errorf("%d were refused as a conflict, want %d (all statuses: %v)", got, claimants-1, other)
	}
}

// ---------------------------------------------------------------------------
// DELETE /rooms/{roomId}/seat/{holdId}
// ---------------------------------------------------------------------------

func TestReleaseSeat(t *testing.T) {
	e := newEnv(t)

	t.Run("released seat is immediately available to another user", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)

		res := e.do(http.MethodDelete, "/rooms/"+room.ID+"/seat/"+hold.ID, "bob", nil)
		if res.status != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", res.status, res.body)
		}

		// No waiting for the TTL: carol can take it now.
		e.reserve("carol", room)

		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if len(seen.Participants) != 1 {
			t.Errorf("participants = %d, want 1 — a released hold leaves no trace", len(seen.Participants))
		}
	})

	t.Run("someone else's hold is forbidden", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)

		res := e.do(http.MethodDelete, "/rooms/"+room.ID+"/seat/"+hold.ID, "carol", nil)
		if res.status != http.StatusForbidden {
			t.Errorf("status = %d, want 403; body: %s", res.status, res.body)
		}
	})

	t.Run("releasing twice is still no content", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)

		e.do(http.MethodDelete, "/rooms/"+room.ID+"/seat/"+hold.ID, "bob", nil)
		res := e.do(http.MethodDelete, "/rooms/"+room.ID+"/seat/"+hold.ID, "bob", nil)
		if res.status != http.StatusNoContent {
			t.Errorf("status = %d, want 204 — the post-condition the caller wants is already true", res.status)
		}
	})

	t.Run("unknown room is not found", func(t *testing.T) {
		res := e.do(http.MethodDelete, "/rooms/no-such-room/seat/no-such-hold", "bob", nil)
		if res.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body: %s", res.status, res.body)
		}
	})

	// Releasing sets the room back to open, so it must never touch a room that is
	// no longer taking seats — a stale hold id must not resurrect a filled duel.
	t.Run("a stale hold cannot reopen a filled room", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
			confirmBody("Ivan P.", "IP", 205, "8.87", true)), http.StatusOK, nil)

		res := e.do(http.MethodDelete, "/rooms/"+room.ID+"/seat/"+hold.ID, "bob", nil)
		if res.status != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", res.status, res.body)
		}

		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if seen.Status != models.StatusFilled {
			t.Errorf("status = %q after releasing a stale hold, want filled", seen.Status)
		}
		if len(seen.Participants) != 2 {
			t.Errorf("participants = %d, want 2", len(seen.Participants))
		}
	})
}

// A hold lapses on its own and the seat reopens, with no participant added.
func TestHoldLapsesAndTheSeatReopens(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")
	e.reserve("bob", room)

	e.advance(testSeatHoldTTLMS + 1)

	// Someone else can take the seat now.
	e.reserve("carol", room)

	var seen models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
	if len(seen.Participants) != 1 {
		t.Errorf("participants = %d, want 1 — a lapsed hold adds nobody", len(seen.Participants))
	}
}

// ---------------------------------------------------------------------------
// POST /rooms/{roomId}/seat/{holdId}/confirm
// ---------------------------------------------------------------------------

func TestConfirmSeat(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")
	hold := e.reserve("bob", room)

	var filled models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
		confirmBody("Ivan P.", "IP", 205, "8.87", true)), http.StatusOK, &filled)

	if filled.Status != models.StatusFilled {
		t.Errorf("status = %q, want filled", filled.Status)
	}
	if len(filled.Participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(filled.Participants))
	}
	if filled.ViewerParticipantID == "" || filled.ViewerParticipantID == filled.Participants[0].ID {
		t.Errorf("viewerParticipantId = %q, want the joiner's own id", filled.ViewerParticipantID)
	}

	placement := filled.Payload.OpponentSide.Placement
	if placement == nil {
		t.Fatal("the opponent side has no placement; the matched state renders each side's own stake")
	}
	if got := placement.Stake.String(); got != "8.87" {
		t.Errorf("opponent stake = %s, want 8.87", got)
	}
	if placement.LineItemID != "li-99" || placement.DataVersion != 9 {
		t.Errorf("feed-sourced fields = %q/%d, want li-99/9", placement.LineItemID, placement.DataVersion)
	}
	if placement.ParticipantID != filled.ViewerParticipantID {
		t.Error("the opponent's placement is not attributed to the joiner")
	}
	// The creator's own side is untouched: their bet is already placed.
	if got := filled.Payload.CreatorSide.Placement.Stake.String(); got != "10.00" {
		t.Errorf("creator stake = %s, want 10.00 — the creator's side must not be rewritten", got)
	}
}

// The joiner is placed at the price current when they accept, so the room records
// that price and restates the figures at it.
func TestConfirmSeatRepricesAtTheAcceptedPrice(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")
	hold := e.reserve("bob", room)

	// The opposite side drifted from 2.05 to 2.20 while the duel waited.
	var filled models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
		confirmBody("Ivan P.", "IP", 220, "8.27", true)), http.StatusOK, &filled)

	if filled.Payload.OpponentSide.Odd != 220 {
		t.Errorf("opponent odd = %d, want the accepted 220", filled.Payload.OpponentSide.Odd)
	}
	// payout is the creator's, unchanged: 10.00 at 1.82.
	if got := filled.Payload.Figures.Payout.String(); got != "18.20" {
		t.Errorf("payout = %s, want 18.20 — the creator's bet is already placed", got)
	}
	// entry = floor(18.20 / 2.20) = 8.27
	if got := filled.Payload.Figures.Entry.String(); got != "8.27" {
		t.Errorf("entry = %s, want 8.27 restated at the accepted price", got)
	}
	if got := filled.Payload.Figures.Pot.String(); got != "18.27" {
		t.Errorf("pot = %s, want 18.27", got)
	}
}

// A pair that was eligible when the duel opened can drift into an underround
// while it waits. Acceptance is then refused rather than repriced.
func TestConfirmSeatRefusesAnUnderroundDrift(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")
	hold := e.reserve("bob", room)

	// 1.82 against 2.30: 100*(182+230) = 41200 is below 182*230 = 41860, so the
	// implied probabilities now sum to less than one and the pot would come out
	// below the winner's take.
	res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
		confirmBody("Ivan P.", "IP", 230, "7.91", true))
	if res.status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", res.status, res.body)
	}
	var errBody struct{ Code string }
	_ = json.Unmarshal(res.body, &errBody)
	if errBody.Code != "underroundPrices" {
		t.Errorf("code = %q, want underroundPrices", errBody.Code)
	}

	var seen models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
	if len(seen.Participants) != 1 {
		t.Errorf("participants = %d, want 1 — a refused acceptance adds nobody", len(seen.Participants))
	}
}

func TestConfirmSeatRefusals(t *testing.T) {
	e := newEnv(t)

	t.Run("a lapsed hold is gone, not a conflict", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)
		e.advance(testSeatHoldTTLMS + 1)

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
			confirmBody("Ivan P.", "IP", 205, "8.87", true))
		if res.status != http.StatusGone {
			t.Fatalf("status = %d, want 410; body: %s", res.status, res.body)
		}

		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if len(seen.Participants) != 1 {
			t.Errorf("participants = %d, want 1", len(seen.Participants))
		}
	})

	t.Run("confirming a hold the caller does not own is forbidden", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "carol",
			confirmBody("Carol X.", "CX", 205, "8.87", true))
		if res.status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", res.status, res.body)
		}

		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if len(seen.Participants) != 1 {
			t.Errorf("participants = %d, want 1 — no participant is added", len(seen.Participants))
		}
	})

	t.Run("an unconfirmed hold does not survive room expiry", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)
		e.advance(testInviteWindowMS + 1)

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
			confirmBody("Ivan P.", "IP", 205, "8.87", true))
		if res.status != http.StatusGone {
			t.Fatalf("status = %d, want 410; body: %s", res.status, res.body)
		}

		var seen models.Room
		e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", nil), http.StatusOK, &seen)
		if seen.Status != models.StatusExpired || len(seen.Participants) != 1 {
			t.Errorf("room = %q with %d participants, want expired with its original participant only",
				seen.Status, len(seen.Participants))
		}
	})

	t.Run("an unknown hold is gone", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/no-such-hold/confirm", "bob",
			confirmBody("Ivan P.", "IP", 205, "8.87", true))
		if res.status != http.StatusGone {
			t.Errorf("status = %d, want 410; body: %s", res.status, res.body)
		}
	})

	t.Run("no betRef is a validation failure", func(t *testing.T) {
		room := e.createRoom("alice", "evt-1")
		hold := e.reserve("bob", room)
		body := confirmBody("Ivan P.", "IP", 205, "8.87", true)
		body["betRef"] = map[string]any{"id": "", "number": 0}

		res := e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob", body)
		if res.status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body: %s", res.status, res.body)
		}
	})
}

// Gap G1: the shipped client sends neither the accepted price nor the placement.
// The room still fills — the widget must keep working — but the two feed-sourced
// fields are empty rather than invented, so the gap is visible in the data.
func TestConfirmSeatWithoutPlacementStillFillsTheRoom(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")
	hold := e.reserve("bob", room)

	var filled models.Room
	e.decode(e.do(http.MethodPost, "/rooms/"+room.ID+"/seat/"+hold.ID+"/confirm", "bob",
		confirmBody("Ivan P.", "IP", 0, "", false)), http.StatusOK, &filled)

	if filled.Status != models.StatusFilled {
		t.Fatalf("status = %q, want filled", filled.Status)
	}
	placement := filled.Payload.OpponentSide.Placement
	if placement == nil {
		t.Fatal("no opponent placement at all")
	}
	// Best available stand-in: the entry the joiner was told to place.
	if got := placement.Stake.String(); got != "8.87" {
		t.Errorf("stake = %s, want the derived entry 8.87", got)
	}
	if placement.LineItemID != "" || placement.DataVersion != 0 {
		t.Errorf("lineItemId/dataVersion = %q/%d, want them empty rather than invented",
			placement.LineItemID, placement.DataVersion)
	}
	// The fallback writes the opponent side and the figures. It must not reach
	// the creator's side, whose bet was placed long before this request.
	if got := filled.Payload.CreatorSide.Placement.Stake.String(); got != "10.00" {
		t.Errorf("creator stake = %s, want 10.00 — the fallback wrote to the wrong side", got)
	}
	if filled.Payload.CreatorSide.Odd != 182 {
		t.Errorf("creator odd = %d, want 182 unchanged", filled.Payload.CreatorSide.Odd)
	}
}
