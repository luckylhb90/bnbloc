package config

import (
	"fmt"
	"os"
	"strings"
)

// EnvVar is the single deployment-mode switch. BNBLOC_ENV=production turns
// every dev-permissive fallback (unverified internal identity, dev login
// verifier, in-memory store, ephemeral session key) into a fatal startup error,
// so a production stack can never boot silently in dev mode. Any other value,
// or unset, is development: the fallbacks stay available with loud warnings.
const EnvVar = "BNBLOC_ENV"

// Production reports whether the process runs in production mode
// (BNBLOC_ENV=production, case-insensitive).
func Production() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvVar)), "production")
}

// RequireInProduction returns an error naming every variable in names that is
// unset or blank when the process runs in production mode; nil in development.
// It is the one check each composition root calls for the secrets/config whose
// absence would otherwise select a dev fallback.
func RequireInProduction(names ...string) error {
	if !Production() {
		return nil
	}
	var missing []string
	for _, n := range names {
		if strings.TrimSpace(os.Getenv(n)) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s=production requires %s", EnvVar, strings.Join(missing, ", "))
}

// RefuseInProduction returns an error naming every dev-only switch in names
// that is set (non-blank) when the process runs in production mode; nil in
// development. It is the counterpart of RequireInProduction for switches
// whose presence, not absence, selects a dev path.
func RefuseInProduction(names ...string) error {
	if !Production() {
		return nil
	}
	var set []string
	for _, n := range names {
		if strings.TrimSpace(os.Getenv(n)) != "" {
			set = append(set, n)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s=production refuses the dev-only %s", EnvVar, strings.Join(set, ", "))
}
