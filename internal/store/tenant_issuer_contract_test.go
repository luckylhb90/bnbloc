package store

import (
	"context"
	"errors"
	"testing"
)

// An SSO issuer maps at most one tenant on both backends: a second tenant
// claiming it is ErrSSOIssuerTaken (and changes nothing), a changed issuer
// releases the old one, and any number of tenants have none.
func TestContractTenantIssuerUnique(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.Tenant
		acme, nimbus, orbit := uniq("acme-"), uniq("nimbus-"), uniq("orbit-")
		issuerX, issuerY := uniq("x-")+".example.com", uniq("y-")+".example.com"
		put := func(id, name, issuer string) error {
			return s.PutTenant(ctx, Tenant{TenantID: id, Name: name, Status: TenantActive, SSOIssuer: issuer, CreatedAtMs: 1})
		}
		owner := func(issuer string) string {
			t.Helper()
			id, found, err := s.TenantForIssuer(ctx, issuer)
			if err != nil {
				t.Fatalf("TenantForIssuer(%s): %v", issuer, err)
			}
			if !found {
				return ""
			}
			return id
		}

		if err := put(acme, "Acme", issuerX); err != nil {
			t.Fatalf("put acme: %v", err)
		}
		if err := put(acme, "Acme", issuerX); err != nil {
			t.Fatalf("re-put acme with its own issuer: %v", err)
		}
		if err := put(nimbus, "Nimbus", issuerX); !errors.Is(err, ErrSSOIssuerTaken) {
			t.Fatalf("second tenant on a taken issuer = %v; want ErrSSOIssuerTaken", err)
		}
		if _, err := s.GetTenant(ctx, nimbus); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("refused tenant was stored: %v", err)
		}
		if got, err := s.GetTenant(ctx, acme); err != nil || got.Name != "Acme" || got.SSOIssuer != issuerX {
			t.Fatalf("issuer holder = %+v, %v; want it untouched", got, err)
		}
		if got := owner(issuerX); got != acme {
			t.Fatalf("issuer X maps %q; want %s", got, acme)
		}

		// Tenants without an issuer never clash.
		if err := put(nimbus, "Nimbus", ""); err != nil {
			t.Fatalf("put nimbus without issuer: %v", err)
		}
		if err := put(orbit, "Orbit", ""); err != nil {
			t.Fatalf("put orbit without issuer: %v", err)
		}
		if got, err := s.GetTenant(ctx, orbit); err != nil || got.SSOIssuer != "" {
			t.Fatalf("issuer-less tenant = %+v, %v", got, err)
		}
		if got := owner(""); got != "" {
			t.Fatalf("empty issuer maps %q; want none", got)
		}

		// A changed issuer releases the old one.
		if err := put(acme, "Acme", issuerY); err != nil {
			t.Fatalf("move acme to Y: %v", err)
		}
		if got := owner(issuerX); got != "" {
			t.Fatalf("released issuer X still maps %q", got)
		}
		if got := owner(issuerY); got != acme {
			t.Fatalf("issuer Y maps %q; want %s", got, acme)
		}
		if err := put(nimbus, "Nimbus", issuerX); err != nil {
			t.Fatalf("claim released issuer X: %v", err)
		}
		if got := owner(issuerX); got != nimbus {
			t.Fatalf("issuer X maps %q; want %s", got, nimbus)
		}
	})
}
