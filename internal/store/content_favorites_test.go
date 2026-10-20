package store

import (
	"context"
	"errors"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Favorites are a member's, in one organization: organization A's stars
// never show in organization B's counts or listings; a repeat keeps the
// first star; a missing component is refused;
// deleting the component deletes its stars.
func TestContractContentFavoriteStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s, prefix := b.ContentComponents, uniq("fav-")
		ctx := context.Background()
		orgA, orgB := prefix+"-a", prefix+"-b"
		app, other, builtin := prefix+"-app", prefix+"-other", prefix+"-starters"
		for _, id := range []string{app, other, builtin} {
			c := ContentComponent{ID: id, Kind: "bnbloc.work.package/v1", Name: id, Scope: ContentScopePlatform, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
			if err := s.CreateContentComponent(ctx, c, contentVersion(id, "1.0.0", uniqBlobKey(), 1)); err != nil {
				t.Fatal(err)
			}
		}
		star := func(tid, member, id string, at int64) {
			t.Helper()
			if err := s.PutContentFavorite(ctx, ContentFavorite{TenantID: tid, Member: member, ComponentID: id, CreatedAtMs: at}); err != nil {
				t.Fatalf("star %s for %s in %s: %v", id, member, tid, err)
			}
		}
		star(orgA, "maya", app, 10)
		star(orgA, "maya", app, 99) // a repeat keeps the first star
		star(orgA, "maya", builtin, 11)
		star(orgA, "jack", app, 12)
		star(orgB, "olga", app, 13)
		star(orgB, "maya", other, 14) // the same subject in another organization

		if err := s.PutContentFavorite(ctx, ContentFavorite{TenantID: orgA, Member: "maya", ComponentID: prefix + "-gone", CreatedAtMs: 1}); !errors.Is(err, ErrContentNotFound) {
			t.Fatalf("star of a missing component = %v", err)
		}
		if err := s.PutContentFavorite(ctx, ContentFavorite{Member: "maya", ComponentID: app}); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("star without a tenant = %v", err)
		}

		counts, err := s.CountContentFavorites(ctx, orgA, []string{app, other, builtin})
		if err != nil || counts[app] != 2 || counts[builtin] != 1 || counts[other] != 0 {
			t.Fatalf("org A counts = %v, %v", counts, err)
		}
		if counts, err := s.CountContentFavorites(ctx, orgB, []string{app, other, builtin}); err != nil || counts[app] != 1 || counts[other] != 1 || counts[builtin] != 0 {
			t.Fatalf("org B counts = %v, %v", counts, err)
		}
		mine, err := s.ListContentFavorites(ctx, orgA, "maya", nil)
		if err != nil || len(mine) != 2 || mine[0].ComponentID != builtin || mine[1].ComponentID != app || mine[1].CreatedAtMs != 10 {
			t.Fatalf("maya's stars in A = %+v, %v (newest first, the first star kept)", mine, err)
		}
		if got, err := s.ListContentFavorites(ctx, orgA, "maya", []string{other, app}); err != nil || len(got) != 1 || got[0].ComponentID != app {
			t.Fatalf("maya's stars in A among two = %+v, %v", got, err)
		}
		if got, err := s.ListContentFavorites(ctx, orgA, "maya", []string{}); err != nil || len(got) != 0 {
			t.Fatalf("maya's stars among none = %+v, %v", got, err)
		}
		if got, err := s.ListContentFavorites(ctx, orgB, "maya", nil); err != nil || len(got) != 1 || got[0].ComponentID != other {
			t.Fatalf("maya's stars in B = %+v, %v", got, err)
		}
		if _, err := s.CountContentFavorites(ctx, "", []string{app}); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("count without a tenant = %v", err)
		}

		if err := s.DeleteContentFavorite(ctx, orgA, "maya", app); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteContentFavorite(ctx, orgA, "maya", app); err != nil {
			t.Fatalf("a second unstar = %v", err)
		}
		if counts, _ := s.CountContentFavorites(ctx, orgA, []string{app}); counts[app] != 1 {
			t.Fatalf("org A count after maya's unstar = %v", counts)
		}
		if err := s.DeleteContentComponent(ctx, app); err != nil {
			t.Fatal(err)
		}
		for _, tid := range []string{orgA, orgB} {
			if counts, _ := s.CountContentFavorites(ctx, tid, []string{app}); counts[app] != 0 {
				t.Fatalf("stars of a deleted component in %s = %v", tid, counts)
			}
		}
	})
}

// A new organization requires approval for publishing to it; an org admin
// turns it off (and on again) through the policy setter, which keeps the
// other switches.
func TestContractTenantStorePublishApproval(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s, ctx := b.Tenant, context.Background()
		id := uniq("pa-")
		if err := s.PutTenant(ctx, Tenant{TenantID: id, Name: "Org", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetTenant(ctx, id); err != nil || got.PublishApproval != wire.PublishApprovalRequired {
			t.Fatalf("a new tenant's publish approval = %q, %v", got.PublishApproval, err)
		}
		none, on := wire.PublishApprovalNone, true
		if got, err := s.SetTenantPolicy(ctx, id, TenantPolicyUpdate{PublishApproval: &none}); err != nil || got.PublishApproval != none {
			t.Fatalf("set none = %+v, %v", got, err)
		}
		if got, err := s.SetTenantPolicy(ctx, id, TenantPolicyUpdate{AnonymousLinksDisabled: &on}); err != nil || got.PublishApproval != none || !got.AnonymousLinksDisabled {
			t.Fatalf("another switch = %+v, %v (publish approval kept)", got, err)
		}
		if err := s.PutTenant(ctx, Tenant{TenantID: id, Name: "Renamed", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetTenant(ctx, id); got.PublishApproval != none {
			t.Fatalf("a re-put changed the publish approval to %q", got.PublishApproval)
		}
	})
}
