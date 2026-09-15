package api_test

// Integration tests for the free vote, against a REAL MongoDB.
//
// The one-vote-per-market rule is a unique index, not a check in Go, so a fake
// would assert this test's own logic rather than the property that matters —
// the same reason the seat claim is tested this way.

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
)

// ---------------------------------------------------------------------------
// Wire shapes, written out rather than imported: these tests are an external
// package on purpose, and a response that stopped matching the widget's reading
// of it should fail here.
// ---------------------------------------------------------------------------

type outcomeKeyWire struct {
	Type   int      `json:"type"`
	Values []string `json:"values"`
}

type marketKeyWire struct {
	EventID    string `json:"eventId"`
	MarketType int    `json:"marketType"`
	Period     int    `json:"period"`
	ResultKind int    `json:"resultKind"`
	SubPeriod  *int   `json:"subPeriod,omitempty"`
}

type outcomeCountWire struct {
	Key   outcomeKeyWire `json:"key"`
	Count int64          `json:"count"`
}

type marketCountsWire struct {
	Market   marketKeyWire      `json:"market"`
	Outcomes []outcomeCountWire `json:"outcomes"`
}

type countsWire struct {
	Markets    []marketCountsWire `json:"markets"`
	VotedToday int64              `json:"votedToday"`
}

type castWire struct {
	Market      marketKeyWire      `json:"market"`
	Outcomes    []outcomeCountWire `json:"outcomes"`
	DeviceToken string             `json:"deviceToken"`
	VotedToday  int64              `json:"votedToday"`
}

// ---------------------------------------------------------------------------
// Request builders
// ---------------------------------------------------------------------------

// market is the market key the widget sends, minus the fields a test overrides.
func market(eventID string) map[string]any {
	return map[string]any{
		"eventId":    eventID,
		"marketType": 1,
		"period":     0,
		"resultKind": 2,
	}
}

func outcome(kind int, values ...string) map[string]any {
	return map[string]any{"type": kind, "values": values}
}

// countsBody asks for one market's outcomes.
func countsBody(m map[string]any, outcomes ...map[string]any) map[string]any {
	return map[string]any{
		"markets": []any{map[string]any{"market": m, "outcomes": outcomes}},
	}
}

func voteBody(m, picked map[string]any) map[string]any {
	return map[string]any{"market": m, "outcome": picked}
}

// ---------------------------------------------------------------------------
// Reading counts
// ---------------------------------------------------------------------------

// A market nobody has voted in is not an error. It answers zeros — the widget
// renders a card before anyone has picked anything.
func TestVoteCountsUnknownMarketIsZeroNotAnError(t *testing.T) {
	e := newEnv(t)

	res := e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(1, "home"), outcome(1, "away")))

	var got countsWire
	e.decode(res, http.StatusOK, &got)

	if len(got.Markets) != 1 {
		t.Fatalf("markets = %d, want 1", len(got.Markets))
	}
	if len(got.Markets[0].Outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2 — every requested outcome must be answered",
			len(got.Markets[0].Outcomes))
	}
	for _, o := range got.Markets[0].Outcomes {
		if o.Count != 0 {
			t.Errorf("count for %v = %d, want 0", o.Key.Values, o.Count)
		}
	}
}

// The batched read is the whole reason this route takes an array: one request
// per widget mount, not one per card. With one response per card the header's
// votedToday races against itself.
func TestVoteCountsAnswersEveryMarketInOneRequest(t *testing.T) {
	e := newEnv(t)

	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-a"), outcome(1, "home"))), http.StatusOK, nil)
	e.decode(e.do(http.MethodPost, "/votes", "bob",
		voteBody(market("evt-b"), outcome(1, "away"))), http.StatusOK, nil)

	res := e.do(http.MethodPost, "/votes/counts", "", map[string]any{
		"markets": []any{
			map[string]any{"market": market("evt-a"), "outcomes": []any{outcome(1, "home")}},
			map[string]any{"market": market("evt-b"), "outcomes": []any{outcome(1, "away")}},
			map[string]any{"market": market("evt-c"), "outcomes": []any{outcome(1, "home")}},
		},
	})

	var got countsWire
	e.decode(res, http.StatusOK, &got)

	if len(got.Markets) != 3 {
		t.Fatalf("markets = %d, want 3 — one entry per requested market, in request order",
			len(got.Markets))
	}
	want := []struct {
		eventID string
		count   int64
	}{{"evt-a", 1}, {"evt-b", 1}, {"evt-c", 0}}
	for i, w := range want {
		if got.Markets[i].Market.EventID != w.eventID {
			t.Errorf("markets[%d].market.eventId = %q, want %q — the response must keep request order",
				i, got.Markets[i].Market.EventID, w.eventID)
		}
		if got.Markets[i].Outcomes[0].Count != w.count {
			t.Errorf("markets[%d] count = %d, want %d", i, got.Markets[i].Outcomes[0].Count, w.count)
		}
	}
}

