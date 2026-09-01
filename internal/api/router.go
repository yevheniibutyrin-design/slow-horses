package api

import "net/http"

// NewRouter wires the routes and the middleware chain. Go 1.22+ ServeMux
// matches method and path patterns, so no third-party router is needed.
func NewRouter(h *Handlers) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", h.ping)
	mux.HandleFunc("GET /rooms", h.listRooms)
	mux.HandleFunc("PUT /room", h.createRoom)
	mux.HandleFunc("DELETE /room/{id}", h.deleteRoom)
	mux.HandleFunc("POST /join", h.join)
	mux.HandleFunc("GET /openapi.yaml", h.openAPI)

	// Outermost first: recover, then log, then CORS (which answers preflights).
	return recovering(logging(cors(mux)))
}
