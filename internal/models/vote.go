package models

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
)

// A vote is one player's free pick of one OUTCOME inside one MARKET, cast
// without money and counted in aggregate. It shares nothing with a room: the
// widget shows the crowd split next to the odds-implied split, and the player
// may then back the same outcome in the betslip as an ordinary single bet.
//
// The identity of a market and of an outcome are the whole correctness story
// here, which is why they are structured values with a derived canonical form
// rather than a string the client hands over — see CanonicalMarketKey.

// MarketKey identifies the market a vote is cast in.
//
// It mirrors the widget's TMarketModel MINUS `layout`. Layout is presentational,
// and keying on it would fragment one market's votes across the layouts it is
// rendered in — two players picking the same outcome would land in different
// buckets because one of them saw a different card. The request shape accepts
// `layout` and drops it; see the note on marketRequest.
type MarketKey struct {
	EventID    string `json:"eventId"    bson:"eventId"`
	MarketType int    `json:"marketType" bson:"marketType"`
	Period     int    `json:"period"     bson:"period"`
	ResultKind int    `json:"resultKind" bson:"resultKind"`
	// SubPeriod is a POINTER because absent and null are the same market and
	// neither is the market with subPeriod 0. A plain int would collapse all
	// three into one bucket.
	SubPeriod *int `json:"subPeriod,omitempty" bson:"subPeriod,omitempty"`
}

// OutcomeKey identifies an outcome within its market. It is scoped to the
// market: the same (type, values) pair in two markets is two different vote
// buckets.
type OutcomeKey struct {
	Type   int      `json:"type"   bson:"type"`
	Values []string `json:"values" bson:"values"`
}

// CanonicalMarketKey renders a market as a single string that depends on the
// FIELD VALUES and nothing else.
//
// This exists because the client indexes its own store by JSON.stringify of the
// key object, whose output depends on the runtime property order of whatever the
// feed handed over — not on any type declaration. That order is load-bearing on
// the client and must not become load-bearing here: an opaque string on the wire
// would let a reorder, an added optional field, or a serializer change on either
// side split one market's votes into two buckets with no error anywhere.
//
// So the wire carries structured fields, the server derives this, and the stored
// identity is the derivation. Two properties matter:
//
//   - Fixed field order, written out literally below rather than ranged over.
//   - Injective: every string field is quoted, so a separator appearing inside a
//     value cannot forge the boundary between two fields.
//
// An absent SubPeriod and an explicit null produce the same string, which is the
// requirement; subPeriod 0 produces a different one, which is also the
// requirement.
func CanonicalMarketKey(m MarketKey) string {
	sub := "null"
	if m.SubPeriod != nil {
		sub = strconv.Itoa(*m.SubPeriod)
	}
	return strings.Join([]string{
		strconv.Quote(m.EventID),
		strconv.Itoa(m.MarketType),
		strconv.Itoa(m.Period),
		strconv.Itoa(m.ResultKind),
		sub,
	}, "|")
}

// CanonicalOutcomeKey renders an outcome the same way, with `values` SORTED.
//
// Sorting is what makes ["1","2"] and ["2","1"] one bucket rather than two. It
// works on a copy: the caller's slice is echoed back to the client in the order
// it arrived, because the client looks the response up by stringifying the key
// object it sent. Normalising the order in the RESPONSE would miss every lookup
// and render zeros on a card that has votes.
func CanonicalOutcomeKey(o OutcomeKey) string {
	values := slices.Clone(o.Values)
	slices.Sort(values)
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return strconv.Itoa(o.Type) + "|" + strings.Join(quoted, ",")
}

// hashKey compresses a canonical string to a fixed-width one for storage.
//
// Fixed width is the point. The unique index that enforces one vote per market
// per voter is compound over this field, and an index key has a length limit an
// unbounded event id could push it past. It also keeps the counts aggregation a
// single $match plus $group over two scalar fields — the alternative, indexing
// the structured tuple, goes multikey because `values` is an array.
//
// Not a security boundary: the canonical form is stored alongside, and nothing
// depends on the hash being hard to invert.
func hashKey(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// MarketKeyHash is the stored identity of a market.
func MarketKeyHash(m MarketKey) string { return hashKey(CanonicalMarketKey(m)) }

// OutcomeKeyHash is the stored identity of an outcome within its market.
func OutcomeKeyHash(o OutcomeKey) string { return hashKey(CanonicalOutcomeKey(o)) }

// Vote is a document in the "votes" collection.
//
// It is never serialised to a client: votes leave this service only as aggregate
// counts. The fields that identify a voter would be a privacy leak in a response
// and there is no route that returns one, so none of them carry json tags.
type Vote struct {
	ID string `bson:"_id"`

	// MarketKeyHash and OutcomeKeyHash are the derived identities every query
	// runs against. The structured forms below are stored too, so a vote can be
	// read back and explained without reversing a hash.
	MarketKeyHash  string `bson:"marketKey"`
	OutcomeKeyHash string `bson:"outcomeKey"`

	Market  MarketKey  `bson:"market"`
	Outcome OutcomeKey `bson:"outcome"`

	// VoterID is what "one vote per market per identity" is enforced against. It
	// is namespaced — "user:<subject>" or "anon:<deviceId>" — so a device id can
	// never collide with an account subject that happens to read the same.
	VoterID string `bson:"voterId"`
	// Subject is the authenticated caller, empty for an anonymous vote. Stored
	// separately from VoterID so an operator can tell the two populations apart.
	Subject string `bson:"subject,omitempty"`
	// IPHash is an HMAC of the caller's address, kept only for the anonymous
	// rate limit. The raw address is never stored: the limit needs to compare
	// addresses, not read them.
	IPHash string `bson:"ipHash,omitempty"`

	CreatedAt int64 `bson:"createdAt"`
}

// VoterIDForSubject namespaces an authenticated caller.
func VoterIDForSubject(subject string) string { return "user:" + subject }

// VoterIDForDevice namespaces an anonymous device.
func VoterIDForDevice(deviceID string) string { return "anon:" + deviceID }
