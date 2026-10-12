package store

import (
	"context"
	"testing"
)

// ContentComponentsByBlob answers the bundle entitlement question in one
// query, the same on both backends: the components bundling the key that a
// member could reach — their own of the tenant, the tenant's organization
// components, platform components and the components the named room uses —
// by id, and never another member's, another tenant's, or one whose
// versions bundle another key.
func TestContractContentComponentStoreByBlob(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.ContentComponents
		ctx := context.Background()
		p := uniq("ccb-")
		tid, other := p+"-t", p+"-other"
		key, otherKey := uniqBlobKey(), uniqBlobKey()
		mk := func(suffix string, scope ContentScope, tenantID, owner, blob string) ContentComponent {
			t.Helper()
			c := ContentComponent{ID: p + suffix, Kind: "bnbloc.expert.package/v1", Name: p + suffix, TenantID: tenantID, Owner: owner,
				Scope: scope, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
			if scope == ContentScopeShared {
				c.SourceID, c.SharedBy = p+"-src", "jack"
			}
			if err := s.CreateContentComponent(ctx, c, contentVersion(c.ID, "1.0.0", blob, 1)); err != nil {
				t.Fatalf("create %s: %v", c.ID, err)
			}
			return c
		}
		own := mk("-a-own", ContentScopePersonal, tid, "maya", key)
		copyOf := mk("-b-copy", ContentScopeShared, tid, "maya", key)
		org := mk("-c-org", ContentScopeOrg, tid, "jack", key)
		plat := mk("-d-plat", ContentScopePlatform, "", "", key)
		jacks := mk("-e-jacks", ContentScopePersonal, tid, "jack", key)
		roomUsed := mk("-f-room", ContentScopePersonal, tid, "jack", otherKey)
		foreign := mk("-g-foreign", ContentScopeOrg, other, "zed", key)
		// A later version bundling the key counts too.
		if err := s.AddContentComponentVersion(ctx, roomUsed, "1.0.0", contentVersion(roomUsed.ID, "1.1.0", key, 2)); err != nil {
			t.Fatal(err)
		}
		_ = mk("-h-elsewhere", ContentScopePersonal, tid, "maya", otherKey)
		room := p + "-room"
		if err := s.PutContentUse(ctx, ContentUse{TenantID: tid, ScopeKind: ContentUseRoom, ScopeID: room, ComponentID: roomUsed.ID, Kind: roomUsed.Kind, CreatedBy: "jack", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		ids := func(q ContentBlobQuery) []string {
			t.Helper()
			var out []string
			for _, c := range must(s.ContentComponentsByBlob(ctx, q))(t) {
				out = append(out, c.ID)
			}
			return out
		}
		eq := func(got []string, want ...string) bool {
			if len(got) != len(want) {
				return false
			}
			for i := range want {
				if got[i] != want[i] {
					return false
				}
			}
			return true
		}
		if got := ids(ContentBlobQuery{Key: key, Subject: "maya", Tenant: tid}); !eq(got, own.ID, copyOf.ID, org.ID, plat.ID) {
			t.Fatalf("maya's reach = %v", got)
		}
		if got := ids(ContentBlobQuery{Key: key, Subject: "maya", Tenant: tid, RoomID: room}); !eq(got, own.ID, copyOf.ID, org.ID, plat.ID, roomUsed.ID) {
			t.Fatalf("maya's reach with the room = %v", got)
		}
		if got := ids(ContentBlobQuery{Key: key, Subject: "jack", Tenant: tid}); !eq(got, org.ID, plat.ID, jacks.ID, roomUsed.ID) {
			t.Fatalf("jack's reach = %v", got)
		}
		if got := ids(ContentBlobQuery{Key: key, Subject: "Maya", Tenant: tid}); !eq(got, org.ID, plat.ID) {
			t.Fatalf("a case-variant subject's reach = %v; owner ids are exact", got)
		}
		if got := ids(ContentBlobQuery{Key: key, Subject: "zed", Tenant: other}); !eq(got, plat.ID, foreign.ID) {
			t.Fatalf("another tenant's reach = %v", got)
		}
		if got := must(s.ContentComponentsByBlob(ctx, ContentBlobQuery{Key: uniqBlobKey(), Subject: "maya", Tenant: tid}))(t); got == nil || len(got) != 0 {
			t.Fatalf("an unknown key = %#v, want []", got)
		}
	})
}
