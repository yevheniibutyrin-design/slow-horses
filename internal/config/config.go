// Package config loads runtime configuration from the environment.
package config

import (
	"os"
	"strings"
)

// Config holds everything the API needs to boot. Defaults describe how the app
// is meant to run: inside docker compose, next to a "mongo" service.
type Config struct {
	Port          string
	MongoURI      string
	MongoDB       string
	PublicBaseURL string
}

// Load reads the environment, falling back to the compose-shaped defaults.
func Load() Config {
	return Config{
		Port:          env("PORT", "5000"),
		MongoURI:      env("MONGO_URI", "mongodb://mongo:27017"),
		MongoDB:       env("MONGO_DB", "slowhorses"),
		PublicBaseURL: strings.TrimSuffix(env("PUBLIC_BASE_URL", "http://localhost:5000"), "/"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
