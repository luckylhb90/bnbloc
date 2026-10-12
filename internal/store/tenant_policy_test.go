package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// The tenant policy switches persist independently (an update leaves unset
// switches alone), survive an unrelated tenant upsert (PutTenant must never
// reset them), and a missing tenant is reported.
func TestTenantPolicy(t *testing.T) {
	stores := map[string]TenantStore{"memory": NewMemoryTenant()}
	if dsn := sharedMySQLDSN(t); dsn != "" {
		b, err := NewBackends(dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer b.DB.Close()
		stores["mysql"] = NewMySQLTenant(b.DB)
	}
	on, off := true, false
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			id := "pol-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if err := s.PutTenant(ctx, Tenant{TenantID: id, Name: "Policy Co", CreatedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.GetTenant(ctx, id); got.RequireSandboxForBorrow || got.PublicSharesDisabled {
				t.Fatalf("policy must default off: %+v", got)
			}
			got, err := s.SetTenantPolicy(ctx, id, TenantPolicyUpdate{RequireSandboxForBorrow: &on})
			if err != nil || !got.RequireSandboxForBorrow || got.PublicSharesDisabled {
				t.Fatalf("set sandbox: %+v %v", got, err)
			}
			if _, err := s.SetTenantPolicy(ctx, id, TenantPolicyUpdate{RequireSandboxForBorrow: &on}); err != nil {
				t.Fatalf("idempotent set: %v", err)
			}
			got, err = s.SetTenantPolicy(ctx, id, TenantPolicyUpdate{PublicSharesDisabled: &on})
			if err != nil || !got.RequireSandboxForBorrow || !got.PublicSharesDisabled {
				t.Fatalf("set public shares: %+v %v", got, err)
			}
			if err := s.PutTenant(ctx, Tenant{TenantID: id, Name: "Renamed", CreatedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetTenant(ctx, id)
			if err != nil || !got.RequireSandboxForBorrow || !got.PublicSharesDisabled || got.Name != "Renamed" {
				t.Fatalf("tenant = %+v %v", got, err)
			}
			got, err = s.SetTenantPolicy(ctx, id, TenantPolicyUpdate{RequireSandboxForBorrow: &off})
			if err != nil || got.RequireSandboxForBorrow || !got.PublicSharesDisabled {
				t.Fatalf("clear sandbox: %+v %v", got, err)
			}
			if _, err := s.SetTenantPolicy(ctx, "missing-"+id, TenantPolicyUpdate{PublicSharesDisabled: &on}); err != ErrTenantNotFound {
				t.Fatalf("missing tenant: %v", err)
			}
		})
	}
}
