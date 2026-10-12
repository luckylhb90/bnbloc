package main

import (
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
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

// The super-admin bootstrap mints a short-lived super_admin session only for
// a listed subject, signed with the gateway's stable key.
func TestMintAdminToken(t *testing.T) {
	now := time.Now()
	tok, err := mintAdminToken("admin@corp.example", []string{" admin@corp.example"}, "signing-key", now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.NewSigner([]byte("signing-key")).Verify(tok)
	if err != nil || c.Role != string(rbac.RoleSuperAdmin) || c.ExpiresAtUnix > now.Add(bootstrapTokenTTL).Unix() {
		t.Fatalf("minted claims = %+v err=%v", c, err)
	}
	if _, err := mintAdminToken("mallory", []string{"admin@corp.example"}, "signing-key", now); err == nil {
		t.Fatal("minted a token for an unlisted subject")
	}
	// Subjects are case-sensitive: a case variant of a listed subject is
	// another identity.
	if _, err := mintAdminToken("Admin@Corp.example", []string{"admin@corp.example"}, "signing-key", now); err == nil {
		t.Fatal("minted a token for a case variant of a listed subject")
	}
	if _, err := mintAdminToken("admin@corp.example", []string{"admin@corp.example"}, "", now); err == nil {
		t.Fatal("minted a token without a stable signing key")
	}
}
