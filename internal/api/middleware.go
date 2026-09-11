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

// subjectFrom returns the authenticated caller for a request. It is present on
// every route the auth middleware guards, so a handler that reads it on an
// unguarded route gets "" and should treat that as a programming error.
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

// cors answers only for origins on the configured allowlist. The boilerplate
// echoed "*", which is fine for a Swagger UI demo and wrong for a service that
// carries a bearer credential.
//
// An origin that is not allowed simply gets no CORS headers back, so the browser
// refuses the response. The request itself is still served: CORS is a browser
// policy, not an authorisation check, and pretending otherwise would give a
// false sense of a boundary.
func cors(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && slices.Contains(allowedOrigins, origin) {
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

// authenticating resolves the caller from the bearer token and puts the subject
// on the request context.
//
// Every room route needs it, because the room contract carries no user
// identifier in any payload — the token is the only caller identity the service
// gets. A request without a valid one is a 401 and nothing is created or changed.
func authenticating(verifier auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(publicPaths, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// The token travels only in this header. Never read it from a query
			// string or a body: both are logged and cached where a header is not.
			token, ok := auth.BearerToken(r.Header.Get("Authorization"))
			if !ok {
				writeError(w, http.StatusUnauthorized, codeUnauthorised, "a bearer credential is required")
				return
			}
			subject, err := verifier.Subject(token)
			if err != nil {
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