// The client looks a count up by JSON.stringify of the key object it sent, so
// the key that comes back must be the key that went out — byte for byte, order
// included. Normalising it here is the c971a538f failure: a silent no-op that
// renders zeros on a card that has votes.
func TestVoteCountsEchoesKeysExactlyAsSent(t *testing.T) {
	e := newEnv(t)

	res := e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(7, "zebra", "alpha")))

	var got countsWire
	e.decode(res, http.StatusOK, &got)

	values := got.Markets[0].Outcomes[0].Key.Values
	if len(values) != 2 || values[0] != "zebra" || values[1] != "alpha" {
		t.Fatalf("key.values = %v, want [zebra alpha] — the response must not reorder the "+
			"caller's own key, or every client-side lookup misses", values)
	}
	if kind := got.Markets[0].Outcomes[0].Key.Type; kind != 7 {
		t.Errorf("key.type = %d, want 7", kind)
	}
}

// The counts response is served from a shared cache path, so nothing about the
// caller may ride on it. This codebase has been burned on that once already.
func TestVoteCountsCarryNothingCallerSpecific(t *testing.T) {
	e := newEnv(t)

	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, nil)

	res := e.do(http.MethodPost, "/votes/counts", "alice",
		countsBody(market("evt-1"), outcome(1, "home")))

	var raw map[string]any
	e.decode(res, http.StatusOK, &raw)

	for key := range raw {
		if key != "markets" && key != "votedToday" {
			t.Errorf("counts response carries %q; only anonymous aggregates belong on this path", key)
		}
	}
}

// Anonymous callers read counts. The widget shows the crowd split before anyone
// has signed in.
func TestVoteCountsNeedNoCredential(t *testing.T) {
	e := newEnv(t)

	res := e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(1, "home")))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.status, res.body)
	}
}

// ---------------------------------------------------------------------------
// The vote key
// ---------------------------------------------------------------------------

// layout is presentational. Keying on it would fragment one market's votes
// across the layouts it renders in, so a vote cast from one layout must be
// counted for a reader on another.
func TestVoteKeyIgnoresLayout(t *testing.T) {
	e := newEnv(t)

	voted := market("evt-1")
	voted["layout"] = "horizontal"
	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(voted, outcome(1, "home"))), http.StatusOK, nil)

	read := market("evt-1")
	read["layout"] = "vertical"
	var got countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(read, outcome(1, "home"))), http.StatusOK, &got)

	if n := got.Markets[0].Outcomes[0].Count; n != 1 {
		t.Fatalf("count = %d, want 1 — layout must not open a second bucket", n)
	}
}

// The order of `values` is the client's, not the identity's. Sorting for the
// stored key is what keeps ["1","2"] and ["2","1"] one bucket.
func TestVoteKeyIgnoresOutcomeValueOrder(t *testing.T) {
	e := newEnv(t)

	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-1"), outcome(3, "1", "2"))), http.StatusOK, nil)

	var got countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(3, "2", "1"))), http.StatusOK, &got)

	if n := got.Markets[0].Outcomes[0].Count; n != 1 {
		t.Fatalf("count = %d, want 1 — a reordered values array is the same outcome", n)
	}
}

// Absent and null are the same market; zero is a different one. A plain int
// would collapse all three.
func TestVoteKeyTreatsAbsentSubPeriodAsNullAndNotAsZero(t *testing.T) {
	e := newEnv(t)

	absent := market("evt-1")
	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(absent, outcome(1, "home"))), http.StatusOK, nil)

	explicitNull := market("evt-1")
	explicitNull["subPeriod"] = nil
	var sameBucket countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(explicitNull, outcome(1, "home"))), http.StatusOK, &sameBucket)
	if n := sameBucket.Markets[0].Outcomes[0].Count; n != 1 {
		t.Errorf("subPeriod null count = %d, want 1 — absent and null are one market", n)
	}

	zero := market("evt-1")
	zero["subPeriod"] = 0
	var otherBucket countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(zero, outcome(1, "home"))), http.StatusOK, &otherBucket)
	if n := otherBucket.Markets[0].Outcomes[0].Count; n != 0 {
		t.Errorf("subPeriod 0 count = %d, want 0 — subPeriod 0 is a different market", n)
	}
}

