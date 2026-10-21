package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// auditRecorder keeps the audit rows a test reads.
type auditRecorder struct {
	mu   sync.Mutex
	recs []store.ExtAuditRecord
}

func (a *auditRecorder) AppendExtAudit(_ context.Context, rec store.ExtAuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, rec)
	return nil
}

// visitorMembers reads one user as a read-only visitor, the rest as the
// fixture's members.
type visitorMembers struct {
	*schedMembers
	visitor string
}

func (v visitorMembers) Lookup(room, user string) (membership.Member, bool) {
	if user == v.visitor {
		return membership.Member{Role: store.RoleVisitor}, true
	}
	return v.schedMembers.Lookup(room, user)
}

func (v visitorMembers) Fresh(room, user string) (membership.Member, bool) {
	return v.Lookup(room, user)
}

// sendEvent posts a member's event to room as user.
func (f *schedFixture) sendEvent(user, room string, in any) (int, wire.MemberEventReply, string) {
	f.t.Helper()
	code, out := f.do(user, http.MethodPost, "/v1/rooms/"+room+"/events", in)
	var reply wire.MemberEventReply
	_ = json.Unmarshal(out, &reply)
	return code, reply, string(out)
}

// A member sends the room an event: its source is theirs, it reaches only
// that room's tasks taking members' events (and their filter), it is
// audited without its data; a platform type, a visitor, a non-member and
// data over the bound are refused.
func TestMemberEventReachesItsRoom(t *testing.T) {
	f := newSchedFixture(t)
	rec := &auditRecorder{}
	f.svc.Audit = rec
	f.svc.Membership = visitorMembers{schedMembers: f.members, visitor: "vera"}
	body := eventBody("Deploy what was finished.", map[string]any{"kind": "room"}, "bnbloc/member", "dev.finished")
	body["recurrence"].(map[string]any)["filter"] = map[string]any{"data": map[string]any{"branch": "main"}}
	deploy := f.create("alice", body)
	platform := f.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/platform", "platform.deployed"))
	other := f.otherRoomTask()
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/member", "room.*")); code != http.StatusBadRequest {
		t.Fatalf("a member trigger on a platform prefix = %d %s", code, out)
	}

	if code, reply, out := f.sendEvent("bob", "room-1", map[string]any{"type": "dev.finished", "subject": "v1.4.0", "data": map[string]any{"branch": "dev"}}); code != http.StatusAccepted || reply.Triggered != 0 {
		t.Fatalf("a filtered event = %d %s", code, out)
	}
	code, reply, out := f.sendEvent("bob", "room-1", map[string]any{"type": "dev.finished", "subject": "v1.4.0", "data": map[string]any{"branch": "main", "secret": "s3"}})
	if code != http.StatusAccepted || reply.Triggered != 1 || reply.Event.Source != "bnbloc/member/bob" || reply.Event.ID == "" {
		t.Fatalf("send = %d %s", code, out)
	}
	if got := f.get(deploy.ScheduleID); got.Event == nil || got.Event.Subject != "v1.4.0" || got.Event.Source != "bnbloc/member/bob" {
		t.Fatalf("the room's task = %+v", got.Event)
	}
	if f.get(platform.ScheduleID).NextRunAtMs != 0 || f.getIn("room-2", other.ScheduleID).NextRunAtMs != 0 {
		t.Fatal("a member's event reached a platform trigger or another room")
	}
	rec.mu.Lock()
	if len(rec.recs) != 2 || rec.recs[1].Kind != wire.AuditKindEvent || rec.recs[1].Name != "room-1" || rec.recs[1].Actor != "bob" ||
		!strings.Contains(rec.recs[1].Detail, "dev.finished") || strings.Contains(rec.recs[1].Detail, "s3") {
		t.Fatalf("audit = %+v", rec.recs)
	}
	rec.mu.Unlock()

	for name, c := range map[string]struct {
		user, room string
		body       any
		want       int
	}{
		"a platform type":  {"bob", "room-1", map[string]any{"type": "platform.deployed"}, http.StatusBadRequest},
		"a room type":      {"bob", "room-1", map[string]any{"type": "room.file.added"}, http.StatusBadRequest},
		"a component type": {"bob", "room-1", map[string]any{"type": "component.published"}, http.StatusBadRequest},
		"no type":          {"bob", "room-1", map[string]any{"subject": "x"}, http.StatusBadRequest},
		"a visitor":        {"vera", "room-1", map[string]any{"type": "dev.finished"}, http.StatusForbidden},
		"a non-member":     {"mallory", "room-1", map[string]any{"type": "dev.finished"}, http.StatusForbidden},
		"data too large":   {"bob", "room-1", map[string]any{"type": "dev.finished", "data": map[string]any{"x": strings.Repeat("a", wire.MaxEventDataBytes)}}, http.StatusRequestEntityTooLarge},
	} {
		if code, _, out := f.sendEvent(c.user, c.room, c.body); code != c.want {
			t.Errorf("%s = %d %s, want %d", name, code, out, c.want)
		}
	}
}

// A member's events spend the task's rate limit: past it they are dropped
// and counted.
func TestMemberEventRateLimit(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/member", "dev.*"))
	dropped := 0
	for i := 0; i < int(taskEventBurst)+2; i++ {
		_, reply, _ := f.sendEvent("bob", "room-1", map[string]any{"type": "dev.finished"})
		dropped += reply.Dropped
	}
	if dropped != 2 || f.get(sc.ScheduleID).DroppedEvents != 2 {
		t.Fatalf("dropped %d (counted %d), want 2", dropped, f.get(sc.ScheduleID).DroppedEvents)
	}
}
