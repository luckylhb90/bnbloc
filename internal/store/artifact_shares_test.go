package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// testArtifactShares runs the share-link contract against any ArtifactStore:
// create/list/revoke in the context tenant, tenant isolation, the
// tenant-agnostic resolve, view counting, and the org-admin list (active
// filter, 30-day window, titles, artifacts that are gone).
func testArtifactShares(t *testing.T, s ArtifactStore, ctx context.Context, sfx string) {
	t.Helper()
	room := "r-share-" + sfx
	artID := "art_share_" + sfx
	if _, err := s.PutArtifact(ctx, Artifact{ArtifactID: artID, RoomID: room, Kind: ArtifactKindMarkdown, Title: "Spec " + sfx, CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	const now = int64(1_000_000_000)
	mk := func(id string, createdAt, expiresAt int64) ArtifactShare {
		sh, err := s.CreateArtifactShare(ctx, ArtifactShare{
			ShareID: id, TokenHash: HashArtifactContent(id), RoomID: room, ArtifactID: artID,
			CreatedBy: "alice", CreatedAtMs: createdAt, ExpiresAtMs: expiresAt,
		})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return sh
	}
	idOf := func(n string) string { return strings.Repeat("0", 32-len(sfx)-len(n)) + sfx + n }
	live := mk(idOf("a"), now-10, now+1000)
	expiredRecent := mk(idOf("b"), now-20, now-1000)
	expiredOld := mk(idOf("c"), now-40*24*3600*1000, now-31*24*3600*1000)
	revoked := mk(idOf("d"), now-5, now+1000)

	if live.TenantID != tenant.FromContext(ctx) {
		t.Fatalf("tenant = %q", live.TenantID)
	}
	if _, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: live.ShareID, TokenHash: HashArtifactContent("x" + sfx), RoomID: room, ArtifactID: artID, CreatedBy: "alice", CreatedAtMs: 1, ExpiresAtMs: 2}); err == nil {
		t.Fatal("duplicate share id accepted")
	}

	r1, err := s.RevokeArtifactShare(ctx, revoked.ShareID, "bob", now-1)
	if err != nil || r1.RevokedAtMs != now-1 || r1.RevokedBy != "bob" || r1.Active(now) {
		t.Fatalf("revoke = %+v %v", r1, err)
	}
	r2, err := s.RevokeArtifactShare(ctx, revoked.ShareID, "carol", now)
	if err != nil || r2.RevokedAtMs != now-1 || r2.RevokedBy != "bob" {
		t.Fatalf("re-revoke must keep the first revocation: %+v %v", r2, err)
	}
	if _, err := s.RevokeArtifactShare(ctx, "missing-"+sfx, "bob", now); !errors.Is(err, ErrArtifactShareNotFound) {
		t.Fatalf("revoke missing = %v", err)
	}

	window := now - 30*24*3600*1000
	list, err := s.ListArtifactShares(ctx, room, artID, window)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sh := range list {
		ids = append(ids, sh.ShareID)
	}
	if want := []string{revoked.ShareID, live.ShareID, expiredRecent.ShareID}; strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("list = %v, want %v (newest first, the 31-day-old link dropped)", ids, want)
	}
	_ = expiredOld

	// Views count on the tenant-scoped row.
	for i := 0; i < 2; i++ {
		if err := s.CountArtifactShareView(ctx, live.ShareID, now+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetArtifactShare(ctx, live.ShareID)
	if err != nil || got.ViewCount != 2 || got.LastViewedAtMs != now+1 || got.TokenHash != HashArtifactContent(live.ShareID) {
		t.Fatalf("after views = %+v %v", got, err)
	}

	// Another tenant cannot see, revoke or count it; resolve finds it anywhere.
	other := tenant.NewContext(context.Background(), "t-other-"+sfx)
	if _, err := s.GetArtifactShare(other, live.ShareID); !errors.Is(err, ErrArtifactShareNotFound) {
		t.Fatalf("cross-tenant get = %v", err)
	}
	if _, err := s.RevokeArtifactShare(other, live.ShareID, "eve", now); !errors.Is(err, ErrArtifactShareNotFound) {
		t.Fatalf("cross-tenant revoke = %v", err)
	}
	if err := s.CountArtifactShareView(other, live.ShareID, now); !errors.Is(err, ErrArtifactShareNotFound) {
		t.Fatalf("cross-tenant view = %v", err)
	}
	if l, _ := s.ListArtifactShares(other, room, artID, 0); len(l) != 0 {
		t.Fatalf("cross-tenant list = %+v", l)
	}
	res, err := s.ResolveArtifactShare(context.Background(), live.ShareID)
	if err != nil || res.TenantID != live.TenantID || res.ViewCount != 2 {
		t.Fatalf("resolve = %+v %v", res, err)
	}
	if _, err := s.ResolveArtifactShare(context.Background(), "nope-"+sfx); !errors.Is(err, ErrArtifactShareNotFound) {
		t.Fatalf("resolve unknown = %v", err)
	}
	if _, err := s.CreateArtifactShare(context.Background(), live); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("create without tenant = %v", err)
	}

	// Org-admin list: titles, the active filter, the window.
	all, err := s.ListTenantArtifactShares(ctx, TenantShareQuery{EndedAfterMs: window})
	if err != nil || len(all) != 3 || all[0].ArtifactTitle != "Spec "+sfx || all[0].RoomID != room {
		t.Fatalf("tenant list = %+v %v", all, err)
	}
	active, err := s.ListTenantArtifactShares(ctx, TenantShareQuery{ActiveAtMs: now, EndedAfterMs: window})
	if err != nil || len(active) != 1 || active[0].ShareID != live.ShareID {
		t.Fatalf("active tenant list = %+v %v", active, err)
	}
	if l, _ := s.ListTenantArtifactShares(other, TenantShareQuery{}); len(l) != 0 {
		t.Fatalf("other tenant list = %+v", l)
	}
	// A link of an artifact that no longer exists is not listed.
	if _, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: idOf("e"), TokenHash: HashArtifactContent(idOf("e")), RoomID: room, ArtifactID: "art_gone_" + sfx, CreatedBy: "alice", CreatedAtMs: now, ExpiresAtMs: now + 1000}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListTenantArtifactShares(ctx, TenantShareQuery{ActiveAtMs: now, EndedAfterMs: window}); len(l) != 1 {
		t.Fatalf("a gone artifact's link is listed: %+v", l)
	}
}

func TestMemoryArtifactShares(t *testing.T) {
	testArtifactShares(t, NewMemoryArtifacts(), actx(), "mem")
}

func TestMySQLArtifactShares(t *testing.T) {
	m, closeDB := mysqlArtifacts(t)
	defer closeDB()
	sfx := itoaBase(time.Now().UnixNano())
	ctx := tenant.NewContext(context.Background(), "t-shares-"+sfx)
	testArtifactShares(t, m, ctx, sfx)
}
