package room

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// membersOnlyEnv is the setup env with a members-only section "Secret"
// owned by bob (its first member) and a channel #plans in it, with a
// deliverable of #plans and one of the default channel.
type membersOnlyEnv struct {
	*setupEnv
	sec        wire.RoomSection
	plans      wire.RoomChannel
	planArt    string
	generalArt string
}

func newMembersOnlyEnv(t *testing.T) membersOnlyEnv {
	t.Helper()
	e := newSetupEnv(t)
	e.svc.Artifacts = store.NewMemoryArtifacts()
	m := membersOnlyEnv{setupEnv: e}
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "bob", map[string]any{"name": "Secret", "visibility": "members"}, http.StatusOK, &m.sec)
	if m.sec.Visibility != wire.SectionMembers || m.sec.JoinPolicy != wire.SectionJoinInvite || !m.sec.Joined || len(m.sec.Members) != 1 || m.sec.Members[0] != "bob" {
		t.Fatalf("a members-only section's creator is its first member, invite by default: %+v", m.sec)
	}
	m.plans = e.create("bob", wire.ChannelCreateRequest{Name: "plans", Purpose: "the secret plan", SectionID: m.sec.SectionID}, http.StatusOK)
	var art struct {
		Artifact wire.Artifact `json:"artifact"`
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/artifacts", "bob", map[string]any{"kind": "markdown", "title": "plan.md", "content": "secret", "channelId": m.plans.ChannelID}, http.StatusOK, &art)
	m.planArt = art.Artifact.ArtifactID
	e.call(http.MethodPost, "/v1/rooms/room-1/artifacts", "carol", map[string]any{"kind": "markdown", "title": "notes.md", "content": "open", "channelId": e.def}, http.StatusOK, nil)
	return m
}

func names(cs []wire.RoomChannel) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func sectionNames(ss []wire.RoomSection) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

