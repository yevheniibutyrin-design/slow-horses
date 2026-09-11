package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// errorBody is the error shape the widget's room client reads.
//
// The key is "message", NOT "error". `readErrorMessage` in the client reads
// body.message and falls back to the HTTP status text, so an "error" key would
// silently drop every stated reason — and a large share of the widget's
// scenarios are "the reason given is ...". Changing this key is what broke the
// boilerplate's own smoke script and Postman collection; both were updated with
// it rather than leaving two error shapes in one service.
//
// Code is additive: logs and future client branching. The widget reads only
// Message.
type errorBody struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// Machine-readable reasons. These accompany the human message; the widget maps
// only the HTTP status, so these exist for logs and for a later client that wants
// to branch without parsing prose.
const (
	codeUnauthorised    = "unauthorised"
	codeForbidden       = "forbidden"
	codeNotFound        = "notFound"
	codeRoomFilled      = "roomFilled"
	codeSeatHeld        = "seatHeld"
	codeAlreadyJoined   = "alreadyJoined"
	codeRoomExpired     = "roomExpired"
	codeHoldExpired     = "holdExpired"
	codeInvalidRequest  = "invalidRequest"
	codeUnknownStrategy = "unknownStrategy"
	codeUnderround      = "underroundPrices"
	codeDailyLimit      = "dailyLimitReached"
	codePerDuelMax      = "perDuelMaxExceeded"
	codeInternal        = "internal"
)

// writeJSON serialises v as the response body.
//
// Deliberately UNWRAPPED: the widget's client does `(await response.json()) as
// TData` with no unwrapping at all, so a {"data": ...} envelope would make every
// field arrive undefined.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	// Off because this is a JSON API, never markup. Left on, Go escapes & < >
	// as \u0026 and friends — which is valid JSON and decodes correctly, but
	// turns every inviteUrl into an unreadable string in logs and in curl.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Headers are already out; all we can do is record it.
		log.Printf("api: encode response: %v", err)
	}
}

// writeError returns {"message": "...", "code": "..."} with the given status.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Message: msg, Code: code})
}

// internalError logs the cause and returns a generic message, so an internal
// error's text never reaches the client.
func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("api: %s: %v", what, err)
	writeError(w, http.StatusInternalServerError, codeInternal, "something went wrong on our side")
}