// A missing identifying number would otherwise vote silently in market type 0.
func TestVoteRejectsAMarketKeyMissingANumber(t *testing.T) {
	e := newEnv(t)

	incomplete := map[string]any{"eventId": "evt-1", "period": 0, "resultKind": 2}
	res := e.do(http.MethodPost, "/votes", "alice", voteBody(incomplete, outcome(1, "home")))
	if res.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", res.status, res.body)
	}
}

// decodeJSON rejects unknown fields, so a typo is a 400 and not a vote in the
// wrong bucket.
func TestVoteRejectsAnUnknownField(t *testing.T) {
	e := newEnv(t)

	body := voteBody(market("evt-1"), outcome(1, "home"))
	body["marketKey"] = "total_goals"
	res := e.do(http.MethodPost, "/votes", "alice", body)
	if res.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", res.status, res.body)
	}
}

// ---------------------------------------------------------------------------
// Casting a vote
// ---------------------------------------------------------------------------

// The write returns fresh counts, which is what lets the client stop
// optimistically incrementing a number it cannot know.
func TestCastVoteReturnsTheUpdatedCounts(t *testing.T) {
	e := newEnv(t)

	body := voteBody(market("evt-1"), outcome(1, "home"))
	body["outcomes"] = []any{outcome(1, "home"), outcome(1, "draw"), outcome(1, "away")}

	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "alice", body), http.StatusOK, &got)

	if len(got.Outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3 — the card renders the whole split", len(got.Outcomes))
	}
	if got.Outcomes[0].Count != 1 {
		t.Errorf("picked outcome count = %d, want 1", got.Outcomes[0].Count)
	}
	for _, o := range got.Outcomes[1:] {
		if o.Count != 0 {
			t.Errorf("unvoted outcome %v count = %d, want 0", o.Key.Values, o.Count)
		}
	}
	if got.VotedToday < 1 {
		t.Errorf("votedToday = %d, want at least 1", got.VotedToday)
	}
}

// Votes are final. Changing a pick is a conflict, and it carries its own code so
// the client can reconcile local state instead of logging a generic failure.
func TestCastVoteRefusesToChangeAPick(t *testing.T) {
	e := newEnv(t)

	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, nil)

	res := e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-1"), outcome(1, "away")))
	if res.status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", res.status, res.body)
	}

	var body struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(res.body, &body); err != nil {
		t.Fatalf("decode error body %s: %v", res.body, err)
	}
	if body.Code != "alreadyVoted" {
		t.Errorf("code = %q, want alreadyVoted — a duplicate must be distinguishable "+
			"from a generic failure", body.Code)
	}
	if body.Message == "" {
		t.Error("message is empty; the widget reads body.message and shows the status line without it")
	}

	var counts countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(1, "home"), outcome(1, "away"))), http.StatusOK, &counts)
	if counts.Markets[0].Outcomes[0].Count != 1 || counts.Markets[0].Outcomes[1].Count != 0 {
		t.Errorf("counts = %d/%d, want 1/0 — a refused vote must not be recorded",
			counts.Markets[0].Outcomes[0].Count, counts.Markets[0].Outcomes[1].Count)
	}
}

// A retried request for the same pick is a retry, not a second vote. It answers
// with the counts the caller wanted, and the count does not move.
func TestCastVoteIsIdempotentForTheSamePick(t *testing.T) {
	e := newEnv(t)

	body := voteBody(market("evt-1"), outcome(1, "home"))
	e.decode(e.do(http.MethodPost, "/votes", "alice", body), http.StatusOK, nil)

	var second castWire
	e.decode(e.do(http.MethodPost, "/votes", "alice", body), http.StatusOK, &second)

	if second.Outcomes[0].Count != 1 {
		t.Fatalf("count after retry = %d, want 1 — a retry must not vote twice",
			second.Outcomes[0].Count)
	}
}

func TestCastVoteCountsTwoCallersSeparately(t *testing.T) {
	e := newEnv(t)

	e.decode(e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, nil)
	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "bob",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &got)

	if got.Outcomes[0].Count != 2 {
		t.Fatalf("count = %d, want 2", got.Outcomes[0].Count)
	}
}

