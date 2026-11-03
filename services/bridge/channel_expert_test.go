package bridge

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A channel wearing an expert of its own runs in a workspace of its own on a
// core that writes instructions into the workspace, and shares the agent's
// workspace on a core that gives each session its own (channels PRD C9, E6).
// Two channels of one agent differ only in the expert: the slot tells them
// apart only where the expert would be overwritten.
func TestChannelExpertWorkspaceApart(t *testing.T) {
	drive := func(channel, expertChannel string, perSession bool) *drive {
		d := &drive{core: "acme", content: &jobContent{roomExpert: &wire.ContentRef{ComponentID: "cc_x"}, expertChannel: expertChannel}}
		d.job.RoomID, d.job.Provider, d.job.ChannelID = "room-1", "codex", channel
		d.caps = driveCaps{reachable: true, profile: true, perSession: perSession}
		return d
	}
	review := drive("ch-review", "ch-review", false)
	general := drive("ch-general", "", false)
	if review.key() == general.key() {
		t.Fatal("a channel with its own expert shares the agent's workspace on a core that writes instructions into it")
	}
	if review.key().seatAgentID() == general.key().seatAgentID() {
		t.Fatal("the apart slot's sandbox space and deliverable ids are the shared slot's")
	}
	if general.key() != drive("ch-other", "", false).key() {
		t.Fatal("two channels wearing the room's expert no longer share a workspace")
	}
	if drive("ch-review", "ch-review", true).key() != general.key() {
		t.Fatal("a profilePerSession core's channel expert left the shared workspace")
	}
	if review.sessionKey().channel != "ch-review" {
		t.Fatal("the apart slot lost its channel session")
	}
}
