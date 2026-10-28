package activity

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A run (and its job row) of a members-only section's channel is listed to
// the channel's readers only (docs/prd/2026-10-28-channels-and-threads.md
// C11): bob, a member of the room but not of the section, sees neither the
// run nor its record; alice, the section's member, sees both.
func TestMembersOnlyRunsAreTheirReaders(t *testing.T) {
	svc := runsFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	ch := store.NewMemoryChannel()
	if err := ch.PutSection(ctx, store.RoomSection{SectionID: "sec_x", RoomID: "room-1", Name: "Secret",
		Visibility: wire.SectionMembers, JoinPolicy: store.SectionJoinInvite, Owners: []string{"alice"}, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ch.PutChannel(ctx, store.RoomChannel{ChannelID: "ch_secret", RoomID: "room-1", Name: "secret", SectionID: "sec_x", CreatedBy: "alice", CreatedAtMs: 2}); err != nil {
		t.Fatal(err)
	}
	if err := ch.AddSectionMember(ctx, store.SectionMember{RoomID: "room-1", SectionID: "sec_x", UserID: "alice", AddedBy: "alice", JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	svc.Access = ch
	if _, err := svc.BridgeJobs.PutAgentJob(ctx, store.AgentJob{JobID: "j-secret", RoomID: "room-1", ChannelID: "ch_secret", MessageSeq: 20,
		RequesterUserID: "alice", OwnerUserID: "alice", Core: "c1", Provider: "codex", MessageText: "the secret plan", CreatedAtMs: 4_000}); err != nil {
		t.Fatal(err)
	}
	_, bob := getRuns(t, svc, "/v1/rooms/room-1/jobs", "bob")
	_, alice := getRuns(t, svc, "/v1/rooms/room-1/jobs", "alice")
	if strings.Contains(runIDs(bob.Runs), "j-secret") || !strings.Contains(runIDs(alice.Runs), "j-secret") {
		t.Fatalf("runs: bob %s, alice %s", runIDs(bob.Runs), runIDs(alice.Runs))
	}
	if code, _ := getRaw(t, svc, "/v1/rooms/room-1/jobs/j-secret", "bob"); code != http.StatusNotFound {
		t.Fatalf("a non-member's run record = %d, want 404", code)
	}
	if code, _ := getRaw(t, svc, "/v1/rooms/room-1/jobs/j-secret", "alice"); code != http.StatusOK {
		t.Fatalf("a member's run record = %d", code)
	}
	_, raw := getRaw(t, svc, "/v1/rooms/room-1/agent-jobs", "bob")
	if strings.Contains(string(raw), "j-secret") {
		t.Fatalf("a non-member's job rows hold the section's job: %s", raw)
	}
	_, raw = getRaw(t, svc, "/v1/rooms/room-1/agent-jobs", "alice")
	if !strings.Contains(string(raw), "j-secret") {
		t.Fatalf("a member's job rows miss the section's job: %s", raw)
	}
}