// One caller, many simultaneous votes, exactly one recorded. This is a property
// of the unique index, not of a check in Go: a check-then-write would let two
// concurrent first votes both pass.
func TestCastVoteRecordsOneVoteUnderConcurrency(t *testing.T) {
	e := newEnv(t)

	const attempts = 12
	var wg sync.WaitGroup
	statuses := make([]int, attempts)
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = e.do(http.MethodPost, "/votes", "alice",
				voteBody(market("evt-1"), outcome(1, "home"))).status
		}()
	}
	wg.Wait()

	for i, status := range statuses {
		// Every attempt is the same pick, so each is either the vote or a retry
		// of it. None may be an error.
		if status != http.StatusOK {
			t.Errorf("attempt %d status = %d, want 200", i, status)
		}
	}

	var counts countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &counts)
	if n := counts.Markets[0].Outcomes[0].Count; n != 1 {
		t.Fatalf("count = %d, want exactly 1 after %d simultaneous votes", n, attempts)
	}
}

// ---------------------------------------------------------------------------
// Anonymous voters
// ---------------------------------------------------------------------------

// The free vote is free: no sign-in. The identity comes back on the WRITE
// response, which is per-caller and uncached — never on the counts response.
func TestAnonymousVoteMintsADeviceToken(t *testing.T) {
	e := newEnv(t)

	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &got)

	if got.DeviceToken == "" {
		t.Fatal("no deviceToken minted; an anonymous voter with no identity cannot be held to one vote")
	}
	if got.Outcomes[0].Count != 1 {
		t.Errorf("count = %d, want 1", got.Outcomes[0].Count)
	}
}

// Presenting the token back is what makes the anonymous rule enforceable at all.
func TestAnonymousVoteWithItsTokenCannotChangeThePick(t *testing.T) {
	e := newEnv(t)

	var first castWire
	e.decode(e.do(http.MethodPost, "/votes", "",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &first)

	second := voteBody(market("evt-1"), outcome(1, "away"))
	second["deviceToken"] = first.DeviceToken
	res := e.do(http.MethodPost, "/votes", "", second)

	if res.status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", res.status, res.body)
	}
}

// A device id the CLIENT made up must not be accepted: it is free to mint, and
// it can be pointed at someone else's identity to lock them out of a market.
// An unsigned one reads as no token, so the caller is issued a real one.
func TestAnonymousVoteRejectsAClientMintedDeviceID(t *testing.T) {
	e := newEnv(t)

	forged := voteBody(market("evt-1"), outcome(1, "home"))
	forged["deviceToken"] = "6f1b2c3d4e5f60718293a4b5c6d7e8f9.not-a-real-signature"

	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "", forged), http.StatusOK, &got)

	if got.DeviceToken == "" {
		t.Fatal("an unsigned device token must be replaced by one this service issued")
	}
	if got.DeviceToken == forged["deviceToken"] {
		t.Fatal("the client's own device token was accepted verbatim")
	}
}

// Discarding the token buys another vote — which is the status quo product rule
// for a device-local pick, and exactly why the address limit below exists.
func TestAnonymousVoteWithoutItsTokenIsANewVoter(t *testing.T) {
	e := newEnv(t)

	e.decode(e.do(http.MethodPost, "/votes", "",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, nil)
	var second castWire
	e.decode(e.do(http.MethodPost, "/votes", "",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &second)

	if second.Outcomes[0].Count != 2 {
		t.Fatalf("count = %d, want 2 — a discarded token is a new voter", second.Outcomes[0].Count)
	}
}

// The real ceiling on anonymous voting. A device token bounds nothing on its
// own, so the limit is per address and per UTC day.
func TestAnonymousVotesAreRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t)

	for i := range testAnonVotesPerIPPerDay {
		res := e.do(http.MethodPost, "/votes", "",
			voteBody(market("evt-1"), outcome(1, "home")))
		if res.status != http.StatusOK {
			t.Fatalf("vote %d status = %d, want 200; body: %s", i, res.status, res.body)
		}
	}

	res := e.do(http.MethodPost, "/votes", "", voteBody(market("evt-1"), outcome(1, "home")))
	if res.status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the per-address limit is reached; body: %s",
			res.status, res.body)
	}

	// A signed-in player is unaffected: the limit exists because an anonymous
	// identity is free to re-mint, and an account is not.
	if res := e.do(http.MethodPost, "/votes", "alice",
		voteBody(market("evt-1"), outcome(1, "home"))); res.status != http.StatusOK {
		t.Fatalf("authenticated status = %d, want 200 — the anonymous limit must not "+
			"reach a signed-in player; body: %s", res.status, res.body)
	}
}

// A signed-in player is identified by their account, so a stale device token in
// their storage cannot change who they vote as.
func TestAuthenticatedVoteIgnoresADeviceToken(t *testing.T) {
	e := newEnv(t)

	var anon castWire
	e.decode(e.do(http.MethodPost, "/votes", "",
		voteBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &anon)

	body := voteBody(market("evt-1"), outcome(1, "away"))
	body["deviceToken"] = anon.DeviceToken

	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "alice", body), http.StatusOK, &got)

	if got.DeviceToken != "" {
		t.Error("a device token was minted for a signed-in caller")
	}
	if got.Outcomes[0].Count != 1 {
		t.Errorf("count = %d, want 1 — the account's own vote, not the device's",
			got.Outcomes[0].Count)
	}
}

