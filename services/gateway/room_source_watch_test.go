package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// notices are the admins' queued notices whose title holds part.
func (g *sourcesGW) notices(part string) []wire.NotificationPayload {
	g.t.Helper()
	var out []wire.NotificationPayload
	for _, p := range queued(g.t, g.outbox) {
		if strings.Contains(p.Title, part) {
			out = append(out, p)
		}
	}
	return out
}

// detail is the console's view of the reading room's ask.
func (g *sourcesGW) detail() wire.RoomSourceDetail {
	g.t.Helper()
	r := g.call(http.MethodGet, "/v1/tenant/room-sources/"+g.roomID+"/runs", nil, g.orgAdmin())
	var d wire.RoomSourceDetail
	if r.status != http.StatusOK || json.Unmarshal(r.body, &d) != nil {
		g.t.Fatalf("the ask's detail = %d %s", r.status, r.body)
	}
	return d
}

// sourceActions are the source audit's actions since the last take.
func (g *sourcesGW) sourceActions() []string {
	var out []string
	for _, rec := range g.sink.take() {
		if rec.Kind == wire.AuditKindSource && rec.Action != "read" {
			out = append(out, rec.Action+":"+rec.Actor)
		}
	}
	return out
}

// task creates subject's daily AI task in the reading room through the
// edge, answering its id.
func (g *sourcesGW) task(subject, text string) string {
	g.t.Helper()
	r := g.call(http.MethodPost, "/v1/rooms/"+g.roomID+"/schedules", map[string]any{"text": text, "recipient": map[string]any{"kind": "ai"},
		"recurrence": map[string]any{"kind": "daily", "time": "09:00"}, "timezone": "UTC", "enabled": false}, g.as(subject))
	if r.status != http.StatusCreated {
		g.t.Fatalf("create a task = %d %s", r.status, r.body)
	}
	return fmt.Sprint(r.json()["scheduleId"])
}

