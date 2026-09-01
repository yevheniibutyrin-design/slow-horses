package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

// maxBodyBytes caps request bodies so a runaway client cannot exhaust memory.
const maxBodyBytes = 1 << 20 // 1 MiB

// Handlers holds everything the HTTP handlers need.
type Handlers struct {
	Rooms         *store.Rooms
	PublicBaseURL string
	OpenAPISpec   []byte
}

// ping answers "is the server up". Deliberately does not touch MongoDB.
func (h *Handlers) ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// listRooms returns every room as a JSON array.
func (h *Handlers) listRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := h.Rooms.List(r.Context())
	if err != nil {
		log.Printf("api: list rooms: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list rooms")
		return
	}
	writeJSON(w, http.StatusOK, rooms)
}

type createRoomRequest struct {
	Name         string               `json:"name"`
	EventID      string               `json:"eventId"`
	IsFilled     bool                 `json:"isFilled"`
	IsStarted    bool                 `json:"isStarted"`
	Participants []models.Participant `json:"participants"`
}

// createRoom handles PUT /room. The id and roomUrl are generated server-side.
func (h *Handlers) createRoom(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.EventID = strings.TrimSpace(req.EventID)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.EventID == "" {
		writeError(w, http.StatusBadRequest, "eventId is required")
		return
	}

	id := uuid.NewString()
	room := models.Room{
		ID:           id,
		Name:         req.Name,
		Participants: req.Participants,
		IsFilled:     req.IsFilled,
		IsStarted:    req.IsStarted,
		EventID:      req.EventID,
		RoomURL:      h.PublicBaseURL + "/join/" + id,
	}
	if room.Participants == nil {
		room.Participants = []models.Participant{}
	}

	if err := h.Rooms.Create(r.Context(), room); err != nil {
		log.Printf("api: create room: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create room")
		return
	}
	writeJSON(w, http.StatusCreated, room)
}

// deleteRoom handles DELETE /room/{id}, keyed on the room UUID.
func (h *Handlers) deleteRoom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	err := h.Rooms.DeleteByID(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "room not found")
	case err != nil:
		log.Printf("api: delete room: %v", err)
		writeError(w, http.StatusInternalServerError, "could not delete room")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type joinRequest struct {
	ID      string         `json:"id"`
	RoomURL string         `json:"roomUrl"`
	Name    string         `json:"name"`
	Balance map[string]any `json:"balance"`
}

// join handles POST /join. The roomUrl acts as the invite secret: a participant
// is only added when both the id and the roomUrl match the stored room.
func (h *Handlers) join(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.RoomURL = strings.TrimSpace(req.RoomURL)
	req.Name = strings.TrimSpace(req.Name)
	if req.ID == "" || req.RoomURL == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "id, roomUrl and name are required")
		return
	}

	participant := models.Participant{Name: req.Name, Balance: req.Balance}
	if participant.Balance == nil {
		participant.Balance = map[string]any{}
	}

	room, err := h.Rooms.AddParticipant(r.Context(), req.ID, req.RoomURL, participant)
	if err == nil {
		writeJSON(w, http.StatusOK, room)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		log.Printf("api: join room: %v", err)
		writeError(w, http.StatusInternalServerError, "could not join room")
		return
	}

	// Nothing matched the filter. One lookup by id tells us which of the three
	// conditions failed, so the client gets an honest status code.
	existing, err := h.Rooms.FindByID(r.Context(), req.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "room not found")
	case err != nil:
		log.Printf("api: join room lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "could not join room")
	case existing.RoomURL != req.RoomURL:
		writeError(w, http.StatusForbidden, "roomUrl does not match this room's invite")
	case existing.IsFilled:
		writeError(w, http.StatusConflict, "room is already filled")
	default:
		// The room changed between the two queries; the client can retry.
		writeError(w, http.StatusConflict, "could not join room, please retry")
	}
}

// openAPI serves the embedded spec so external tooling can pull it off a
// running server.
func (h *Handlers) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.OpenAPISpec)
}

// decodeJSON reads a JSON body and rejects unknown fields, so typos in a
// request surface as a 400 instead of being silently ignored.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}
