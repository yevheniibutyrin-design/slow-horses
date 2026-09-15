package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// A device token is the weaker identity an ANONYMOUS voter gets, so the free
// vote does not require signing in.
//
// It exists because the client's own identifier cannot be one. The widget today
// mints crypto.randomUUID() into localStorage and sends it as `userId`; a
// caller-minted id is free to mint, so keying "one vote per market per identity"
// on it enforces nothing. Worse, a client-supplied id can be SOMEONE ELSE'S:
// vote with a stolen id and the real owner is locked out of that market.
//
// So the id is issued here, unguessable, and carried inside a token this service
// signs. Forging one is not possible without the secret; stealing a whole token
// still is, but that needs the victim's storage, which is where their vote
// already lives.
//
// What this deliberately does NOT claim: a device token bounds nothing on its
// own, because a caller can always discard it and be issued a fresh one. It
// keeps honest clients honest and keeps anonymous voters distinguishable from
// each other. The actual ceiling on anonymous voting is the per-address rate
// limit — see the vote handler.

// deviceIDBytes is the random part of a token: 128 bits, so ids cannot be
// enumerated or guessed into.
const deviceIDBytes = 16

// deviceMACBytes truncates the HMAC. 128 bits is far past what a forgery attempt
// could search, and it keeps the token short enough to sit in localStorage
// without comment.
const deviceMACBytes = 16

// DeviceTokens issues and verifies anonymous voter tokens.
type DeviceTokens struct {
	secret []byte
	// Ephemeral reports that the secret was generated for this process and does
	// not survive a restart, so startup can say so rather than letting every
	// issued token silently stop verifying on the next deploy.
	Ephemeral bool
}

// NewDeviceTokens returns an issuer for the given secret. An empty secret gets a
// random one, valid only for the life of this process.
func NewDeviceTokens(secret []byte) (*DeviceTokens, error) {
	if len(secret) > 0 {
		return &DeviceTokens{secret: secret}, nil
	}
	ephemeral := make([]byte, 32)
	if _, err := rand.Read(ephemeral); err != nil {
		return nil, err
	}
	return &DeviceTokens{secret: ephemeral, Ephemeral: true}, nil
}

// Mint issues a fresh device token and returns it along with the id inside it.
func (d *DeviceTokens) Mint() (token, id string, err error) {
	raw := make([]byte, deviceIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	id = base64.RawURLEncoding.EncodeToString(raw)
	return id + "." + d.sign(id), id, nil
}

// DeviceID returns the id carried by a token, or false when the token is
// missing, malformed or not signed by this service.
//
// A token that does not verify reads as NO token rather than as an error: the
// caller is simply an anonymous voter this service has not seen, and is issued a
// fresh one. Refusing the vote instead would strand every player whose token
// predates a secret rotation.
func (d *DeviceTokens) DeviceID(token string) (string, bool) {
	token = strings.TrimSpace(token)
	id, mac, found := strings.Cut(token, ".")
	if !found || id == "" || mac == "" {
		return "", false
	}
	if _, err := base64.RawURLEncoding.DecodeString(id); err != nil {
		return "", false
	}
	// Constant time, so a token cannot be recovered a byte at a time from how
	// long the comparison takes.
	if !hmac.Equal([]byte(mac), []byte(d.sign(id))) {
		return "", false
	}
	return id, true
}

func (d *DeviceTokens) sign(id string) string {
	mac := hmac.New(sha256.New, d.secret)
	mac.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:deviceMACBytes])
}

// HashIP returns a stable, non-reversible handle for a caller's address.
//
// The rate limit needs to COMPARE addresses, not read them, so the address
// itself is never stored. Keyed with the same secret, so the stored values are
// not a rainbow table away from the addresses that produced them.
func (d *DeviceTokens) HashIP(ip string) string {
	if ip == "" {
		return ""
	}
	mac := hmac.New(sha256.New, d.secret)
	mac.Write([]byte("ip:" + ip))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:deviceMACBytes])
}
