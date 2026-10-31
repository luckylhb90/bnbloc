package room

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestJoinsAnnounced: a person gains a membership only through addMember or
// redeemInvite (room_create.go), which announce member_joined; a handler
// writing the membership itself has to remember the announcement, and one
// (joining a shared room) re-implemented the agent-host conversion besides.
// TestJoinsPublishMemberJoined covers the behaviour.
func TestJoinsAnnounced(t *testing.T) {
	write := regexp.MustCompile(`\.Store\.(AddMember|RedeemInvite)\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "room_create.go" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if write.Match(raw) {
			t.Errorf("%s writes a membership itself; join through addMember / redeemInvite", f)
		}
	}
}

// TestMembershipWritersAnnounce: across the repository a person's room
// membership is written only where the write is announced (member_joined):
// this service's room_create.go, and org sync (its Joined hook). A member
// stream follows its member's rooms by those announcements, so a writer
// that does not announce leaves the member's tabs without the room.
func TestMembershipWritersAnnounce(t *testing.T) {
	write := regexp.MustCompile(`\.(AddMember|RedeemInvite|CreateRoomWithOwner\w*)\(`)
	allowed := map[string]*regexp.Regexp{
		"services/room/room_create.go": regexp.MustCompile(`publishMemberJoined\(`),
		"internal/orgsync/orgsync.go":  regexp.MustCompile(`s\.Joined\(`),
	}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "core", "cmd"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/store/") || !write.Match(src) {
			return
		}
		announce, ok := allowed[rel]
		switch {
		case !ok:
			t.Errorf("%s writes a room membership; join through bnbloc-room (addMember, redeemInvite) or announce member_joined as org sync does", rel)
		case !announce.Match(src):
			t.Errorf("%s writes a room membership and no longer announces it", rel)
		}
	})
}
