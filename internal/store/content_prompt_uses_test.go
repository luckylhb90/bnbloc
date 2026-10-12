package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func TestContractContentComponentStorePromptUses(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		testContentPromptUses(t, b.ContentComponents, uniq("pu-"))
		if b.DB == nil {
			return
		}
		// The prompt library's table is gone (start empty).
		var n int
		if err := b.DB.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'prompts'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("prompts table count = %d, %v", n, err)
		}
	})
}

// Use counts are per organization: built-in and platform content is used
// by every organization, and organization A's prompt insertions, member
// uses and scope uses never show in organization B's counts (docs/bugs/
// 2026-10-13-starters-and-shared-use-counts.md).
func TestContractContentComponentStoreTenantUseCounts(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s, prefix := b.ContentComponents, uniq("tuc-")
		ctx := context.Background()
		orgA, orgB := prefix+"-a", prefix+"-b"
		platform, builtin := prefix+"-platform", BuiltinContentIDPrefix+"starters"
		c := ContentComponent{ID: platform, Kind: "bnbloc.prompt.package/v1", Name: "shared-" + prefix,
			Scope: ContentScopePlatform, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
		if err := s.CreateContentComponent(ctx, c, contentVersion(platform, "1.0.0", uniqBlobKey(), 1)); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{platform, builtin} {
			for i := 0; i < 3; i++ {
				if err := s.RecordContentPromptUse(ctx, orgA, id, "notes", int64(10+i)); err != nil {
					t.Fatalf("record %s in A: %v", id, err)
				}
			}
			if err := s.RecordContentPromptUse(ctx, orgB, id, "notes", 20); err != nil {
				t.Fatalf("record %s in B: %v", id, err)
			}
			// Org A's members and rooms turn it on; org B's member too.
			for _, u := range []ContentUse{
				{TenantID: orgA, ScopeKind: ContentUseMember, ScopeID: prefix + "-maya"},
				{TenantID: orgA, ScopeKind: ContentUseMember, ScopeID: prefix + "-jack"},
				{TenantID: orgA, ScopeKind: ContentUseRoom, ScopeID: prefix + "-room-a"},
				{TenantID: orgB, ScopeKind: ContentUseMember, ScopeID: prefix + "-olga"},
			} {
				u.ComponentID, u.Kind, u.CreatedBy, u.CreatedAtMs = id, c.Kind, u.ScopeID, 1
				if err := s.PutContentUse(ctx, u); err != nil {
					t.Fatalf("put use of %s: %v", id, err)
				}
			}
		}
		ids := []string{platform, builtin}
		for _, tc := range []struct {
			org                     string
			prompts, members, scope int
		}{{orgA, 3, 2, 3}, {orgB, 1, 1, 1}, {prefix + "-c", 0, 0, 0}} {
			prompts, err := s.ContentPromptUses(ctx, tc.org, ids)
			if err != nil {
				t.Fatal(err)
			}
			members, err := s.CountMemberUses(ctx, tc.org, ids)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := s.CountContentUses(ctx, tc.org, ids)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				if got := prompts[id]["notes"]; got != int64(tc.prompts) {
					t.Errorf("%s prompt uses of %s = %d, want %d", tc.org, id, got, tc.prompts)
				}
				if members[id] != tc.members || scope[id] != tc.scope {
					t.Errorf("%s uses of %s: members %d scopes %d, want %d %d", tc.org, id, members[id], scope[id], tc.members, tc.scope)
				}
			}
		}
		// A count always names its organization.
		if err := s.RecordContentPromptUse(ctx, "", platform, "notes", 30); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("record without a tenant = %v", err)
		}
		if _, err := s.ContentPromptUses(ctx, "", ids); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("read prompt uses without a tenant = %v", err)
		}
		if _, err := s.CountMemberUses(ctx, "", ids); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("member uses without a tenant = %v", err)
		}
		if _, err := s.CountContentUses(ctx, "", ids); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("scope uses without a tenant = %v", err)
		}
	})
}

func testContentPromptUses(t *testing.T, s ContentComponentStore, prefix string) {
	ctx := context.Background()
	blob := "sha256/" + strings.Repeat("c", 64)
	tid := prefix + "-t"
	mk := func(id string) {
		c := ContentComponent{ID: id, Kind: "bnbloc.prompt.package/v1", Name: "writing-" + strings.TrimPrefix(id, prefix), TenantID: tid, Owner: "maya",
			Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
		if err := s.CreateContentComponent(ctx, c, contentVersion(id, "1.0.0", blob, 1)); err != nil {
			t.Fatal(err)
		}
	}
	a, b := prefix+"-a", prefix+"-b"
	mk(a)
	mk(b)
	for i := 0; i < 3; i++ {
		if err := s.RecordContentPromptUse(ctx, tid, a, "notes", int64(10+i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordContentPromptUse(ctx, tid, a, "summary", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordContentPromptUse(ctx, tid, b, "notes", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordContentPromptUse(ctx, tid, prefix+"-missing", "notes", 20); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("missing component = %v", err)
	}
	got, err := s.ContentPromptUses(ctx, tid, []string{a, b, prefix + "-none"})
	if err != nil || got[a]["notes"] != 3 || got[a]["summary"] != 1 || got[b]["notes"] != 1 || len(got) != 2 {
		t.Fatalf("uses = %+v, %v", got, err)
	}
	if empty, err := s.ContentPromptUses(ctx, tid, nil); err != nil || len(empty) != 0 {
		t.Fatalf("no ids = %+v, %v", empty, err)
	}
	// Deleting a component deletes its counts.
	if err := s.DeleteContentComponent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err = s.ContentPromptUses(ctx, tid, []string{a, b})
	if err != nil || len(got[a]) != 0 || got[b]["notes"] != 1 {
		t.Fatalf("after delete = %+v, %v", got, err)
	}
}
