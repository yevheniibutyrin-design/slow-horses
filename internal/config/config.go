// Package config loads runtime configuration from the environment.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// Config holds everything the API needs to boot. Defaults describe how the app
// is meant to run: inside docker compose, next to a "mongo" service.
type Config struct {
	Port     string
	MongoURI string
	MongoDB  string

	// PublicBaseURL is this service's own base. It is NOT the base for an invite
	// link — see HostEventURLTemplate.
	PublicBaseURL string

	// AuthJWTSecret signs the bearer tokens this service accepts. When empty the
	// server runs with auth.InsecureVerifier, which treats the token itself as
	// the caller's identity so local development and the smoke script work
	// without a token issuer. Startup logs a warning in that mode.
	AuthJWTSecret []byte

	// CORSAllowedOrigins lists the browser origins allowed to read responses. An
	// origin not on it gets no CORS headers back, so a browser refuses the
	// response. The single entry "*" allows every origin, which is the MVP
	// default; narrow it to real origins before this serves anything private.
	CORSAllowedOrigins []string

	// HostEventURLTemplate builds an invite link. The link points at the HOST's
	// event page, not at this service: the widget reads `betRoomId` and
	// `betRoomInvite` off the query string once the host has landed the joiner on
	// the event. `{eventId}` is substituted; the two parameters are appended.
	//
	// This is why the boilerplate's roomUrl 404'd in a browser — that link was
	// never this service's to serve.
	HostEventURLTemplate string

	// InviteWindow is the standard invite window in milliseconds, measured from a
	// room's creation. A room's expiry is clamped to it.
	InviteWindowMS int64

	// SeatHoldTTL is how long a reserved seat is held, in milliseconds.
	//
	// UNSIZED. It has to cover a GetOutcomesV2 subscription load PLUS a placement
	// round-trip, not a placement alone, and that has never been measured against
	// real infrastructure. The default below is a placeholder, not a sized value.
	SeatHoldTTLMS int64

	// VoteDeviceSecret signs the device tokens anonymous voters are issued, and
	// keys the hash of the addresses the anonymous rate limit counts against. It
	// falls back to AuthJWTSecret, and then to a random per-process secret that
	// does not survive a restart — startup says so when it comes to that.
	VoteDeviceSecret []byte

	// AnonVotesPerIPPerDay caps anonymous votes from one address per UTC day.
	// A device token is free to discard and re-mint, so this — not the
	// one-vote-per-device rule — is the actual ceiling on anonymous voting.
	// Zero or less disables it.
	AnonVotesPerIPPerDay int

	// TrustedClientIPHeader names the forwarded-address header to believe. Empty
	// (the default) believes none: a header any caller can set is a header any
	// caller can vary to walk past a per-address limit. Behind a proxy this MUST
	// be set, or every player collapses onto the proxy's own address.
	TrustedClientIPHeader string

	// Duel limits. The room service is the only component that knows how many
	// duels a caller opened today, so the count comes from here; the two ceilings
	// may be administered elsewhere and are configuration for now.
	DuelPerDuelMax models.Amount
	DuelDailyLimit int
	DuelCurrency   string
}

// Load reads the environment, falling back to the compose-shaped defaults.
func Load() Config {
	return Config{
		Port:          env("PORT", "5000"),
		MongoURI:      env("MONGO_URI", "mongodb://mongo:27017"),
		MongoDB:       env("MONGO_DB", "slowhorses"),
		PublicBaseURL: strings.TrimSuffix(env("PUBLIC_BASE_URL", "http://localhost:5000"), "/"),

		AuthJWTSecret:      []byte(os.Getenv("AUTH_JWT_SECRET")),
		CORSAllowedOrigins: splitList(env("CORS_ALLOWED_ORIGINS", "*")),

		HostEventURLTemplate: env("HOST_EVENT_URL_TEMPLATE", "http://localhost:3000/event/{eventId}"),

		InviteWindowMS: envInt64("INVITE_WINDOW_MS", 30*60*1000),
		SeatHoldTTLMS:  envInt64("SEAT_HOLD_TTL_MS", 90*1000),

		VoteDeviceSecret:      []byte(os.Getenv("VOTE_DEVICE_SECRET")),
		AnonVotesPerIPPerDay:  int(envInt64("VOTE_ANON_IP_DAILY_LIMIT", 50)),
		TrustedClientIPHeader: os.Getenv("TRUSTED_CLIENT_IP_HEADER"),

		DuelPerDuelMax: envAmount("DUEL_PER_DUEL_MAX", 50000), // 500.00
		DuelDailyLimit: int(envInt64("DUEL_DAILY_LIMIT", 10)),
		DuelCurrency:   env("DUEL_CURRENCY", "EUR"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		log.Printf("config: %s=%q is not a positive integer, using %d", key, raw, fallback)
		return fallback
	}
	return v
}

func envAmount(key string, fallback models.Amount) models.Amount {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := models.ParseAmount(raw)
	if err != nil || v <= 0 {
		log.Printf("config: %s=%q is not a positive amount, using %s", key, raw, fallback)
		return fallback
	}
	return v
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
