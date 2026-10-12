package main

import (
	"testing"
)

// Production without OIDC never falls back to the dev verifier: direct login
// refuses every identity (SSO through an installed auth-provider is the way
// in). Development keeps the dev verifier.
func TestResolveVerifierNeverDevInProduction(t *testing.T) {
	t.Setenv("BNBLOC_OIDC_ISSUER_URL", "")
	t.Setenv("BNBLOC_OIDC_CLIENT_ID", "")
	t.Setenv("BNBLOC_ENV", "production")
	if _, err := resolveVerifier().Verify("admin@corp.example"); err == nil {
		t.Fatal("production accepted a dev login")
	}
	t.Setenv("BNBLOC_ENV", "")
	// Subjects are case-sensitive: the dev subject is the token as typed.
	if id, err := resolveVerifier().Verify(" Alice "); err != nil || id.Subject != "Alice" {
		t.Fatalf("dev verifier = %+v %v", id, err)
	}
}
