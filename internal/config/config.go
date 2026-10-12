// Package config loads bnbloc service configuration from the environment. Keeps
// OS/deployment specifics at the composition root, out of business logic.
package config

import (
	"os"
	"strconv"
	"strings"
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

// ParseBool is the one reading of a boolean switch, in the environment or a
// stored setting: 1/true/yes/on and 0/false/no/off (any case); anything else
// — unset included — is fallback.
func ParseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// Bool reads a boolean environment switch (ParseBool).
func Bool(key string, fallback bool) bool { return ParseBool(os.Getenv(key), fallback) }

// List reads a comma-separated environment list: trimmed, empty entries
// dropped, nil when unset.
func List(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
