package api

import (
	"errors"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
	"github.com/yevheniibutyrin-design/slow-horses/internal/store"
)

// The free "Who wins?" vote. A player picks an outcome for nothing, sees the
// crowd split beside the odds-implied one, and may then back the same outcome in
// the betslip as an ordinary single bet. Nothing here touches a room, a bet or
// money.
//
// Two routes, and the split between them is a requirement rather than a layout
// choice: the counts read is anonymous and cacheable, so nothing caller-specific
// may ride on it. The device token a new anonymous voter is issued comes back on
// the WRITE response, which is per-caller and not cached.

// maxVoteBatchMarkets and maxVoteBatchOutcomes bound one counts request. The
// widget asks for the three to five cards on screen; these are far above that
// and exist so one request cannot turn into an unbounded aggregation.
const (
	maxVoteBatchMarkets  = 50
	maxVoteBatchOutcomes = 50
)

// ---------------------------------------------------------------------------
// Request shapes
//
// Every field a client sends must be declared: decodeJSON rejects unknown ones.
// ---------------------------------------------------------------------------

// marketRequest is TMarketModel as it arrives.
//
// The three required numbers are POINTERS so a missing field is a 400 rather
// than a silent vote in market type 0 — for a value whose whole job is to
// identify a bucket, "absent" and "zero" must not be the same request.
//
// Layout is DECLARED AND IGNORED, and both halves matter. Ignored, because it is
// presentational and keying on it would split one market's votes across the
// layouts it renders in. Declared, because decodeJSON rejects unknown fields, so
// leaving it out would turn a client that sends the whole TMarketModel — which
// is the natural thing to do — into a 400 on the entire batch. Same treatment as
// duelPayloadRequest.WinnerParticipantID.
type marketRequest struct {
	EventID    string `json:"eventId"`
	MarketType *int   `json:"marketType"`
	Period     *int   `json:"period"`
	ResultKind *int   `json:"resultKind"`
	SubPeriod  *int   `json:"subPeriod"`
	Layout     string `json:"layout"`
}

// validate returns the key to count on and the key to echo back.
//
// They differ in exactly one way, deliberately: the eventId is trimmed for the
// KEY, so a stray space cannot open a second bucket for one market, and echoed
// UNTRIMMED, because the client looks the response up by stringifying the object
// it sent. Normalising what comes back would miss that lookup and render zeros
// on a card that has votes.
func (m marketRequest) validate(field string) (key, echo models.MarketKey, msg string) {
	eventID := trimmed(m.EventID)
	switch {
	case eventID == "":
		return key, echo, field + ".eventId is required"
	case m.MarketType == nil:
		return key, echo, field + ".marketType is required"
	case m.Period == nil:
		return key, echo, field + ".period is required"
	case m.ResultKind == nil:
		return key, echo, field + ".resultKind is required"
	}

	key = models.MarketKey{
		EventID:    eventID,
		MarketType: *m.MarketType,
		Period:     *m.Period,
		ResultKind: *m.ResultKind,
		SubPeriod:  m.SubPeriod,
	}
	echo = key
	echo.EventID = m.EventID
	return key, echo, ""
}

// outcomeRequest is TOutcomeKey as it arrives. Type is a pointer for the same
// reason the market's numbers are.
type outcomeRequest struct {
	Type   *int     `json:"type"`
	Values []string `json:"values"`
}

func (o outcomeRequest) validate(field string) (models.OutcomeKey, string) {
	if o.Type == nil {
		return models.OutcomeKey{}, field + ".type is required"
	}
	values := o.Values
	if values == nil {
		// An absent list and an empty one are the same outcome. Stored as an
		// empty slice so it never serialises as null.
		values = []string{}
	}
	// Order is preserved. CanonicalOutcomeKey sorts a copy for the stored
	// identity; this value is echoed back as it arrived.
	return models.OutcomeKey{Type: *o.Type, Values: values}, ""
}

// ---------------------------------------------------------------------------
// Response shapes
// ---------------------------------------------------------------------------

type outcomeCount struct {
	// Key is the outcome exactly as the caller sent it.
	Key   models.OutcomeKey `json:"key"`
	Count int64             `json:"count"`
}

