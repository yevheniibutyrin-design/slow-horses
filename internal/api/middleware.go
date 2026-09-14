package api

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
)

// subjectKey carries the authenticated caller through the request context.
type subjectKeyType struct{}

var subjectKey subjectKeyType

// subjectFrom returns the authenticated caller for a request, or "" when there
// is none.
//
// On a guarded route it is always present. On one of the optionalAuthPatterns
// routes it is present only if the caller sent a usable token, so "" there means
// an anonymous reader and NOT a programming error — see the note on those
// patterns about what an empty subject must never be allowed to match.
func subjectFrom(r *http.Request) string {
	subject, _ := r.Context().Value(subjectKey).(string)
	return subject
}

// statusRecorder captures the status code so the logger can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// logging prints one line per request: method, path, status, duration.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// recovering turns a handler panic into a 500 instead of a dead connection.
func recovering(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("api: panic on %s %s: %v", r.Method, r.URL.Path, v)
				writeError(w, http.StatusInternalServerError, codeInternal, "something went wrong on our side")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// cors answers for the configured origins, or for every origin when the list is
// the single wildcard "*".
//
// An origin that is not allowed simply gets no CORS headers back, so the browser
// refuses the response. The request itself is still served: CORS is a browser
// policy, not an authorisation check, and pretending otherwise would give a
// false sense of a boundary.
//
// Access-Control-Allow-Credentials is never set, and must not be: a browser
// rejects it outright alongside "*", and nothing here needs it. The bearer token
// rides a header the caller sets deliberately, not an ambient cookie, so a
// cross-origin page cannot have one attached on its behalf.
func cors(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := slices.Contains(allowedOrigins, "*")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case allowAll:
				// No Vary: the answer is the same for every origin, so a shared
				// cache has nothing to get wrong.
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", "300")
			case origin != "" && slices.Contains(allowedOrigins, origin):
				w.Header().Set("Access-Control-Allow-Origin", origin)
				// The response varies by origin, so a shared cache must not serve
				// one origin's headers to another.
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", "300")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// publicPaths need no credential: a liveness probe and the served spec.
var publicPaths = []string{"/ping", "/openapi.yaml"}

// optionalAuthPatterns are the read-only routes an anonymous caller may reach,
// so a widget can show a duel before anyone has signed in.
//
// A valid token is still honoured on them — a participant reading their own room
// has to be recognised as one — but its absence is not an error, and neither is
// a token that does not verify: on these routes an unusable credential reads as
// no credential rather than a 401.
//
// Only reads are listed, and that boundary is load-bearing rather than
// conservative. Anonymous callers all share the empty subject, so admitting a
// write here would make one anonymous caller indistinguishable from every other:
// any of them could release or confirm any other's seat hold. Every rule that
// turns on WHO is calling — seat ownership, the creator's own rooms being
// excluded from the open list, the rematch restriction, the daily duel count —
// still needs a real token. models.ParticipantBySubject already refuses to match
// on "", which stops an empty subject from ever reading as a participant.
//
// These are ServeMux patterns compared against the pattern the mux itself
// resolved, not hand-matched path shapes, so they cannot drift out of step with
// the routes registered in NewRouter.
var optionalAuthPatterns = []string{
	"GET /rooms",
	"POST /rooms/{roomId}/read",
}

// authenticating resolves the caller from the bearer token and puts the subject
// on the request context.
//
// Guarded routes need it, because the room contract carries no user identifier
// in any payload — the token is the only caller identity the service gets. A
// request without a valid one is a 401 and nothing is created or changed.
func authenticating(verifier auth.Verifier, mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(publicPaths, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Ask the mux which route this is. An unmatched path resolves to the
			// 404 handler with an empty pattern, which is on no list and so still
			// demands a credential — the default stays closed.
			_, pattern := mux.Handler(r)
			optional := slices.Contains(optionalAuthPatterns, pattern)

			// The token travels only in this header. Never read it from a query
			// string or a body: both are logged and cached where a header is not.
			token, ok := auth.BearerToken(r.Header.Get("Authorization"))
			if !ok {
				if optional {
					next.ServeHTTP(w, r)
					return
				}
				writeError(w, http.StatusUnauthorized, codeUnauthorised, "a bearer credential is required")
				return
			}
			subject, err := verifier.Subject(token)
			if err != nil {
				if optional {
					next.ServeHTTP(w, r)
					return
				}
				// The reason is deliberately not distinguished to the caller.
				writeError(w, http.StatusUnauthorized, codeUnauthorised, "the bearer credential is not valid")
				return
			}

			ctx := context.WithValue(r.Context(), subjectKey, subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// chain applies middleware so the first listed is the outermost.
func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

// trimmed is shorthand for the validation the handlers do on every string field.
func trimmed(s string) string { return strings.TrimSpace(s) }
