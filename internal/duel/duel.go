// Package duel holds the duel strategy's arithmetic: the entry derivation both
// players see, and the price-pair guard that decides whether a pair can be
// duelled at all.
//
// Everything here is a pure function of its inputs, and nothing here touches a
// float. See `docs/bet-room-api.md` §3 for why both of those are load-bearing.
package duel

import (
	"errors"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// MinRawOdd is 1.00 in the feed's raw integer form. A price below it would pay
// out less than the stake, which is not a real sportsbook price — and the
// underround guard cannot be trusted on one, so such a pair is refused.
const MinRawOdd = 100

var (
	// ErrUnpriced is returned when a side has no usable price yet, which is what
	// makes "the pot, the winner's take and the opponent's entry are all shown as
	// unset" representable without putting unset figures into stored state.
	ErrUnpriced = errors.New("duel: a side has no usable price")
	// ErrUnderround is returned for a price pair whose implied probabilities sum
	// to less than one.
	ErrUnderround = errors.New("duel: the two prices form an underround")
)

// IsUnderround reports whether a price pair would make the pot smaller than the
// winner's take.
//
// The test is exact in the raw-odd integer domain. Requiring 1/a + 1/b >= 1
// rearranges to
//
//	100 * (a + b) >= a * b
//
// which involves no division, no epsilon and no floating point at all, so the
// guard cannot introduce the class of error it exists to prevent. Equality is
// permitted: a perfectly fair pair makes the pot exactly equal the take, which
// displays consistently.
//
// It FAILS CLOSED. A price the integer test cannot evaluate is reported as an
// underround rather than waved through — the frontend shipped the opposite bug
// once (an unevaluable odd made the whole conjunction false and returned the
// market *eligible*), and this is the same guard on the other side of the wire.
func IsUnderround(aRawOdd, bRawOdd int) bool {
	if aRawOdd < MinRawOdd || bRawOdd < MinRawOdd {
		return true
	}
	return 100*(int64(aRawOdd)+int64(bRawOdd)) < int64(aRawOdd)*int64(bRawOdd)
}

// DeriveFigures computes the figures both players are shown, from the creator's
// stake and the two raw prices.
//
//	payout = round(creatorStake * creatorOdd, 2)
//	entry  = floor(payout / opponentOdd, 2)
//	pot    = creatorStake + entry
//
// The ORDER IS LOAD-BEARING: the entry is floored from the ROUNDED payout, not
// from the exact product. 1.01 at 1.24 against 2.05 has an exact product of
// 1.2524 — flooring that gives an entry of 0.61, while flooring the rounded 1.25
// gives 0.60. Only the latter matches the stated invariant that the opponent
// wins exactly the payout the creator was shown. Pinned by test.
//
// The entry is floored rather than rounded so the opponent's own return never
// exceeds the stated winner's take.
func DeriveFigures(creatorStake models.Amount, creatorRawOdd, opponentRawOdd int) (models.Figures, error) {
	if creatorStake <= 0 {
		return models.Figures{}, ErrUnpriced
	}
	if creatorRawOdd < MinRawOdd || opponentRawOdd < MinRawOdd {
		return models.Figures{}, ErrUnpriced
	}
	if IsUnderround(creatorRawOdd, opponentRawOdd) {
		return models.Figures{}, ErrUnderround
	}

	// Minor units throughout. A raw odd is the price in hundredths, so
	// stake * odd / 100 is the return in minor units; round half-up on the way.
	product := int64(creatorStake) * int64(creatorRawOdd)
	payout := (product + 50) / 100

	// floor(payout / (odd/100)) == floor(payout * 100 / odd), and Go's integer
	// division truncates, which is floor for the positive values involved here.
	entry := payout * 100 / int64(opponentRawOdd)

	return models.Figures{
		Payout: models.Amount(payout),
		Entry:  models.Amount(entry),
		Pot:    creatorStake + models.Amount(entry),
	}, nil
}
