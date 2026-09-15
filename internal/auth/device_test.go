package auth_test

import (
	"testing"

	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
)

func newDevices(t *testing.T, secret string) *auth.DeviceTokens {
	t.Helper()
	d, err := auth.NewDeviceTokens([]byte(secret))
	if err != nil {
		t.Fatalf("new device tokens: %v", err)
	}
	return d
}

func TestDeviceTokenRoundTrips(t *testing.T) {
	d := newDevices(t, "secret")

	token, id, err := d.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	got, ok := d.DeviceID(token)
	if !ok {
		t.Fatal("a token this service minted did not verify")
	}
	if got != id {
		t.Fatalf("id = %q, want %q", got, id)
	}
}

func TestDeviceTokensAreUnique(t *testing.T) {
	d := newDevices(t, "secret")

	seen := make(map[string]bool, 100)
	for range 100 {
		_, id, err := d.Mint()
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if seen[id] {
			t.Fatalf("minted a duplicate device id %q", id)
		}
		seen[id] = true
	}
}

// The whole point of signing: a caller cannot invent an identity, and so cannot
// point one at somebody else's and lock them out of a market.
func TestDeviceIDRejectsWhatThisServiceDidNotSign(t *testing.T) {
	d := newDevices(t, "secret")
	token, id, _ := d.Mint()

	for name, candidate := range map[string]string{
		"empty":            "",
		"no signature":     id,
		"wrong signature":  id + ".AAAAAAAAAAAAAAAAAAAAAA",
		"a client uuid":    "3f8c1e2a-0b4d-4c6e-9a1f-2b3c4d5e6f70",
		"signature only":   "." + token,
		"another secret's": mintWith(t, "other secret"),
	} {
		if _, ok := d.DeviceID(candidate); ok {
			t.Errorf("%s verified as a device token", name)
		}
	}
}

func mintWith(t *testing.T, secret string) string {
	t.Helper()
	token, _, err := newDevices(t, secret).Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return token
}

// An empty secret still produces a working issuer, and says it will not survive
// a restart.
func TestEmptySecretIsEphemeralAndStillWorks(t *testing.T) {
	d, err := auth.NewDeviceTokens(nil)
	if err != nil {
		t.Fatalf("new device tokens: %v", err)
	}
	if !d.Ephemeral {
		t.Error("an issuer with no configured secret must report itself ephemeral")
	}
	token, _, err := d.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, ok := d.DeviceID(token); !ok {
		t.Error("an ephemeral issuer must still verify its own tokens")
	}
}

// The rate limit compares addresses; it never needs to read them back.
func TestHashIPIsStableAndNotTheAddress(t *testing.T) {
	d := newDevices(t, "secret")

	first := d.HashIP("203.0.113.7")
	if first == "" {
		t.Fatal("an address hashed to nothing")
	}
	if first != d.HashIP("203.0.113.7") {
		t.Error("the same address hashed differently twice")
	}
	if first == d.HashIP("203.0.113.8") {
		t.Error("two addresses hashed the same")
	}
	if first == "203.0.113.7" {
		t.Error("the address was stored verbatim")
	}
	if d.HashIP("") != "" {
		t.Error("an absent address must hash to nothing, so the limit skips it")
	}
}
