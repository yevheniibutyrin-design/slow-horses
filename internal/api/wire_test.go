package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/api"
	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
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

// Every route that WRITES, or that answers "what have I done", still needs a
// credential. The payload carries no identifier, so an unauthenticated request
// cannot be attributed to anyone and must change nothing.
//
// The two read routes are deliberately absent: see the anonymous tests below.
func TestEveryRoomWriteRouteRequiresACredential(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	routes := []struct{ method, path string }{
		{http.MethodPost, "/rooms"},
		{http.MethodGet, "/rooms/limits"},
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

// A spectator with no credential can read a room and list the open ones, so a
// widget can render a duel before anyone has signed in.
func TestReadRoutesAllowAnAnonymousCaller(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	read := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "", map[string]any{})
	if read.status != http.StatusOK {
		t.Fatalf("anonymous read status = %d, want 200; body: %s", read.status, read.body)
	}

	list := e.do(http.MethodGet, "/rooms?eventId=evt-1&status=open", "", nil)
	if list.status != http.StatusOK {
		t.Errorf("anonymous list status = %d, want 200; body: %s", list.status, list.body)
	}
}

// The open-rooms list discloses invite codes to callers who can act on them. An
// anonymous one cannot -- taking a seat needs a token -- so handing codes over
// here would only let a scraper harvest every open code on an event in a single
// unauthenticated request, defeating the gating on the read route.
func TestAnonymousListHidesInviteCodes(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	anon := e.do(http.MethodGet, "/rooms?eventId=evt-1&status=open", "", nil)
	if anon.status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", anon.status, anon.body)
	}
	if !strings.Contains(string(anon.body), room.ID) {
		t.Fatalf("precondition: the open duel was not listed at all: %s", anon.body)
	}
	if strings.Contains(string(anon.body), room.InviteCode) {
		t.Errorf("the anonymous list leaked an invite code: %s", anon.body)
	}

	// A signed-in viewer still gets it, because they can act on it. It is bob's
	// list, not alice's: a caller's own rooms are excluded from their own list.
	bob := e.do(http.MethodGet, "/rooms?eventId=evt-1&status=open", "bob", nil)
	if !strings.Contains(string(bob.body), room.InviteCode) {
		t.Errorf("a signed-in viewer was not given the invite code: %s", bob.body)
	}
}

// The invite code is the secret that authorises taking the seat. A reader who is
// neither a participant nor already holding it must not be handed it, or a
// scraped room id would be worth as much as a code.
func TestAnonymousReadHidesTheInviteCode(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")
	if room.InviteCode == "" {
		t.Fatal("precondition: the created room has no invite code to hide")
	}

	anon := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "", map[string]any{})
	if anon.status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", anon.status, anon.body)
	}
	if strings.Contains(string(anon.body), room.InviteCode) {
		t.Errorf("anonymous read leaked the invite code: %s", anon.body)
	}

	// The participant still gets it back, because sharing the invite needs it.
	owner := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "alice", map[string]any{})
	if !strings.Contains(string(owner.body), room.InviteCode) {
		t.Errorf("the room's own creator was not given the invite code: %s", owner.body)
	}

	// So does a caller who presented the code, who plainly already has it.
	holder := e.do(http.MethodPost, "/rooms/"+room.ID+"/read", "", map[string]any{
		"inviteCode": room.InviteCode,
	})
	if !strings.Contains(string(holder.body), room.InviteCode) {
		t.Errorf("a code holder was not given the invite code back: %s", holder.body)
	}
}

// On an open route an unusable credential reads as no credential rather than a
// 401 — but it must never be mistaken for a real subject.
func TestMalformedAuthorizationIsAnonymousOnOpenRoutesAndUnauthorisedOnGuardedOnes(t *testing.T) {
	e := newEnv(t)
	room := e.createRoom("alice", "evt-1")

	for _, header := range []string{"Basic abc", "abc", "Bearer", "Bearer "} {
		t.Run(header, func(t *testing.T) {
			guarded := e.doWithRawAuth(http.MethodGet, "/rooms/limits", header)
			if guarded.status != http.StatusUnauthorized {
				t.Errorf("guarded route status = %d for Authorization %q, want 401", guarded.status, header)
			}

			open := e.doWithRawAuth(http.MethodPost, "/rooms/"+room.ID+"/read", header)
			if open.status != http.StatusOK {
				t.Errorf("open route status = %d for Authorization %q, want 200", open.status, header)
			}
			if strings.Contains(string(open.body), room.InviteCode) {
				t.Errorf("Authorization %q was treated as a participant: %s", header, open.body)
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

// A configured allowlist still behaves as one: an unknown origin gets no headers
// back, so a browser refuses the response. "*" is the default, not the only mode
// — see TestCORSWildcardAnswersEveryOrigin.
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

// Configured with "*", every origin is answered, including ones nobody listed.
//
// This needs no database, so it builds its own server rather than going through
// newEnv, which skips when Mongo is unreachable.
func TestCORSWildcardAnswersEveryOrigin(t *testing.T) {
	server := httptest.NewServer(api.NewRouter(&api.Handlers{}, auth.InsecureVerifier{}, []string{"*"}))
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/ping", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", "https://anywhere.example")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer res.Body.Close()

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	// A browser rejects "*" outright when credentials are allowed, so this header
	// must stay absent for the wildcard to work at all.
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want none alongside \"*\"", got)
	}
}
