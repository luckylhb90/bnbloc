// Package config loads bnbloc service configuration from the environment. Keeps
// OS/deployment specifics at the composition root, out of business logic.
package config

import (
	"os"
	"strconv"
)

// Service holds the common configuration shared by every bnbloc service.
type Service struct {
	// ListenAddr is the host:port the service binds.
	ListenAddr string
	// RoomBaseURL is the bnbloc-room base URL used for membership checks.
	RoomBaseURL string
}

// FromEnv reads Service configuration, applying defaults. envPrefix scopes the
// variables, e.g. "BNBLOC_ACTIVITY".
func FromEnv(envPrefix, defaultAddr string) Service {
	return Service{
		ListenAddr:  getenv(envPrefix+"_LISTEN_ADDR", defaultAddr),
		RoomBaseURL: getenv("BNBLOC_ROOM_BASE_URL", ""),
	}
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// FromEnvRaw returns the raw value of an environment variable, or "" if unset.
func FromEnvRaw(key string) string {
	return os.Getenv(key)
}

// LookupEnv reports an environment variable's value and whether it was set,
// so callers can distinguish "unset" (use a default) from "set to empty"
// (an intentional override, e.g. disabling a host rewrite).
func LookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

// GetInt reads an integer environment variable with a fallback.
func GetInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
