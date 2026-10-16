package roomlog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// .room/groups.md lists the group chats the room's members connected, by the
// name an app's notify or a scheduled task sends to, never the chat's address.
// It is written once the room has one and every turn after, so it never goes
// stale; a failed read says so.
func TestGroupsGolden(t *testing.T) {
	fetch := func(_ context.Context, path string) ([]byte, error) {
		if path != "/v1/rooms/r1/groups" {
			t.Errorf("read %s", path)
		}
		return []byte(`{"groups":[{"name":"weekly report","notifier":"corp-notify","connectedBy":"maya","createdAtMs":1}]}`), nil
	}
	groups, err := ReadGroups(context.Background(), fetch, "r1")
	if err != nil || len(groups) != 1 || groups[0].Name != "weekly report" {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	f, write, listed := GroupsUpdate(false, groups, nil)
	if !write || !listed || f.Name != ".room/groups.md" {
		t.Fatalf("a room with a group: write=%v listed=%v name=%s", write, listed, f.Name)
	}
	golden(t, "groups.md", f.Content)
	if !strings.Contains(f.Content, "- group:weekly report (delivered by corp-notify)\n") || strings.Contains(f.Content, "maya") {
		t.Errorf("groups.md names the group and its notifier only:\n%s", f.Content)
	}

	if _, write, listed := GroupsUpdate(false, nil, nil); write || listed {
		t.Fatal("a room without groups gets a groups.md")
	}
	if _, write, _ := GroupsUpdate(false, nil, errors.New("down")); write {
		t.Fatal("an unreadable room that never had groups gets a groups.md")
	}
	if f, write, listed := GroupsUpdate(true, []wire.RoomGroup{}, nil); !write || !listed || !strings.Contains(f.Content, "(no group chats now)") {
		t.Fatalf("groups.md after the last group went is rewritten: %v\n%s", write, f.Content)
	}
	if f, _, _ := GroupsUpdate(true, nil, errors.New("down")); !strings.Contains(f.Content, "could not be read this turn") {
		t.Fatalf("a failed read renders:\n%s", f.Content)
	}
	if f, _, _ := GroupsUpdate(true, groups, errors.New("down")); !strings.Contains(f.Content, "may be out of date") || !strings.Contains(f.Content, "group:weekly report") {
		t.Fatalf("a failed refresh keeps the last names:\n%s", f.Content)
	}
}
