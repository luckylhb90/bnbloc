package room

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
