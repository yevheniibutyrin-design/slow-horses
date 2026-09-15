package models_test

import (
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

func ptr(v int) *int { return &v }

// The canonical form is the stored identity of a market, so every pair below is
// a statement about which votes land in one bucket and which do not.
func TestCanonicalMarketKey(t *testing.T) {
	base := models.MarketKey{EventID: "evt-1", MarketType: 1, Period: 0, ResultKind: 2}

	withNull := base
	withNull.SubPeriod = nil
	if models.CanonicalMarketKey(base) != models.CanonicalMarketKey(withNull) {
		t.Error("an absent subPeriod and an explicit null must be the same market")
	}

	withZero := base
	withZero.SubPeriod = ptr(0)
	if models.CanonicalMarketKey(base) == models.CanonicalMarketKey(withZero) {
		t.Error("subPeriod 0 must not collapse into subPeriod absent")
	}

	for name, changed := range map[string]models.MarketKey{
		"eventId":    {EventID: "evt-2", MarketType: 1, Period: 0, ResultKind: 2},
		"marketType": {EventID: "evt-1", MarketType: 2, Period: 0, ResultKind: 2},
		"period":     {EventID: "evt-1", MarketType: 1, Period: 1, ResultKind: 2},
		"resultKind": {EventID: "evt-1", MarketType: 1, Period: 0, ResultKind: 3},
	} {
		if models.CanonicalMarketKey(base) == models.CanonicalMarketKey(changed) {
			t.Errorf("a different %s must be a different market", name)
		}
	}
}

// Injectivity: a separator inside a value must not be able to forge the boundary
// between two fields, or two different markets share one bucket.
func TestCanonicalMarketKeyIsNotForgeableThroughAnEventID(t *testing.T) {
	a := models.MarketKey{EventID: `evt|1`, MarketType: 1, Period: 0, ResultKind: 2}
	b := models.MarketKey{EventID: `evt`, MarketType: 1, Period: 1, ResultKind: 2}

	if models.CanonicalMarketKey(a) == models.CanonicalMarketKey(b) {
		t.Fatal("an event id containing the separator collided with another market")
	}
}

// Sorting is what makes a reordered values array the same outcome. It must work
// on a copy: the caller's slice is echoed back to the client in the order it
// arrived, and reordering it there would miss every client-side lookup.
func TestCanonicalOutcomeKeySortsValuesWithoutMutatingTheInput(t *testing.T) {
	values := []string{"zebra", "alpha"}
	key := models.OutcomeKey{Type: 1, Values: values}

	got := models.CanonicalOutcomeKey(key)
	if want := models.CanonicalOutcomeKey(models.OutcomeKey{Type: 1, Values: []string{"alpha", "zebra"}}); got != want {
		t.Errorf("a reordered values array must be the same outcome: %q != %q", got, want)
	}
	if values[0] != "zebra" || values[1] != "alpha" {
		t.Fatalf("the caller's slice was reordered in place: %v", values)
	}
}

func TestCanonicalOutcomeKeyDistinguishesTypeAndValues(t *testing.T) {
	base := models.OutcomeKey{Type: 1, Values: []string{"home"}}

	if models.CanonicalOutcomeKey(base) == models.CanonicalOutcomeKey(models.OutcomeKey{Type: 2, Values: []string{"home"}}) {
		t.Error("a different type must be a different outcome")
	}
	if models.CanonicalOutcomeKey(base) == models.CanonicalOutcomeKey(models.OutcomeKey{Type: 1, Values: []string{"away"}}) {
		t.Error("a different value must be a different outcome")
	}
	if models.CanonicalOutcomeKey(base) == models.CanonicalOutcomeKey(models.OutcomeKey{Type: 1, Values: []string{"home", "away"}}) {
		t.Error("an extra value must be a different outcome")
	}
}

// The hash is what the unique index is compound over, so equal canonical forms
// must hash equal and differing ones must not.
func TestKeyHashesFollowTheCanonicalForm(t *testing.T) {
	a := models.MarketKey{EventID: "evt-1", MarketType: 1, Period: 0, ResultKind: 2}
	b := a
	b.SubPeriod = ptr(1)

	if models.MarketKeyHash(a) != models.MarketKeyHash(a) {
		t.Fatal("the market key hash is not stable")
	}
	if models.MarketKeyHash(a) == models.MarketKeyHash(b) {
		t.Fatal("two different markets hashed the same")
	}
	if models.OutcomeKeyHash(models.OutcomeKey{Type: 1, Values: []string{"a", "b"}}) !=
		models.OutcomeKeyHash(models.OutcomeKey{Type: 1, Values: []string{"b", "a"}}) {
		t.Fatal("a reordered values array hashed differently")
	}
}

// The namespaces stop a device id colliding with an account subject that happens
// to read the same.
func TestVoterIDsAreNamespaced(t *testing.T) {
	if models.VoterIDForSubject("abc") == models.VoterIDForDevice("abc") {
		t.Fatal("a device id and an account subject collided")
	}
}
