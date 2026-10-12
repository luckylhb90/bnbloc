package realtimehttp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// copiesOf lists the shared copies a member holds of a component.
func copiesOf(t *testing.T, content store.ContentComponentStore, member, src string) []store.ContentComponent {
	t.Helper()
	out, err := content.ListContentComponents(dctx(), store.ContentComponentQuery{Owner: member, SourceID: src, Scopes: []store.ContentScope{store.ContentScopeShared}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A member who joins a room with a standing content share gets a copy at
// the shared version, attributed to its author, with an audit row; a
// second join changes nothing. A share whose author left the room, or that
// the author revoked, gives nothing ("Gap closure" 2).
func TestMemberJoinGetsRoomShares(t *testing.T) {
	svc, _, audit, content := expertFixture(t)
	src, _ := content.GetContentComponent(dctx(), "cc_rev")
	v, _ := content.GetContentComponentVersion(dctx(), "cc_rev", "1.0.0")
	if err := contentshare.RecordRoomShare(dctx(), content, src, v, "room-1", "", 1); err != nil {
		t.Fatal(err)
	}
	mem := svc.Membership.(*membership.Static)
	mem.Add("room-1", "bob")
	svc.grantRoomShares(dctx(), tenant.Default, "room-1", "bob")
	cps := copiesOf(t, content, "bob", "cc_rev")
	if len(cps) != 1 || cps[0].SharedBy != "maya" || cps[0].LatestVersion != "1.0.0" {
		t.Fatalf("bob's copies = %+v", cps)
	}
	recs, _ := audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindContent}, Limit: 10})
	if len(recs) != 1 || recs[0].Action != "share" || recs[0].Actor != "maya" || !strings.Contains(recs[0].Detail, "with bob (created, joined room room-1)") {
		t.Fatalf("audit = %+v", recs)
	}
	svc.grantRoomShares(dctx(), tenant.Default, "room-1", "bob")
	if recs, _ := audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindContent}, Limit: 10}); len(recs) != 1 {
		t.Fatalf("a second join audited again: %+v", recs)
	}
	// The author never gets a copy of her own component.
	svc.grantRoomShares(dctx(), tenant.Default, "room-1", "maya")
	if cps := copiesOf(t, content, "maya", "cc_rev"); len(cps) != 0 {
		t.Fatalf("author's copies = %+v", cps)
	}
	// Someone who is not a human member of the room gets nothing.
	svc.grantRoomShares(dctx(), tenant.Default, "room-1", "eve")
	if cps := copiesOf(t, content, "eve", "cc_rev"); len(cps) != 0 {
		t.Fatalf("non-member's copies = %+v", cps)
	}
	// Revoked: later joiners get nothing; bob keeps his copy.
	if ok, err := content.DeleteContentRoomShare(dctx(), "room-1", "cc_rev"); err != nil || !ok {
		t.Fatalf("revoke = %v %v", ok, err)
	}
	mem.Add("room-1", "vic")
	svc.grantRoomShares(dctx(), tenant.Default, "room-1", "vic")
	if cps := copiesOf(t, content, "vic", "cc_rev"); len(cps) != 0 {
		t.Fatalf("copies after revoke = %+v", cps)
	}
	if cps := copiesOf(t, content, "bob", "cc_rev"); len(cps) != 1 {
		t.Fatal("revoking took bob's copy")
	}
}

// A share stops handing out copies once its author has left the room.
func TestRoomShareStopsWhenTheAuthorLeft(t *testing.T) {
	svc, _, _, content := expertFixture(t)
	src, _ := content.GetContentComponent(dctx(), "cc_rev")
	v, _ := content.GetContentComponentVersion(dctx(), "cc_rev", "1.1.0")
	if err := contentshare.RecordRoomShare(dctx(), content, src, v, "room-1", "", 1); err != nil {
		t.Fatal(err)
	}
	mem := membership.NewStatic()
	mem.Add("room-1", "jack")
	mem.Add("room-1", "bob")
	svc.Membership = mem // maya left
	svc.grantRoomShares(dctx(), tenant.Default, "room-1", "bob")
	if cps := copiesOf(t, content, "bob", "cc_rev"); len(cps) != 0 {
		t.Fatalf("copies after the author left = %+v", cps)
	}
}

// The room service's member_joined event, through realtime's signed
// internal publish, triggers the copy.
func TestMemberJoinedEventThroughInternalPublish(t *testing.T) {
	const key = "internal-key"
	t.Setenv("BNBLOC_INTERNAL_KEY", key)
	svc, _, _, content := expertFixture(t)
	src, _ := content.GetContentComponent(dctx(), "cc_rev")
	v, _ := content.GetContentComponentVersion(dctx(), "cc_rev", "1.1.0")
	if err := contentshare.RecordRoomShare(dctx(), content, src, v, "room-1", "for the review rota", 1); err != nil {
		t.Fatal(err)
	}
	svc.Membership.(*membership.Static).Add("room-1", "bob")
	srv := httptest.NewServer(svcauth.Middleware(key, svc.Handler()))
	defer srv.Close()
	sub := svc.Hub.SubscribeLive("room-1")
	defer svc.Hub.Unsubscribe("room-1", sub)
	realtime.NewHTTPPublisher(srv.URL, "room").Publish(dctx(), wire.Event{RoomID: "room-1", Kind: wire.EventMemberJoined, Payload: wire.MemberJoinedPayload{UserID: "bob"}})
	select {
	case ev := <-sub.Events():
		raw, _ := json.Marshal(ev.Payload)
		if ev.Kind != wire.EventMemberJoined || !strings.Contains(string(raw), `"userId":"bob"`) {
			t.Fatalf("delivered %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("member_joined not delivered to the room")
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(copiesOf(t, content, "bob", "cc_rev")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no copy for the new member")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := copiesOf(t, content, "bob", "cc_rev")[0]; got.LatestVersion != "1.1.0" {
		t.Fatalf("copy = %+v", got)
	}
}