type marketCounts struct {
	Market   models.MarketKey `json:"market"`
	Outcomes []outcomeCount   `json:"outcomes"`
}

type voteCountsResponse struct {
	Markets []marketCounts `json:"markets"`
	// VotedToday is the header's "N voted today": every vote this service has
	// recorded since the current UTC day began, site-wide. A mount-time snapshot
	// — there is no push channel here, so it does not tick on its own.
	VotedToday int64 `json:"votedToday"`
}

// ---------------------------------------------------------------------------
// POST /votes/counts
// ---------------------------------------------------------------------------

type countsMarketRequest struct {
	Market   marketRequest    `json:"market"`
	Outcomes []outcomeRequest `json:"outcomes"`
}

type voteCountsRequest struct {
	Markets []countsMarketRequest `json:"markets"`
}

// voteCounts answers the batched read: every card on screen in ONE request.
//
// A POST rather than a GET because three to five structured market keys do not
// serialize sanely into a query string — the same reason POST /rooms/{id}/read
// is a POST. It is a read: nothing is written, and an anonymous caller is served
// exactly what a signed-in one is.
func (h *Handlers) voteCounts(w http.ResponseWriter, r *http.Request) {
	var req voteCountsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if len(req.Markets) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "markets must contain at least one entry")
		return
	}
	if len(req.Markets) > maxVoteBatchMarkets {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"markets holds more entries than one request may ask for")
		return
	}

	// Resolved up front so the whole batch is validated before anything is
	// queried, and so the response can be assembled in request order.
	type resolved struct {
		echo     models.MarketKey
		keyHash  string
		outcomes []outcomeCount
		hashes   []string
	}
	entries := make([]resolved, 0, len(req.Markets))
	hashes := make([]string, 0, len(req.Markets))

	for _, entry := range req.Markets {
		key, echo, msg := entry.Market.validate("markets[].market")
		if msg != "" {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
			return
		}
		if len(entry.Outcomes) == 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"markets[].outcomes must contain at least one entry")
			return
		}
		if len(entry.Outcomes) > maxVoteBatchOutcomes {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"markets[].outcomes holds more entries than one request may ask for")
			return
		}

		item := resolved{
			echo:     echo,
			keyHash:  models.MarketKeyHash(key),
			outcomes: make([]outcomeCount, 0, len(entry.Outcomes)),
			hashes:   make([]string, 0, len(entry.Outcomes)),
		}
		for _, o := range entry.Outcomes {
			outcome, msg := o.validate("markets[].outcomes[]")
			if msg != "" {
				writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
				return
			}
			item.outcomes = append(item.outcomes, outcomeCount{Key: outcome})
			item.hashes = append(item.hashes, models.OutcomeKeyHash(outcome))
		}
		entries = append(entries, item)
		hashes = append(hashes, item.keyHash)
	}

	counts, err := h.Votes.Counts(r.Context(), hashes)
	if err != nil {
		internalError(w, "read vote counts", err)
		return
	}

	out := voteCountsResponse{
		Markets:    make([]marketCounts, 0, len(entries)),
		VotedToday: h.votedToday(r),
	}
	for _, entry := range entries {
		// A market nobody has voted in yet is not an error: every requested
		// outcome is answered, and an unknown one answers 0.
		bucket := counts[entry.keyHash]
		for i := range entry.outcomes {
			entry.outcomes[i].Count = bucket[entry.hashes[i]]
		}
		out.Markets = append(out.Markets, marketCounts{Market: entry.echo, Outcomes: entry.outcomes})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// POST /votes
// ---------------------------------------------------------------------------

type castVoteRequest struct {
	Market  marketRequest  `json:"market"`
	Outcome outcomeRequest `json:"outcome"`
	// Outcomes is the market's full outcome list, so the response can carry a
	// count for each — the card renders the whole split, not just the picked
	// side. Optional: omitted, the response answers for the picked outcome alone.
	Outcomes []outcomeRequest `json:"outcomes"`
	// DeviceToken is a token THIS SERVICE issued on a previous anonymous vote.
	// A caller with a bearer token does not need one and it is ignored there.
	// Absent, unreadable or not signed by us, the caller is treated as a voter
	// we have not seen and a fresh token is minted for them.
	DeviceToken string `json:"deviceToken"`
}

type castVoteResponse struct {
	Market   models.MarketKey `json:"market"`
	Outcomes []outcomeCount   `json:"outcomes"`
	// DeviceToken is present only when this request minted one. Store it and
	// send it back on the next vote; lose it, and the next vote is a new voter.
	//
	// It appears HERE and never on the counts response, which is the cacheable
	// anonymous path — mixing a per-caller value into a response served from a
	// shared cache is how one caller's identity reaches another.
	DeviceToken string `json:"deviceToken,omitempty"`
	VotedToday  int64  `json:"votedToday"`
}

// castVote records one player's pick.
//
// Final: there is no re-vote, no change of pick and no withdrawal. A repeat of
// the SAME pick is answered as a success with fresh counts, because a retried
// request after a timeout must not read as a failure — the same tolerance
// POST /rooms already has. A different outcome in the same market is a 409 with
// its own code, so the client can reconcile local state it got wrong instead of
// swallowing the refusal as a logged error.
//
// NOT CHECKED: that the market is still open. "Reject votes on a market that is
// closed, removed or settled" needs a sportsbook feed this service does not
// have — the same gap that stops it re-checking duel eligibility. See
// `docs/votes-api.md` §Not included yet. A card that closes between render and
// click currently records a vote.
func (h *Handlers) castVote(w http.ResponseWriter, r *http.Request) {
	var req castVoteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	key, echo, msg := req.Market.validate("market")
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	picked, msg := req.Outcome.validate("outcome")
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	if len(req.Outcomes) > maxVoteBatchOutcomes {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"outcomes holds more entries than one request may ask for")
		return
	}
	// Answered in the caller's own order, the same way the counts route answers,
	// and with the caller's own spelling of each key — that is what the card
	// looks the count up by. The picked outcome is answered for whether or not it
	// appears in the list, so a client that sends only `outcome` still learns
	// what its vote did.
	pickedHash := models.OutcomeKeyHash(picked)
	answerFor := make([]models.OutcomeKey, 0, len(req.Outcomes)+1)
	pickedListed := false
	for _, o := range req.Outcomes {
		outcome, msg := o.validate("outcomes[]")
		if msg != "" {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
			return
		}
		if models.OutcomeKeyHash(outcome) == pickedHash {
			pickedListed = true
		}
		answerFor = append(answerFor, outcome)
	}
	if !pickedListed {
		answerFor = append(answerFor, picked)
	}

	voter, minted, refusal := h.resolveVoter(r, req.DeviceToken)
	if refusal != nil {
		writeError(w, refusal.status, refusal.code, refusal.message)
		return
	}

	marketKeyHash := models.MarketKeyHash(key)
	vote := models.Vote{
		ID:             uuid.NewString(),
		MarketKeyHash:  marketKeyHash,
		OutcomeKeyHash: models.OutcomeKeyHash(picked),
		Market:         key,
		Outcome:        picked,
		VoterID:        voter.id,
		Subject:        voter.subject,
		IPHash:         voter.ipHash,
		CreatedAt:      h.nowMS(),
	}

	recorded, err := h.Votes.Cast(r.Context(), vote)
	switch {
	case err == nil:
		// Recorded.
	case errors.Is(err, store.ErrAlreadyVoted):
		if recorded.OutcomeKeyHash != vote.OutcomeKeyHash {
			writeError(w, http.StatusConflict, codeAlreadyVoted,
				"you have already voted in this market, and a vote cannot be changed")
			return
		}
		// Same pick as before: a retry, not a second vote. Fall through to the
		// counts, which is what the caller wanted either way.
	default:
		internalError(w, "cast vote", err)
		return
	}

	counts, err := h.Votes.Counts(r.Context(), []string{marketKeyHash})
	if err != nil {
		internalError(w, "read vote counts after vote", err)
		return
	}

	// Returning the counts is required, not a convenience: it is what lets the
	// client stop optimistically incrementing a number it cannot know.
	bucket := counts[marketKeyHash]
	out := castVoteResponse{
		Market:      echo,
		Outcomes:    make([]outcomeCount, 0, len(answerFor)),
		DeviceToken: minted,
		VotedToday:  h.votedToday(r),
	}
	for _, outcome := range answerFor {
		out.Outcomes = append(out.Outcomes, outcomeCount{
			Key:   outcome,
			Count: bucket[models.OutcomeKeyHash(outcome)],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Voter identity
// ---------------------------------------------------------------------------

// voter is who a vote is attributed to, and what the limits are applied against.
type voter struct {
	id      string
	subject string
	ipHash  string
}

// resolveVoter decides whose vote this is.
//
// A bearer token wins outright: the subject is a real identity and "one vote per
// market" means per account for a signed-in player. Without one the caller is
// anonymous, and gets a device identity this service issued — never one they
// supplied, because a client-minted id enforces nothing and can be pointed at
// someone else.
//
// The returned token is non-empty only when this request minted one.
func (h *Handlers) resolveVoter(r *http.Request, deviceToken string) (voter, string, *refusal) {
	if subject := subjectFrom(r); subject != "" {
		return voter{id: models.VoterIDForSubject(subject), subject: subject}, "", nil
	}

	id, ok := h.Devices.DeviceID(deviceToken)
	minted := ""
	if !ok {
		var err error
		minted, id, err = h.Devices.Mint()
		if err != nil {
			log.Printf("api: mint device token: %v", err)
			return voter{}, "", &refusal{http.StatusInternalServerError, codeInternal,
				"something went wrong on our side"}
		}
	}

	ipHash := h.Devices.HashIP(h.clientIP(r))
	if refused := h.checkAnonymousVoteLimit(r, ipHash); refused != nil {
		return voter{}, "", refused
	}
	return voter{id: models.VoterIDForDevice(id), ipHash: ipHash}, minted, nil
}

// checkAnonymousVoteLimit is the only real ceiling on anonymous voting.
//
// A device token bounds nothing on its own: a caller can discard it and be
// issued another, which is exactly what clearing site data does. So the limit is
// per ADDRESS and per UTC day, on the same boundary the daily duel count resets.
//
// Deliberately generous and configurable rather than one-vote-per-address:
// a shared office or mobile-carrier NAT puts many genuine players behind one
// address, and a strict per-address rule would silently disenfranchise all but
// the first of them.
func (h *Handlers) checkAnonymousVoteLimit(r *http.Request, ipHash string) *refusal {
	if h.AnonVotesPerIPPerDay <= 0 || ipHash == "" {
		return nil
	}
	cast, err := h.Votes.CountAnonymousFromIPSince(r.Context(), ipHash, startOfUTCDay(h.nowMS()))
	if err != nil {
		// A limit that cannot be checked is not a reason to refuse a legitimate
		// vote; log it and let it through. Same call as the daily duel limit.
		log.Printf("api: count anonymous votes for rate limit: %v", err)
		return nil
	}
	if cast >= int64(h.AnonVotesPerIPPerDay) {
		return &refusal{http.StatusTooManyRequests, codeVoteRateLimited,
			"too many votes from this connection today; sign in to keep voting"}
	}
	return nil
}

// votedToday is the header counter: site-wide, since the current UTC day began.
//
// A counter that cannot be read is reported as 0 rather than failing the
// request — it is a decoration beside the counts, and the counts are the answer.
func (h *Handlers) votedToday(r *http.Request) int64 {
	n, err := h.Votes.CountSince(r.Context(), startOfUTCDay(h.nowMS()))
	if err != nil {
		log.Printf("api: count votes today: %v", err)
		return 0
	}
	return n
}

// clientIP returns the address the rate limit counts against.
//
// RemoteAddr by default, which is the only address this process can actually
// verify. A forwarded header is read ONLY when TrustedClientIPHeader names one,
// because a header any caller can set is a header any caller can vary to walk
// straight past a per-address limit. Behind a proxy the default collapses every
// player onto one address, so a deployment that has one must set the header —
// see `docs/votes-api.md`.
func (h *Handlers) clientIP(r *http.Request) string {
	if header := h.TrustedClientIPHeader; header != "" {
		if v := r.Header.Get(header); v != "" {
			first, _, _ := strings.Cut(v, ",")
			if first = strings.TrimSpace(first); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