// The write answers in the caller's own order, the same way the counts route
// does — a response that reorders the keys is one the card cannot read back.
func TestCastVoteAnswersOutcomesInRequestOrder(t *testing.T) {
	e := newEnv(t)

	body := voteBody(market("evt-1"), outcome(1, "away"))
	body["outcomes"] = []any{outcome(1, "home"), outcome(1, "draw"), outcome(1, "away")}

	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "alice", body), http.StatusOK, &got)

	want := []string{"home", "draw", "away"}
	if len(got.Outcomes) != len(want) {
		t.Fatalf("outcomes = %d, want %d", len(got.Outcomes), len(want))
	}
	for i, w := range want {
		if got.Outcomes[i].Key.Values[0] != w {
			t.Errorf("outcomes[%d] = %q, want %q — the picked outcome must not jump to the front",
				i, got.Outcomes[i].Key.Values[0], w)
		}
	}
	if got.Outcomes[2].Count != 1 {
		t.Errorf("picked outcome count = %d, want 1", got.Outcomes[2].Count)
	}
}

// A client that sends only `outcome` still learns what its own vote did.
func TestCastVoteAnswersForThePickEvenWhenItIsNotListed(t *testing.T) {
	e := newEnv(t)

	body := voteBody(market("evt-1"), outcome(1, "away"))
	body["outcomes"] = []any{outcome(1, "home")}

	var got castWire
	e.decode(e.do(http.MethodPost, "/votes", "alice", body), http.StatusOK, &got)

	if len(got.Outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2 — the pick is answered for even when unlisted", len(got.Outcomes))
	}
	if got.Outcomes[1].Key.Values[0] != "away" || got.Outcomes[1].Count != 1 {
		t.Errorf("last outcome = %v/%d, want away/1", got.Outcomes[1].Key.Values, got.Outcomes[1].Count)
	}
}

// A credential that does not verify must NOT fall through to an anonymous vote.
//
// Treated as anonymous, the caller is recorded as a device; once their session
// refreshes they vote again in the same market as their account — two votes from
// one human, both counted, because the unique index is per voter id and those
// are two different ones. "One vote per market per account" has to hold across a
// credential that has just lapsed.
//
// This runs on a REAL verifier: InsecureVerifier fails only on an empty token,
// which BearerToken already rejects, so this path is unreachable under it — in
// the rest of these tests and in local development alike.
func TestVoteRefusesACredentialThatDoesNotVerify(t *testing.T) {
	e := newEnvWithVerifier(t, auth.NewHS256Verifier([]byte("a-real-signing-secret")))

	res := e.do(http.MethodPost, "/votes", "not.a.valid.jwt",
		voteBody(market("evt-1"), outcome(1, "home")))
	if res.status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — an unusable credential must not vote anonymously; body: %s",
			res.status, res.body)
	}

	// Nothing was recorded.
	var counts countsWire
	e.decode(e.do(http.MethodPost, "/votes/counts", "",
		countsBody(market("evt-1"), outcome(1, "home"))), http.StatusOK, &counts)
	if n := counts.Markets[0].Outcomes[0].Count; n != 0 {
		t.Fatalf("count = %d, want 0 — a refused vote must not be recorded", n)
	}
}

// Sending NO credential is still fine on the same route: that is the free vote,
// and it is the whole feature. Absence and invalidity are different answers.
func TestVoteStillAllowsNoCredentialUnderARealVerifier(t *testing.T) {
	e := newEnvWithVerifier(t, auth.NewHS256Verifier([]byte("a-real-signing-secret")))

	res := e.do(http.MethodPost, "/votes", "", voteBody(market("evt-1"), outcome(1, "home")))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the poll is free; body: %s", res.status, res.body)
	}
}

// The reads keep their existing behaviour, which is deliberate and different: on
// a read an unusable credential reads as no credential rather than a 401.
func TestVoteCountsTolerateACredentialThatDoesNotVerify(t *testing.T) {
	e := newEnvWithVerifier(t, auth.NewHS256Verifier([]byte("a-real-signing-secret")))

	res := e.do(http.MethodPost, "/votes/counts", "not.a.valid.jwt",
		countsBody(market("evt-1"), outcome(1, "home")))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — counts are an anonymous aggregate; body: %s",
			res.status, res.body)
	}
}
