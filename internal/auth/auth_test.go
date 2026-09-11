package auth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yevheniibutyrin-design/slow-horses/internal/auth"
)

var secret = []byte("test-secret")

func sign(t *testing.T, header, claims map[string]any, key []byte) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	body := enc(header) + "." + enc(claims)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestHS256VerifierAcceptsAValidToken(t *testing.T) {
	token := sign(t,
		map[string]any{"alg": "HS256", "typ": "JWT"},
		map[string]any{"sub": "user-1", "exp": time.Now().Add(time.Hour).Unix()},
		secret)

	got, err := auth.NewHS256Verifier(secret).Subject(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "user-1" {
		t.Errorf("subject = %q, want user-1", got)
	}
}

func TestHS256VerifierRejects(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	cases := []struct {
		name    string
		token   string
		comment string
	}{
		{"empty", "", "no credential at all"},
		{"not a jwt", "opaque-token", "wrong shape"},
		{"wrong secret", sign(t,
			map[string]any{"alg": "HS256"},
			map[string]any{"sub": "user-1", "exp": future},
			[]byte("other-secret")), "signature does not verify"},
		{"alg none", sign(t,
			map[string]any{"alg": "none"},
			map[string]any{"sub": "user-1", "exp": future},
			secret), "the classic JWT bypass; alg is pinned, not trusted"},
		{"alg HS512", sign(t,
			map[string]any{"alg": "HS512"},
			map[string]any{"sub": "user-1", "exp": future},
			secret), "only HS256 is accepted"},
		{"expired", sign(t,
			map[string]any{"alg": "HS256"},
			map[string]any{"sub": "user-1", "exp": past},
			secret), "exp has passed"},
		{"not yet valid", sign(t,
			map[string]any{"alg": "HS256"},
			map[string]any{"sub": "user-1", "nbf": future},
			secret), "nbf is in the future"},
		{"no subject", sign(t,
			map[string]any{"alg": "HS256"},
			map[string]any{"exp": future},
			secret), "a token with no sub identifies nobody"},
	}

	v := auth.NewHS256Verifier(secret)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := v.Subject(c.token); err == nil {
				t.Errorf("accepted a token that should be rejected (%s)", c.comment)
			}
		})
	}
}

// A tampered payload must fail even though the header and signature are intact.
func TestHS256VerifierDetectsTampering(t *testing.T) {
	token := sign(t,
		map[string]any{"alg": "HS256"},
		map[string]any{"sub": "user-1", "exp": time.Now().Add(time.Hour).Unix()},
		secret)

	forged, err := json.Marshal(map[string]any{"sub": "user-2", "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	// Swap the claims segment, keeping the header and the signature intact.
	header, rest, _ := strings.Cut(token, ".")
	_, signature, _ := strings.Cut(rest, ".")
	tampered := header + "." + base64.RawURLEncoding.EncodeToString(forged) + "." + signature

	if _, err := auth.NewHS256Verifier(secret).Subject(tampered); err == nil {
		t.Error("accepted a token whose claims were swapped under a valid signature")
	}
}

func TestInsecureVerifier(t *testing.T) {
	v := auth.InsecureVerifier{}
	if !v.Insecure() {
		t.Error("InsecureVerifier must report itself as insecure so startup can warn")
	}
	got, err := v.Subject("alice")
	if err != nil || got != "alice" {
		t.Errorf("Subject(alice) = %q, %v; want alice, nil", got, err)
	}
	if _, err := v.Subject("   "); err == nil {
		t.Error("an empty token identifies nobody, even in insecure mode")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"BEARER abc", "abc", true},
		{"Bearer   abc  ", "abc", true},
		{"Basic abc", "", false},
		{"abc", "", false},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			got, ok := auth.BearerToken(c.header)
			if got != c.want || ok != c.ok {
				t.Errorf("BearerToken(%q) = %q, %v; want %q, %v", c.header, got, ok, c.want, c.ok)
			}
		})
	}
}