// A grant needs a private reading room and is pinned to its owner, whom
// the admin names; the console's ask lists the room's members with their
// roles, and the grant's reader shows in the console and in the covered
// rooms' note. Only the grantee's schedules read: a room admin's is
// refused. The room made shared withdraws the grant; a member who joins
// after the grant is told to the admins (the grant holds); a transfer of
// the room pauses it, back to waiting. Each is audited and told.
func TestRoomSourceGrantPinnedToAPrivateRoomsOwner(t *testing.T) {
	g := newSourcesGW(t)
	runs := `{"source":"runs"}`
	shared, private := store.VisibilityShared, store.VisibilityPrivate
	if err := g.b.Room.UpdateRoom(g.ctx(), g.roomID, store.RoomPatch{Visibility: &shared}); err != nil {
		t.Fatal(err)
	}
	if r := g.decideAs(wire.RoomSourceScopeAll, "maya"); r.status != http.StatusConflict || !strings.Contains(string(r.body), "private") {
		t.Fatalf("a grant to a shared room = %d %s", r.status, r.body)
	}
	if d := g.detail(); !d.Shared || d.Owner != "maya" {
		t.Fatalf("the shared room's ask = %+v", d)
	}
	if err := g.b.Room.UpdateRoom(g.ctx(), g.roomID, store.RoomPatch{Visibility: &private}); err != nil {
		t.Fatal(err)
	}
	if r := g.decideAs(wire.RoomSourceScopeAll, "jack"); r.status != http.StatusConflict || !strings.Contains(string(r.body), "owner is maya") {
		t.Fatalf("a grant read by someone not the owner = %d %s", r.status, r.body)
	}
	if err := g.b.Room.AddMember(g.ctx(), store.Membership{RoomID: g.roomID, UserID: "jack", Role: store.RoleAdmin, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	g.sink.take()
	g.decide(wire.RoomSourceScopeAll)
	d := g.detail()
	members := map[string]string{}
	for _, m := range d.Members {
		members[m.UserID] = m.Role
	}
	if d.Shared || d.Owner != "maya" || d.Grant.Grantee != "maya" || members["maya"] != "owner" || members["jack"] != "admin" || len(members) != 2 {
		t.Fatalf("the granted ask = %+v", d)
	}
	if got := g.sourceActions(); !slices.Equal(got, []string{"grant:olga"}) {
		t.Fatalf("audit = %v", got)
	}
	r := g.call(http.MethodGet, "/v1/rooms/shared/sources", nil, g.as("jack"))
	var info wire.RoomSourcesInfo
	if json.Unmarshal(r.body, &info) != nil || len(info.ReviewedBy) != 1 || info.ReviewedBy[0].Grantee != "maya" {
		t.Fatalf("the covered room's note = %s", r.body)
	}

	// Only the grantee's schedules read: a room admin's is refused.
	if recs, _ := g.page(g.read("maya", runs)[0]); len(recs) != 3 {
		t.Fatalf("the grantee reads %d records", len(recs))
	}
	if out := g.read("jack", runs); !strings.Contains(out[0], "read by maya") {
		t.Fatalf("a room admin's schedule read = %q", out)
	}

	// A member added after the grant is told to the admins; the grant holds.
	if r := g.call(http.MethodPost, "/v1/rooms/"+g.roomID+"/members", map[string]string{"userId": "nora", "role": "member"}, g.as("maya")); r.status != http.StatusOK {
		t.Fatalf("add nora = %d %s", r.status, r.body)
	}
	joined := g.notices("nora joined a room that reads")
	if len(joined) != 1 || !slices.Equal(recipientsOf(joined[0]), []string{"olga"}) || !strings.Contains(joined[0].Text, "read by maya") || !strings.HasSuffix(joined[0].URL, "/#/org") {
		t.Fatalf("the admins' notice of a new member = %+v", joined)
	}
	if recs, _ := g.page(g.read("maya", runs)[0]); len(recs) != 3 {
		t.Fatalf("after a member joined the grantee reads %d records", len(recs))
	}
	if len(g.notices("nora joined")) != 1 {
		t.Fatal("a member was told twice")
	}
	// One who redeems an invite (the room is in the answer) is told too.
	inv := g.call(http.MethodPost, "/v1/rooms/"+g.roomID+"/invites", map[string]any{"role": "member", "capacity": 1}, g.as("maya"))
	if inv.status != http.StatusOK {
		t.Fatalf("invite = %d %s", inv.status, inv.body)
	}
	if r := g.call(http.MethodPost, "/v1/invites/"+fmt.Sprint(inv.json()["code"])+"/redeem", nil, g.as("ivy")); r.status != http.StatusOK || r.json()["roomId"] != g.roomID {
		t.Fatalf("redeem = %d %s", r.status, r.body)
	}
	if len(g.notices("ivy joined a room that reads")) != 1 {
		t.Fatal("the admins were not told of a member who redeemed an invite")
	}

	// The room made shared through the edge withdraws the grant.
	if r := g.call(http.MethodPatch, "/v1/rooms/"+g.roomID, map[string]string{"visibility": "shared"}, g.as("maya")); r.status != http.StatusOK {
		t.Fatalf("make the room shared = %d %s", r.status, r.body)
	}
	if d := g.detail(); d.Grant.Scope != "" || d.Grant.Grantee != "" {
		t.Fatalf("a shared room kept its grant: %+v", d.Grant)
	}
	if got := g.sourceActions(); !slices.Equal(got, []string{"withdraw:maya"}) {
		t.Fatalf("audit = %v", got)
	}
	if len(g.notices("is withdrawn")) != 1 {
		t.Fatal("the admins were not told of the withdrawal")
	}
	if out := g.read("maya", runs); !strings.Contains(out[0], "has not granted") {
		t.Fatalf("a read after the room turned shared = %q", out)
	}

	// Private again and granted again: a transfer pauses it.
	if r := g.call(http.MethodPatch, "/v1/rooms/"+g.roomID, map[string]string{"visibility": "private"}, g.as("maya")); r.status != http.StatusOK {
		t.Fatalf("make the room private = %d %s", r.status, r.body)
	}
	g.decide(wire.RoomSourceScopeAll)
	g.sink.take()
	if r := g.call(http.MethodPost, "/v1/rooms/"+g.roomID+"/transfer", map[string]string{"userId": "jack"}, g.as("maya")); r.status != http.StatusOK {
		t.Fatalf("transfer the room = %d %s", r.status, r.body)
	}
	if d := g.detail(); d.Grant.Scope != "" || d.Owner != "jack" {
		t.Fatalf("a transferred room kept its grant: %+v", d)
	}
	if got := g.sourceActions(); !slices.Equal(got, []string{"pause:maya"}) {
		t.Fatalf("audit = %v", got)
	}
	if paused := g.notices("waits for you again"); len(paused) != 1 || !strings.Contains(paused[0].Text, "jack does") {
		t.Fatalf("the admins' notice of the pause = %+v", paused)
	}
	for _, owner := range []string{"maya", "jack"} {
		if out := g.read(owner, runs); !strings.Contains(out[0], "has not granted") {
			t.Fatalf("%s's read after the transfer = %q", owner, out)
		}
	}
}

// A grant checks itself before every read: a room made shared or
// transferred where the edge did not see it reads nothing.
func TestReadRecordsWatchesTheGrantFirst(t *testing.T) {
	g := newSourcesGW(t)
	g.decide(wire.RoomSourceScopeAll)
	shared := store.VisibilityShared
	if err := g.b.Room.UpdateRoom(g.ctx(), g.roomID, store.RoomPatch{Visibility: &shared}); err != nil {
		t.Fatal(err)
	}
	if out := g.read("maya", `{"source":"runs"}`); !strings.Contains(out[0], "has not granted") {
		t.Fatalf("a read from a room turned shared = %q", out)
	}
	if got := g.sourceActions(); !slices.Contains(got, "withdraw:"+sourceWatcher) {
		t.Fatalf("audit = %v", got)
	}
}

// The grantee's AI tasks are consented as they are at the grant. A task
// changed or added since is told to the admins with explain-source-use's
// explanation of the change against the purpose (the words before and
// after are its input), and the console shows it until an admin
// acknowledges; another member's task (it reads nothing) is not watched.
func TestRoomSourceTaskChangedSinceGrant(t *testing.T) {
	g := newSourcesGW(t)
	review := g.task("maya", "Review yesterday's conversations and post what failed.")
	g.task("jack", "Summarize the room every day.")
	g.decide(wire.RoomSourceScopeAll)
	if d := g.detail(); len(d.Tasks) != 1 || d.Tasks[0].ScheduleID != review || d.Grant.Changed != 0 || !strings.Contains(d.ExplainInput, "Review yesterday") ||
		strings.Contains(d.ExplainInput, "Summarize the room") || strings.Contains(d.ExplainInput, "before") {
		t.Fatalf("the granted ask = %+v", d)
	}

	g.model.seen, g.model.turns = nil, []any{map[string]any{"content": `{"summary":"Ranks each member by failures.","uses":["Ranks members"],"mismatch":"It ranks people, which the purpose does not say."}`}}
	if r := g.call(http.MethodPatch, "/v1/rooms/"+g.roomID+"/schedules/"+review, map[string]string{"text": "Rank every member by how often their conversations failed."}, g.as("maya")); r.status != http.StatusOK {
		t.Fatalf("change the task = %d %s", r.status, r.body)
	}
	if len(g.model.seen) != 1 {
		t.Fatalf("the explanation ran %d times", len(g.model.seen))
	}
	asked := fmt.Sprint(g.model.seen[0])
	for _, want := range []string{"Judge whether conversations reached their goal.", `"before":"Review yesterday's conversations and post what failed."`, "Rank every member"} {
		if !strings.Contains(asked, want) {
			t.Fatalf("the explanation's input lacks %q: %s", want, asked)
		}
	}
	changed := g.notices("maya changed a task that reads")
	if len(changed) != 1 || !slices.Equal(recipientsOf(changed[0]), []string{"olga"}) || !strings.Contains(changed[0].Text, "Before: Review yesterday") ||
		!strings.Contains(changed[0].Text, "Beyond the stated purpose (AI): It ranks people") {
		t.Fatalf("the admins' notice of a changed task = %+v", changed)
	}
	d := g.detail()
	if d.Grant.Changed != 1 || d.Grant.Scope != wire.RoomSourceScopeAll || len(d.Drift) != 1 || d.Drift[0].Before != "Review yesterday's conversations and post what failed." ||
		d.Drift[0].After != "Rank every member by how often their conversations failed." || d.Drift[0].Mismatch == "" || !strings.Contains(d.ExplainInput, `"before"`) {
		t.Fatalf("the console after the change = %+v", d)
	}
	// Watched again (a read), the change is not told, nor explained, twice.
	g.read("maya", `{"source":"runs"}`)
	explained := 0
	for _, call := range g.model.seen {
		if strings.Contains(fmt.Sprint(call), "decide whether a room may read records") {
			explained++
		}
	}
	if len(g.notices("changed a task")) != 1 || explained != 1 {
		t.Fatalf("a change was told %d times, explained %d times", len(g.notices("changed a task")), explained)
	}
	// Another member's task reads nothing: not watched.
	g.task("jack", "Post a joke.")
	if len(g.notices("jack added")) != 0 {
		t.Fatal("a task that reads nothing was told")
	}

	if r := g.call(http.MethodPost, "/v1/tenant/room-sources/"+g.roomID+"/runs/ack", nil, g.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a member acknowledges = %d", r.status)
	}
	g.sink.take()
	if r := g.call(http.MethodPost, "/v1/tenant/room-sources/"+g.roomID+"/runs/ack", nil, g.orgAdmin()); r.status != http.StatusOK {
		t.Fatalf("acknowledge = %d %s", r.status, r.body)
	}
	if d := g.detail(); d.Grant.Changed != 0 || len(d.Drift) != 0 {
		t.Fatalf("the console after the acknowledgment = %+v", d)
	}
	if got := g.sourceActions(); !slices.Equal(got, []string{"acknowledge:olga"}) {
		t.Fatalf("audit = %v", got)
	}

	// A new task of the grantee is told too; AI off, the notice says so.
	off := true
	if _, err := g.b.Tenant.SetTenantPolicy(g.ctx(), g.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	g.task("maya", "Email the failures to the whole company.")
	added := g.notices("maya added a task that reads")
	if len(added) != 1 || !strings.Contains(added[0].Text, "No AI explanation") {
		t.Fatalf("the admins' notice of a new task = %+v", added)
	}
	if d := g.detail(); d.Grant.Changed != 1 || d.Drift[0].Before != "" {
		t.Fatalf("the console after a new task = %+v", d)
	}
}
