// Package auth resolves the caller identity every room route depends on.
//
// The room contract deliberately carries NO user identifier in any request
// payload — not an id, not an email, not an account number — so the bearer token
// is the service's only way to know who is calling. Four rules are unenforceable
// without it: which participant a reader is, that a creator cannot take a second
// seat in their own room, that the open-rooms list excludes the caller's own, and
// that a rematch is joinable only by the original opponent.
//
// The subject resolved here is stored on a participant and never serialised.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrInvalidToken covers every reason a credential was not accepted. The reason
// is deliberately not distinguished to the caller: a 401 says no more than that.
var ErrInvalidToken = errors.New("auth: invalid token")

// Verifier turns a bearer token into a stable subject identifying the caller.
type Verifier interface {
	Subject(token string) (string, error)
	// Insecure reports whether this verifier performs no cryptographic check, so
	// startup can say so loudly rather than letting it pass unnoticed.
	Insecure() bool
}

// HS256Verifier validates a JWT signed with HMAC-SHA256 and returns its `sub`.
//
// Hand-rolled rather than pulled from a library: the whole check is a hash and a
// constant-time compare, and this repository's two-dependency budget is worth
// more than the fifty lines saved.
type HS256Verifier struct{ secret []byte }

// NewHS256Verifier returns a verifier for the given shared secret.
func NewHS256Verifier(secret []byte) *HS256Verifier { return &HS256Verifier{secret: secret} }

func (v *HS256Verifier) Insecure() bool { return false }

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Nbf int64  `json:"nbf"`
}

func (v *HS256Verifier) Subject(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidToken
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidToken
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return "", ErrInvalidToken
	}
	// Pinned to one algorithm on purpose. Trusting the header's `alg` is the
	// classic JWT vulnerability: it lets a caller present alg "none", or swap an
	// RS256 public key in as an HMAC secret.
	if header.Alg != "HS256" {
		return "", ErrInvalidToken
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", ErrInvalidToken
	}
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", ErrInvalidToken
	}

	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		return "", ErrInvalidToken
	}
	if claims.Sub == "" {
		return "", ErrInvalidToken
	}

	now := time.Now().Unix()
	if claims.Exp != 0 && now >= claims.Exp {
		return "", ErrInvalidToken
	}
	if claims.Nbf != 0 && now < claims.Nbf {
		return "", ErrInvalidToken
	}
	return claims.Sub, nil
}

// InsecureVerifier treats the token itself as the subject, verifying nothing.
//
// It exists so the smoke script, the Postman collection and local development
// work without a token issuer: `Authorization: Bearer alice` is a caller named
// alice. Anyone can impersonate anyone, so it is selected only when no signing
// secret is configured, and the server logs a warning at startup.
type InsecureVerifier struct{}

func (InsecureVerifier) Insecure() bool { return true }

func (InsecureVerifier) Subject(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", ErrInvalidToken
	}
	return token, nil
}

// BearerToken extracts the credential from an Authorization header value.
//
// The token travels only in this header. It must never be read from a query
// string or a request body: both are logged and cached in places a header is not.
func BearerToken(authorization string) (string, bool) {
	const prefix = "Bearer "
	if len(authorization) <= len(prefix) || !strings.EqualFold(authorization[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(authorization[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
