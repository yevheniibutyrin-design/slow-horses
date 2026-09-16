package models

// MarketModel is the market half of the selection triple. Mirrors the widget's
// MarketModelType (sport-web-widgets, libs/types/src/markets.ts) field for field:
// the widget sends these as an object and reads them back as one.
type MarketModel struct {
	EventID    string `json:"eventId"    bson:"eventId"`
	MarketType int    `json:"marketType" bson:"marketType"`
	Period     int    `json:"period"     bson:"period"`
	ResultKind int    `json:"resultKind" bson:"resultKind"`
	// SubPeriod is absent for most markets, so it is omitted rather than zeroed —
	// the widget distinguishes "no sub-period" from "sub-period 0" when it matches
	// a market model against a placed bet's outcome id.
	SubPeriod *int `json:"subPeriod,omitempty" bson:"subPeriod,omitempty"`
}

// MarketItemKey names one item within a market — what tells Over 2.5 from Over
// 3.5, and why a market id alone cannot identify the selection.
//
// MarketParameters is legitimately empty for a parameterless market such as
// 1X2, so an empty slice is valid data, not a missing field. It must serialise
// as `[]` and never as `null`: the widget resolves a room's own market by
// comparing JSON.stringify(marketParameters) against an outcome id's values,
// and `null` matches nothing.
type MarketItemKey struct {
	MarketParameters []string `json:"marketParameters" bson:"marketParameters"`
}

// OutcomeKey names one outcome within a market item. Values, like
// MarketItemKey.MarketParameters, is legitimately empty and must serialise as
// `[]` rather than `null` for the same reason.
type OutcomeKey struct {
	Type   int      `json:"type"   bson:"type"`
	Values []string `json:"values" bson:"values"`
}

// SideOffer is one side of a duel as an offer: which outcome, at what price.
// Both sides are known from creation, because the opposite side is the other
// placeable outcome of the same market item.
type SideOffer struct {
	OutcomeID OutcomeKey `json:"outcomeId" bson:"outcomeId"`
	// Odd is the feed's RAW INTEGER price: 182 means 1.82. Kept as an integer
	// server-side, which is what lets the underround guard be exact.
	Odd int `json:"odd" bson:"odd"`
}

// SidePlacement is a side once its bet exists.
//
// LineItemID and DataVersion are the two feed-sourced fields a bet request
// requires and the market feed does not carry; both come from a GetOutcomesV2
// subscription. They are recorded as placed and are never read back to place a
// LATER bet — a stored DataVersion is stale by the time the opposite side is
// taken.
type SidePlacement struct {
	ParticipantID string `json:"participantId" bson:"participantId"`
	// Stake is what was actually placed on this side. Each side's own stake is
	// what the matched, settled and closed presentations render — never the
	// shared Figures.Entry, which is the joiner's by definition.
	Stake       Amount `json:"stake"       bson:"stake"`
	LineItemID  string `json:"lineItemId"  bson:"lineItemId"`
	DataVersion int    `json:"dataVersion" bson:"dataVersion"`
}

// Side is a side whose bet may not exist yet — the opponent's, until they
// confirm their seat.
type Side struct {
	SideOffer `bson:",inline"`
	Placement *SidePlacement `json:"placement,omitempty" bson:"placement,omitempty"`
}

// Figures are the values presented to players:
//
//	payout = round(creatorStake * creatorOdd, 2)
//	entry  = floor(payout / opponentOdd, 2)
//	pot    = creatorStake + entry
//
// The gap between Pot and Payout is the book's existing margin on the two
// prices. It is not a fee, and the room never takes it.
//
// Provisional while the room is unfilled: the joiner's entry is recomputed
// against live prices at acceptance, and this snapshot is refreshed then.
type Figures struct {
	// Payout is what the winner takes.
	Payout Amount `json:"payout" bson:"payout"`
	// Entry is what the joiner puts in, always rounded DOWN so their own return
	// never exceeds Payout.
	Entry Amount `json:"entry" bson:"entry"`
	Pot   Amount `json:"pot"   bson:"pot"`
}

// DuelPayload is the duel strategy's payload, carried by the room.
//
// The selection is the TRIPLE MarketID + MarketItemID + a side's OutcomeID.
// Each part is a STRUCTURED KEY rather than an opaque string — the widget
// composes and reads them as objects. The market and the item are held once at
// payload level, which is what structurally guarantees both sides sit on the
// same line: a market holds many items, so a market id alone cannot tell
// Over 2.5 from Over 3.5.
type DuelPayload struct {
	MarketID     MarketModel   `json:"marketId"     bson:"marketId"`
	MarketItemID MarketItemKey `json:"marketItemId" bson:"marketItemId"`
	// CreatorSide is always placed: a duel room is created from a bet that
	// already exists, because the creator's stake is only known once the host bet
	// slip has placed it.
	CreatorSide Side `json:"creatorSide" bson:"creatorSide"`
	// OpponentSide is offered from creation and placed only on acceptance — the
	// opposite ordering, because a joiner's entry is committed on their behalf at
	// a fixed amount and must never exist without a seat.
	OpponentSide Side    `json:"opponentSide" bson:"opponentSide"`
	Figures      Figures `json:"figures"      bson:"figures"`
	// WinnerParticipantID is the participant whose side settled as a win. Absent
	// while settlement is pending and absent on a void.
	//
	// NOT WRITTEN BY ANY ROUTE. It is set by a settlement worker resolving each
	// participant's BetRef against the sportsbook's settlement — a dependency
	// this service does not have. See `docs/bet-room-api.md` §5.6: until that
	// ships, a filled duel stays filled forever.
	WinnerParticipantID string `json:"winnerParticipantId,omitempty" bson:"winnerParticipantId,omitempty"`
}
