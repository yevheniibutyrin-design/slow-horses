package api

import (
	"net/http"

	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
)

// NewRouter wires the routes and the middleware chain. Go 1.22+ ServeMux matches
// method and path patterns, so no third-party router is needed — including the
// three-segment seat routes below.
//
// The boilerplate's PUT /room, POST /join and DELETE /room/{id} are gone: they
// are superseded by this surface, and the room they operated on no longer has
// the fields they set. See `docs/bet-room-api.md`.
func NewRouter(h *Handlers, verifier auth.Verifier, allowedOrigins []string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", h.ping)
	mux.HandleFunc("GET /openapi.yaml", h.openAPI)

	// 1. Create a room from a bet the creator has already placed.
	mux.HandleFunc("POST /rooms", h.createRoom)
	// 5. Open rooms on an event, excluding the caller's own and every rematch.
	mux.HandleFunc("GET /rooms", h.listOpenRooms)
	// 8. The caller's duel limits and how many they have played today.
	mux.HandleFunc("GET /rooms/limits", h.roomLimits)
	// 4. Read room state. POST, because the invite code must never reach a query
	// string, and this is the route the widget polls.
	mux.HandleFunc("POST /rooms/{roomId}/read", h.readRoom)
	// 2. Hold a seat, before the joiner commits anything.
	mux.HandleFunc("POST /rooms/{roomId}/seat", h.reserveSeat)
	// 7. Release a held seat explicitly, rather than waiting out its TTL.
	mux.HandleFunc("DELETE /rooms/{roomId}/seat/{holdId}", h.releaseSeat)
	// 3. Turn the hold into a participant, with the bet that was placed.
	mux.HandleFunc("POST /rooms/{roomId}/seat/{holdId}/confirm", h.confirmSeat)
	// 6. Open a rematch, restricted to the opponent of the duel it came from.
	mux.HandleFunc("POST /rooms/{roomId}/rematch", h.rematch)

	// The free vote. Counts are read for every card on screen in one request;
	// a vote is cast one at a time. Both are POSTs because a structured market
	// key does not serialize sanely into a query string.
	mux.HandleFunc("POST /votes/counts", h.voteCounts)
	mux.HandleFunc("POST /votes", h.castVote)

	// Outermost first: recover, then log, then CORS (which answers preflights),
	// then authentication — so a rejected credential is still logged, and a
	// preflight is answered without needing one.
	//
	// The mux is handed to authenticating so it can ask which route a request
	// resolved to, and thus which ones tolerate an anonymous caller.
	return chain(mux,
		recovering,
		logging,
		cors(allowedOrigins),
		authenticating(verifier, mux),
	)
}
