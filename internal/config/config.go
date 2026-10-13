// Package config loads bnbloc service configuration from the environment. Keeps
// OS/deployment specifics at the composition root, out of business logic.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

// Duration reads a positive Go duration ("2h", "90m") from the environment;
// unset, unparsable or not positive is fallback.
func Duration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

// ParseBool is the one reading of a boolean switch, in the environment or a
// stored setting: 1/t/true/yes/on and 0/f/false/no/off (any case); anything
// else — unset included — is fallback.
func ParseBool(raw string, fallback bool) bool {
	if v, ok := boolWord(raw); ok {
		return v
	}
	return fallback
}

// boolWord is the switch vocabulary of ParseBool (a superset of
// strconv.ParseBool's, so no value that parsed there is refused here).
func boolWord(raw string) (v, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "yes", "on":
		return true, true
	case "0", "f", "false", "no", "off":
		return false, true
	}
	return false, false
}

// Bool reads a boolean environment switch (ParseBool).
func Bool(key string, fallback bool) bool { return ParseBool(os.Getenv(key), fallback) }

// StrictBool reads a boolean environment switch whose malformed value must
// refuse startup: blank is false, a ParseBool word its value, anything else
// an error naming the variable.
func StrictBool(key string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return false, nil
	}
	v, ok := boolWord(raw)
	if !ok {
		return false, fmt.Errorf("%s=%q: want 1/true/yes/on or 0/false/no/off", key, raw)
	}
	return v, nil
}

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
