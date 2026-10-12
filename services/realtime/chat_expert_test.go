package realtimehttp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// expertFixture: room-1 with maya and jack; maya owns the expert
// "reviewer" at 1.0.0 and 1.1.0.
func expertFixture(t *testing.T) (*Service, *store.MemoryBridgeJobs, *store.MemoryExtensionAudit, *store.MemoryContentComponents) {
	t.Helper()
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	mem.Add("room-1", "jack")
	channels := store.NewMemoryChannel()
	if err := channels.PutChannel(dctx(), store.RoomChannel{ChannelID: "ch_general", TenantID: tenant.Default, RoomID: "room-1", IsDefault: true, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	jobs, audit, content := store.NewMemoryBridgeJobs(), store.NewMemoryExtensionAudit(), store.NewMemoryContentComponents()
	c := store.ContentComponent{ID: "cc_rev", Kind: contentpkg.KindExpert, Name: "reviewer", TenantID: tenant.Default, Owner: "maya",
		Scope: store.ContentScopePersonal, Status: store.ContentStatusActive, DisplayName: "Reviewer"}
	manifest := func(v string) json.RawMessage {
		return json.RawMessage(`{"kind":"` + contentpkg.KindExpert + `","name":"reviewer","version":"` + v + `","displayName":"Reviewer"}`)
	}
	if err := content.CreateContentComponent(dctx(), c, store.ContentComponentVersion{Version: "1.0.0", ManifestJSON: manifest("1.0.0"), BundleBlobKey: "sha256/a"}); err != nil {
		t.Fatal(err)
	}
	if err := content.AddContentComponentVersion(dctx(), c, "1.0.0", store.ContentComponentVersion{Version: "1.1.0", ManifestJSON: manifest("1.1.0"), BundleBlobKey: "sha256/b"}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Hub: realtime.NewHub(64, 16), Membership: mem, Chat: store.NewMemoryChat(), Channels: channels,
		BridgeJobs: jobs, Audit: audit, Content: content}
	return svc, jobs, audit, content
}

func toJackCodex() *ChatRecipient {
	return &ChatRecipient{OwnerID: "jack", Provider: "codex", Core: "acme"}
}

// A chat to an agent with the author's expert pins the latest version on
// the job, carries the expert in the message, and audits the use.
func TestChatExpertPinnedOnJobAndAudited(t *testing.T) {
	svc, _, audit, _ := expertFixture(t)
	posted, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "review the diff", Recipient: toJackCodex(), Expert: &wire.ChatExpert{ID: "cc_rev"}})
	if err != nil {
		t.Fatal(err)
	}
	j := posted.Job
	if j == nil || j.ExpertID != "cc_rev" || j.ExpertVersion != "1.1.0" || j.ExpertName != "Reviewer" {
		t.Fatalf("job = %+v", j)
	}
	recs, _ := audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindContent}, Limit: 10})
	if len(recs) != 1 || recs[0].Name != "reviewer@1.1.0" || recs[0].Actor != "maya" || recs[0].Detail != "expert Reviewer v1.1.0 on agent jack/acme/codex (job "+j.JobID+", room room-1)" {
		t.Fatalf("audit = %+v", recs)
	}
	// An explicit version is honored.
	posted, err = svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "again", Recipient: toJackCodex(), Expert: &wire.ChatExpert{ID: "cc_rev", Version: "1.0.0"}})
	if err != nil || posted.Job.ExpertVersion != "1.0.0" {
		t.Fatalf("pinned = %+v, %v", posted.Job, err)
	}
}

// Someone else's personal expert is refused unless the room pinned it; an
// expert without an agent recipient and an unknown version are refused.
func TestChatExpertRefusals(t *testing.T) {
	svc, _, _, content := expertFixture(t)
	if _, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "jack", Text: "x", Recipient: toJackCodex(), Expert: &wire.ChatExpert{ID: "cc_rev"}}); !errors.Is(err, errExpertNotAvailable) {
		t.Fatalf("other member = %v", err)
	}
	if _, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "x", Expert: &wire.ChatExpert{ID: "cc_rev"}}); !errors.Is(err, errExpertWithoutAgent) {
		t.Fatalf("no agent = %v", err)
	}
	if _, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "x", Recipient: toJackCodex(), Expert: &wire.ChatExpert{ID: "cc_rev", Version: "9.0.0"}}); !errors.Is(err, errExpertNotAvailable) {
		t.Fatalf("unknown version = %v", err)
	}
	if _, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "x", Recipient: toJackCodex(), Expert: &wire.ChatExpert{ID: "cc_none"}}); !errors.Is(err, errExpertNotAvailable) {
		t.Fatalf("unknown expert = %v", err)
	}
	// maya pins her expert in the room at 1.0.0: jack may use it there, at
	// the pinned version.
	if err := content.PutContentUse(dctx(), store.ContentUse{TenantID: tenant.Default, ScopeKind: store.ContentUseRoom, ScopeID: "room-1", ComponentID: "cc_rev", Kind: contentpkg.KindExpert, PinnedVersion: "1.0.0", CreatedBy: "maya"}); err != nil {
		t.Fatal(err)
	}
	posted, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "jack", Text: "x", Recipient: toJackCodex(), Expert: &wire.ChatExpert{ID: "cc_rev"}})
	if err != nil || posted.Job.ExpertVersion != "1.0.0" {
		t.Fatalf("room-pinned = %+v, %v", posted.Job, err)
	}
}
