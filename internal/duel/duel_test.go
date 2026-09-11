package duel_test

import (
	"errors"
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/duel"
	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

func amount(t *testing.T, s string) models.Amount {
	t.Helper()
	a, err := models.ParseAmount(s)
	if err != nil {
		t.Fatalf("ParseAmount(%q): %v", s, err)
	}
	return a
}

func TestDeriveFiguresWorkedExample(t *testing.T) {
	// design.md's own example: 10.00 at 1.82 against 2.05.
	got, err := duel.DeriveFigures(amount(t, "10.00"), 182, 205)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Payout.String() != "18.20" {
		t.Errorf("payout = %s, want 18.20", got.Payout)
	}
	if got.Entry.String() != "8.87" {
		t.Errorf("entry = %s, want 8.87", got.Entry)
	}
	if got.Pot.String() != "18.87" {
		t.Errorf("pot = %s, want 18.87", got.Pot)
	}
}

// The entry must be floored from the ROUNDED payout, not the exact product.
// Flooring 1.2524 gives 0.61; flooring the rounded 1.25 gives 0.60.
func TestDeriveFiguresFloorsFromRoundedPayout(t *testing.T) {
	got, err := duel.DeriveFigures(amount(t, "1.01"), 124, 205)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Payout.String() != "1.25" {
		t.Fatalf("payout = %s, want 1.25", got.Payout)
	}
	if got.Entry.String() != "0.60" {
		t.Errorf("entry = %s, want 0.60 (flooring the exact product would give 0.61)", got.Entry)
	}
}

// The opponent's own return must never exceed the stated winner's take. That is
// the property flooring exists for, so assert the property rather than the digits.
func TestOpponentReturnNeverExceedsPayout(t *testing.T) {
	stakes := []string{"0.50", "1.01", "8.87", "10.00", "33.33", "250.50", "1000.00"}
	odds := []int{100, 101, 124, 150, 182, 200, 205, 333, 999, 5000}

	for _, s := range stakes {
		for _, a := range odds {
			for _, b := range odds {
				figures, err := duel.DeriveFigures(amount(t, s), a, b)
				if err != nil {
					continue // refused pairs are not claims about returns
				}
				// The opponent stakes `entry` at price b; their return is
				// floor-consistent with entry * b / 100.
				opponentReturn := int64(figures.Entry) * int64(b) / 100
				if opponentReturn > int64(figures.Payout) {
					t.Fatalf("stake %s at %d vs %d: opponent return %s exceeds payout %s",
						s, a, b, models.Amount(opponentReturn), figures.Payout)
				}
				if figures.Pot < figures.Payout {
					t.Fatalf("stake %s at %d vs %d: pot %s below payout %s",
						s, a, b, figures.Pot, figures.Payout)
				}
			}
		}
	}
}

func TestIsUnderround(t *testing.T) {
	cases := []struct {
		name    string
		a, b    int
		want    bool
		comment string
	}{
		{"normal pair", 182, 205, false, "1/1.82 + 1/2.05 > 1"},
		{"exactly fair", 200, 200, false, "equality is permitted"},
		{"underround", 333, 143, true, "design.md's verified example"},
		{"odd below 1.00 fails closed", 99, 205, true, "cannot be evaluated, so refused"},
		{"zero fails closed", 0, 205, true, "cannot be evaluated, so refused"},
		{"negative fails closed", -182, 205, true, "cannot be evaluated, so refused"},
		{"both below 1.00 fail closed", 50, 50, true, "cannot be evaluated, so refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := duel.IsUnderround(c.a, c.b); got != c.want {
				t.Errorf("IsUnderround(%d, %d) = %v, want %v (%s)", c.a, c.b, got, c.want, c.comment)
			}
		})
	}
}

func TestDeriveFiguresRefusals(t *testing.T) {
	cases := []struct {
		name    string
		stake   string
		a, b    int
		wantErr error
	}{
		{"underround pair", "10.00", 333, 143, duel.ErrUnderround},
		{"no stake", "0.00", 182, 205, duel.ErrUnpriced},
		{"creator side unpriced", "10.00", 0, 205, duel.ErrUnpriced},
		{"opponent side unpriced", "10.00", 182, 0, duel.ErrUnpriced},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := duel.DeriveFigures(amount(t, c.stake), c.a, c.b)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want %v", err, c.wantErr)
			}
		})
	}
}

// Exactly fair prices make the pot equal the winner's take.
func TestExactlyFairPairPotEqualsPayout(t *testing.T) {
	got, err := duel.DeriveFigures(amount(t, "10.00"), 200, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Pot != got.Payout {
		t.Errorf("pot %s != payout %s for a perfectly fair pair", got.Pot, got.Payout)
	}
}