// A member of the room who is not in a members-only section sees nothing
// of it on any room-service path (C11): not the section, not its channels,
// not their deliverables; the room's admin sees the section's name and
// members but no content until they join (C12); its member sees all.
func TestMembersOnlySectionIsHiddenFromNonMembers(t *testing.T) {
	m := newMembersOnlyEnv(t)
	e := m.setupEnv
	plans, sec := m.plans.ChannelID, m.sec.SectionID

	// carol: a member of the room, not of the section.
	list := e.list("carol", "")
	if strings.Contains(strings.Join(names(list.Channels), ","), "plans") || len(list.Sections) != 0 {
		t.Fatalf("a non-member lists %v / %v", names(list.Channels), sectionNames(list.Sections))
	}
	if l := e.list("carol", "?agents=1"); strings.Contains(strings.Join(names(l.Channels), ","), "plans") {
		t.Fatalf("an agent working for a non-member lists %v", names(l.Channels))
	}
	var dir wire.ChannelDirectory
	e.call(http.MethodGet, "/v1/rooms/room-1/channel-directory", "carol", nil, http.StatusOK, &dir)
	for _, c := range dir.Channels {
		if c.ChannelID == plans {
			t.Fatal("the directory lists a members-only channel to a non-member")
		}
	}
	if len(dir.Sections) != 0 {
		t.Fatalf("the directory names the section to a non-member: %+v", dir.Sections)
	}
	e.call(http.MethodPut, "/v1/rooms/room-1/channels/"+plans+"/follow", "carol", map[string]any{}, http.StatusNotFound, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+plans, "carol", map[string]any{"purpose": "x"}, http.StatusNotFound, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1/sections/"+sec, "carol", map[string]any{"name": "x"}, http.StatusNotFound, nil)
	e.create("carol", wire.ChannelCreateRequest{Name: "sneak", SectionID: sec}, http.StatusBadRequest)
	// Deliverables of the section's channel (C13).
	var arts wire.ArtifactList
	e.call(http.MethodGet, "/v1/rooms/room-1/artifacts", "carol", nil, http.StatusOK, &arts)
	if len(arts.Artifacts) != 1 || arts.Artifacts[0].Title != "notes.md" {
		t.Fatalf("a non-member's artifacts = %+v", arts.Artifacts)
	}
	for _, p := range []string{"", "/versions/1", "/versions/1/content", "/state"} {
		e.call(http.MethodGet, "/v1/rooms/room-1/artifacts/"+m.planArt+p, "carol", nil, http.StatusNotFound, nil)
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/artifacts/"+m.planArt+"/versions", "carol", map[string]any{"content": "x", "baseVersion": 1}, http.StatusNotFound, nil)

	// erin: the room's admin, not joined: the section and its members, no content.
	list = e.list("erin", "")
	if strings.Contains(strings.Join(names(list.Channels), ","), "plans") {
		t.Fatalf("an admin who has not joined lists %v", names(list.Channels))
	}
	if len(list.Sections) != 1 || list.Sections[0].Joined || len(list.Sections[0].Members) != 1 {
		t.Fatalf("an admin sees the section's name and members, not joined: %+v", list.Sections)
	}
	e.call(http.MethodGet, "/v1/rooms/room-1/artifacts/"+m.planArt, "erin", nil, http.StatusNotFound, nil)

	// bob: a member: everything.
	list = e.list("bob", "")
	if !strings.Contains(strings.Join(names(list.Channels), ","), "plans") || len(list.Sections) != 1 || !list.Sections[0].Joined {
		t.Fatalf("a member lists %v / %+v", names(list.Channels), list.Sections)
	}
	e.call(http.MethodGet, "/v1/rooms/room-1/artifacts/"+m.planArt+"/versions/1/content", "bob", nil, http.StatusOK, nil)
	e.call(http.MethodGet, "/v1/rooms/room-1/artifacts", "bob", nil, http.StatusOK, &arts)
	if len(arts.Artifacts) != 2 {
		t.Fatalf("a member's artifacts = %+v", arts.Artifacts)
	}
}

// Joining (C17): an "ask" section takes a request its owners approve or
// decline; an "open" one is joined at once; the room's admin joins at once,
// announced to the section and audited (C12); leaving re-scopes at once; a
// visitor joins nothing; the only owner cannot leave.
func TestSectionMembership(t *testing.T) {
	m := newMembersOnlyEnv(t)
	e := m.setupEnv
	audit := store.NewMemoryExtensionAudit()
	e.svc.Audit = audit
	base := "/v1/rooms/room-1/sections/" + m.sec.SectionID
	var reply wire.SectionJoinReply
	// An invite section is not shown: joining it is not found.
	e.call(http.MethodPost, base+"/join", "carol", nil, http.StatusNotFound, nil)
	// Shown for joining by asking: its name, never its channels or members.
	e.call(http.MethodPatch, base, "bob", map[string]any{"joinPolicy": "ask"}, http.StatusOK, nil)
	if l := e.list("carol", ""); len(l.Sections) != 1 || l.Sections[0].Joined || len(l.Sections[0].Members) != 0 || strings.Contains(strings.Join(names(l.Channels), ","), "plans") {
		t.Fatalf("an ask section shows its name alone: %+v / %v", l.Sections, names(l.Channels))
	}
	e.call(http.MethodPost, base+"/join", "carol", nil, http.StatusOK, &reply)
	if reply.Joined || !reply.Requested {
		t.Fatalf("an ask section takes a request: %+v", reply)
	}
	if l := e.list("carol", ""); strings.Contains(strings.Join(names(l.Channels), ","), "plans") {
		t.Fatal("a request is no membership")
	}
	var reqs wire.SectionRequests
	e.call(http.MethodGet, base+"/requests", "carol", nil, http.StatusForbidden, nil)
	e.call(http.MethodGet, base+"/requests", "bob", nil, http.StatusOK, &reqs)
	if len(reqs.Requests) != 1 || reqs.Requests[0].UserID != "carol" {
		t.Fatalf("requests = %+v", reqs.Requests)
	}
	e.call(http.MethodPost, base+"/members", "carol", map[string]any{"userId": "carol"}, http.StatusForbidden, nil)
	e.call(http.MethodPost, base+"/members", "bob", map[string]any{"userId": "vic"}, http.StatusBadRequest, nil)
	e.call(http.MethodPost, base+"/members", "bob", map[string]any{"userId": "carol"}, http.StatusOK, nil)
	if l := e.list("carol", ""); !strings.Contains(strings.Join(names(l.Channels), ","), "plans") {
		t.Fatalf("an approved member lists %v", names(l.Channels))
	}
	e.call(http.MethodGet, base+"/requests", "bob", nil, http.StatusOK, &reqs)
	if len(reqs.Requests) != 0 {
		t.Fatalf("approving closes the request: %+v", reqs.Requests)
	}
	// Leaving re-scopes at once and is announced.
	e.call(http.MethodDelete, base+"/join", "carol", nil, http.StatusNoContent, nil)
	if l := e.list("carol", ""); strings.Contains(strings.Join(names(l.Channels), ","), "plans") {
		t.Fatal("a member who left still lists the section's channel")
	}
	// A declined request.
	e.call(http.MethodPost, base+"/join", "carol", nil, http.StatusOK, nil)
	e.call(http.MethodDelete, base+"/requests/carol", "bob", nil, http.StatusNoContent, nil)
	e.call(http.MethodDelete, base+"/requests/carol", "bob", nil, http.StatusNotFound, nil)
	// The room's admin joins at once: announced (admin) and audited.
	e.call(http.MethodPost, base+"/join", "erin", nil, http.StatusOK, &reply)
	if !reply.Joined {
		t.Fatalf("an admin joins at once: %+v", reply)
	}
	var announced bool
	for _, ev := range e.pub.evs {
		if p, ok := ev.Payload.(wire.SectionMembershipPayload); ok && ev.Kind == wire.EventSectionMembership && p.UserID == "erin" && p.Action == "joined" && p.Admin {
			announced = true
		}
	}
	if !announced {
		t.Fatal("an admin's join is announced to the section, marked admin")
	}
	recs, err := audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true})
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	for _, r := range recs {
		if r.Kind == wire.AuditKindSection && r.Action == "joined" && strings.Contains(r.Detail, "member=erin") {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("an admin's join is audited: %+v", recs)
	}
	e.call(http.MethodGet, "/v1/rooms/room-1/artifacts/"+m.planArt, "erin", nil, http.StatusOK, nil)
	// An open section: joined at once; a visitor joins nothing.
	e.call(http.MethodPatch, base, "bob", map[string]any{"joinPolicy": "open"}, http.StatusOK, nil)
	e.call(http.MethodPost, base+"/join", "carol", nil, http.StatusOK, &reply)
	if !reply.Joined {
		t.Fatalf("an open section is joined at once: %+v", reply)
	}
	e.call(http.MethodPost, base+"/join", "vic", nil, http.StatusForbidden, nil)
	// The only owner cannot leave; an owner is removed by changing owners.
	e.call(http.MethodDelete, base+"/join", "bob", nil, http.StatusConflict, nil)
	e.call(http.MethodDelete, base+"/members/bob", "erin", nil, http.StatusConflict, nil)
	e.call(http.MethodDelete, base+"/members/carol", "erin", nil, http.StatusNoContent, nil)
	e.call(http.MethodDelete, base+"/members/carol", "erin", nil, http.StatusNotFound, nil)
	// An open section has no membership routes.
	var open wire.RoomSection
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "bob", map[string]any{"name": "Open"}, http.StatusOK, &open)
	e.call(http.MethodPost, "/v1/rooms/room-1/sections/"+open.SectionID+"/join", "carol", nil, http.StatusConflict, nil)
}

// The room's export (a handoff package) holds what its exporter reads: an
// admin who has not joined a members-only section exports none of it (C12).
func TestExportLeavesOutUnjoinedSections(t *testing.T) {
	m := newMembersOnlyEnv(t)
	e := m.setupEnv
	access, err := e.svc.memberAccess(dctx(), "room-1", channelCaller{user: "erin", role: store.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	ch, _, _ := e.svc.Channels.GetChannel(dctx(), "room-1", m.plans.ChannelID)
	if access.MayReadChannel(ch) {
		t.Fatal("the exporter's access reads an unjoined section's channel")
	}
}
