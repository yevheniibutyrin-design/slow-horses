package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The three wire-format facts below live in the widget's transport layer rather
// than in any spec, and each one fails SILENTLY: the request succeeds, the
// widget renders, and the wrong thing is on screen. They are asserted first for
// that reason.

// The client does `(await response.json()) as TData` with no unwrapping at all,
// so an envelope would make every field arrive undefined.
func TestSuccessBodyIsNotEnveloped(t *testing.T) {
	e := newEnv(t)
	res := e.do(http.MethodPost, "/rooms", "alice",
		createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00"))

	var raw map[string]json.RawMessage
	e.decode(res, http.StatusCreated, &raw)

	if _, enveloped := raw["data"]; enveloped {
		t.Error("response is wrapped in a data envelope; the client does not unwrap")
	}
	for _, field := range []string{"id", "eventId", "strategy", "status", "capacity", "participants"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("response has no top-level %q, so the body is not the room object itself", field)
		}
	}
}

// readErrorMessage reads body.message and falls back to the HTTP status text.
// The boilerplate emitted {"error": ...}, which would drop every stated reason.
func TestErrorBodyUsesMessageNotError(t *testing.T) {
	e := newEnv(t)
	res := e.do(http.MethodPost, "/rooms", "alice", map[string]any{"eventId": ""})

	if res.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", res.status, res.body)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.body, &raw); err != nil {
		t.Fatalf("decode error body %s: %v", res.body, err)
	}
	if _, wrong := raw["error"]; wrong {
		t.Error(`error body uses the "error" key; the client reads body.message and would show the status text instead`)
	}
	message, ok := raw["message"]
	if !ok {
		t.Fatal(`error body has no "message" key`)
	}
	var text string
	if err := json.Unmarshal(message, &text); err != nil || strings.TrimSpace(text) == "" {
		t.Errorf("message is not a non-empty string: %s", message)
	}
	if _, ok := raw["code"]; !ok {
		t.Error(`error body has no "code"; it is additive but every error should carry one`)
	}
}

// The widget's types declare these as `number`. Go marshals time.Time to
// RFC3339 by default, which would parse as NaN and stop the countdown ticking.
func TestTimestampsAreEpochMillisecondNumbers(t *testing.T) {
	e := newEnv(t)
	res := e.do(http.MethodPost, "/rooms", "alice",
		createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00"))

	var raw map[string]json.RawMessage
	e.decode(res, http.StatusCreated, &raw)

	for _, field := range []string{"createdAt", "expiresAt"} {
		var ms int64
		if err := json.Unmarshal(raw[field], &ms); err != nil {
			t.Fatalf("%s is %s, not a JSON number: %v", field, raw[field], err)
		}
		// Sanity: milliseconds since the epoch, not seconds. Seconds would be
		// about 1.7e9 here; milliseconds about 1.7e12.
		if ms < 1_000_000_000_000 {
			t.Errorf("%s = %d looks like epoch SECONDS, not milliseconds", field, ms)
		}
	}
}

// Money crosses the wire as an unquoted JSON number with two decimals.
func TestMoneyIsABareNumber(t *testing.T) {
	e := newEnv(t)
	res := e.do(http.MethodPost, "/rooms", "alice",
		createBody("evt-1", e.nowMS()+testInviteWindowMS, 182, 205, "10.00"))

	var raw struct {
		Payload struct {
			Figures map[string]json.RawMessage `json:"figures"`
		} `json:"payload"`
	}
	e.decode(res, http.StatusCreated, &raw)

	want := map[string]string{"payout": "18.20", "entry": "8.87", "pot": "18.87"}
	for field, expected := range want {
		got := string(raw.Payload.Figures[field])
		if strings.HasPrefix(got, `"`) {
			t.Errorf("figures.%s is quoted (%s); the client's type declares it as a number", field, got)
		}
		if got != expected {
			t.Errorf("figures.%s = %s, want %s", field, got, expected)
		}
	}
}

// The caller identity the service resolves from the bearer token is its own
// bookkeeping. It must never reach a response.
func TestCallerSubjectNeverLeaks(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice-secret-subject", "evt-1")

	res := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice-secret-subject", nil)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.status, res.body)
	}
	body := string(res.body)
	if strings.Contains(body, "alice-secret-subject") {
		t.Errorf("the caller's subject appears in the response body: %s", body)
	}
	if strings.Contains(body, `"subject"`) {
		t.Errorf("a subject field was serialised: %s", body)
	}
	// The hold and the rematch restriction are internal too.
	if strings.Contains(body, `"hold"`) || strings.Contains(body, "permittedSubject") {
		t.Errorf("internal fields were serialised: %s", body)
	}
}

// Every room route needs a credential. The payload carries no identifier, so an
// unauthenticated request cannot be attributed to anyone and must change nothing.
func TestEveryRoomRouteRequiresACredential(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	routes := []struct{ method, path string }{
		{http.MethodPost, "/rooms"},
		{http.MethodGet, "/rooms?eventId=evt-1&status=open"},
		{http.MethodGet, "/rooms/limits"},
		{http.MethodPost, "/rooms/" + room.ID + "/read"},
		{http.MethodPost, "/rooms/" + room.ID + "/seat"},
		{http.MethodDelete, "/rooms/" + room.ID + "/seat/hold-1"},
		{http.MethodPost, "/rooms/" + room.ID + "/seat/hold-1/confirm"},
		{http.MethodPost, "/rooms/" + room.ID + "/rematch"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			res := e.do(route.method, route.path, "", map[string]any{})
			if res.status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401; body: %s", res.status, res.body)
			}
		})
	}
}

func TestMalformedAuthorizationHeaderIsUnauthorised(t *testing.T) {
	e := newEnv(t)
	for _, header := range []string{"Basic abc", "abc", "Bearer", "Bearer "} {
		t.Run(header, func(t *testing.T) {
			res := e.doWithRawAuth(http.MethodGet, "/rooms/limits", header)
			if res.status != http.StatusUnauthorized {
				t.Errorf("status = %d for Authorization %q, want 401", res.status, header)
			}
		})
	}
}

// Liveness and the served spec stay public, so a probe needs no credential.
func TestPublicRoutesNeedNoCredential(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/ping", "/openapi.yaml"} {
		t.Run(path, func(t *testing.T) {
			if res := e.do(http.MethodGet, path, "", nil); res.status != http.StatusOK {
				t.Errorf("status = %d, want 200", res.status)
			}
		})
	}
}

// CORS is an allowlist now, not "*". An unknown origin gets no headers back, so
// a browser refuses the response.
func TestCORSAnswersOnlyAllowedOrigins(t *testing.T) {
	e := newEnv(t)

	allowed := e.doWithOrigin(http.MethodGet, "/ping", "https://allowed.example")
	if got := allowed.header.Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Errorf("allowed origin got Access-Control-Allow-Origin %q, want the origin echoed back", got)
	}
	if got := allowed.header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want it to include Origin so a cache cannot cross origins", got)
	}

	// An origin off the list gets no CORS headers at all, which is what makes the
	// browser refuse the response. The boilerplate echoed "*" to everyone.
	denied := e.doWithOrigin(http.MethodGet, "/ping", "https://evil.example")
	if got := denied.header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("denied origin got Access-Control-Allow-Origin %q, want none", got)
	}
}
