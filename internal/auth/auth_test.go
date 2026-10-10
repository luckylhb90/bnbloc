package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSignerRoundTripsClaims(t *testing.T) {
	s := NewSigner([]byte("key"))
	in := Claims{Subject: "alice", Rooms: []string{"r1"}, Tenant: "acme", Role: "member", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}
	got, err := s.Verify(s.Issue(in))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Subject != "alice" || got.Tenant != "acme" || got.Role != "member" || !got.HasRoom("r1") || got.HasRoom("r2") {
		t.Fatalf("claims = %+v", got)
	}
}

func TestSignerRejectsTamperingWrongKeyAndExpiry(t *testing.T) {
	s := NewSigner([]byte("key"))
	exp := time.Now().Add(time.Hour).Unix()
	tok := s.Issue(Claims{Subject: "alice", Role: "member", ExpiresAtUnix: exp})

	// Payload swapped for an escalated role, original signature kept.
	forged := NewSigner([]byte("other")).Issue(Claims{Subject: "alice", Role: "super_admin", ExpiresAtUnix: exp})
	tampered := strings.SplitN(forged, ".", 2)[0] + "." + strings.SplitN(tok, ".", 2)[1]
	cases := map[string]string{
		"tampered payload": tampered,
		"wrong key":        forged,
		"garbage":          "not-a-token",
		"empty":            "",
		"no expiry":        s.Issue(Claims{Subject: "alice"}),
		"expired":          s.Issue(Claims{Subject: "alice", ExpiresAtUnix: time.Now().Add(-time.Minute).Unix()}),
	}
	for name, token := range cases {
		if _, err := s.Verify(token); err != ErrInvalidToken {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

// A refresh token is never a bearer, and a session is never a refresh token.
func TestRefreshAndSessionTokensAreNotInterchangeable(t *testing.T) {
	s := NewSigner([]byte("k"))
	exp := time.Now().Add(time.Hour).Unix()
	session := s.Issue(Claims{Subject: "alice", ExpiresAtUnix: exp})
	refresh := s.Issue(Claims{Subject: "alice", Kind: KindRefresh, ExpiresAtUnix: exp})
	if _, err := s.Verify(refresh); err == nil {
		t.Fatal("Verify accepted a refresh token as a session bearer")
	}
	if _, err := s.VerifyRefresh(session); err == nil {
		t.Fatal("VerifyRefresh accepted a session token")
	}
	if c, err := s.VerifyRefresh(refresh); err != nil || c.Subject != "alice" {
		t.Fatalf("VerifyRefresh(refresh) = %+v, %v", c, err)
	}
	expired := s.Issue(Claims{Subject: "alice", Kind: KindRefresh, ExpiresAtUnix: time.Now().Add(-time.Minute).Unix()})
	if _, err := s.VerifyRefresh(expired); err == nil {
		t.Fatal("VerifyRefresh accepted an expired refresh token")
	}
}

// PeekSession names a bearer's owner without the signing key, for clients
// holding their own token; it never verifies, so it refuses only what is not
// a session at all.
func TestPeekSession(t *testing.T) {
	s := NewSigner([]byte("k"))
	exp := time.Now().Add(time.Hour).Unix()
	c, err := PeekSession(s.Issue(Claims{Subject: "maya", Tenant: "acme", ExpiresAtUnix: exp}))
	if err != nil || c.Subject != "maya" || c.Tenant != "acme" || c.ExpiresAtUnix != exp {
		t.Fatalf("PeekSession = %+v, %v", c, err)
	}
	// An expired session still decodes: expiry is the caller's to judge.
	if c, err := PeekSession(s.Issue(Claims{Subject: "maya", ExpiresAtUnix: 1})); err != nil || c.ExpiresAtUnix != 1 {
		t.Fatalf("expired session = %+v, %v", c, err)
	}
	for name, tok := range map[string]string{
		"no signature":  "opaque",
		"not base64url": "!!!.sig",
		"not JSON":      "bm90IGpzb24.sig",
		"refresh token": s.Issue(Claims{Subject: "maya", Kind: KindRefresh, ExpiresAtUnix: exp}),
		"frame ticket":  s.Issue(Claims{Subject: "maya", Kind: KindSiteFrame, ExpiresAtUnix: exp}),
		"no subject":    s.Issue(Claims{ExpiresAtUnix: exp}),
	} {
		if _, err := PeekSession(tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
