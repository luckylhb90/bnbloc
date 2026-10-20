package gateway

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A member who may post in the room reports what it could not do to the
// organization's admins, linked to the reply; the same note about the same
// reply goes once; nobody else reports the room; an admin who turned room
// reports off gets none, and the member learns it was not queued.
func TestRoomReport(t *testing.T) {
	e, outbox, sink := notifyGW(t)
	e.gw.OrgAdmins = func(tid string) ([]string, error) {
		if tid != e.tenant {
			return nil, nil
		}
		return []string{"ada"}, nil
	}
	path := "/v1/rooms/" + e.roomID + "/report"
	note := wire.RoomReportRequest{Text: "The agent cannot reach the admin console; we need a browser component.", Seq: 2}
	r := e.call(http.MethodPost, path, note, e.as("maya"))
	if r.status != http.StatusOK || r.json()["queued"] != true {
		t.Fatalf("report = %d %s", r.status, r.body)
	}
	got := queued(t, outbox)
	if len(got) != 1 || got[0].Type != wire.NotificationRoomReport || !slices.Equal(recipientsOf(got[0]), []string{"ada"}) ||
		got[0].Text != note.Text || !strings.HasPrefix(got[0].Title, "maya reports") || !strings.Contains(got[0].URL, e.roomID) {
		t.Fatalf("queued = %+v", got)
	}
	if recs := sink.take(); len(recs) != 1 || recs[0].Name != "room-report" || strings.Contains(recs[0].Detail, "console") {
		t.Fatalf("audit = %+v", recs)
	}
	if r := e.call(http.MethodPost, path, note, e.as("maya")); r.status != http.StatusOK || r.json()["queued"] != false || len(queued(t, outbox)) != 1 {
		t.Fatalf("the same report again = %d %s, %d queued", r.status, r.body, len(queued(t, outbox)))
	}
	if r := e.call(http.MethodPost, path, note, e.as("nora")); r.status != http.StatusForbidden {
		t.Fatalf("a non-member's report = %d", r.status)
	}
	if r := e.call(http.MethodPost, path, wire.RoomReportRequest{Text: "  "}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("an empty report = %d", r.status)
	}
	if r := e.call(http.MethodPost, path, wire.RoomReportRequest{Text: strings.Repeat("a", wire.MaxRoomReportText+1)}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("a report too long = %d", r.status)
	}
	prefs := store.DefaultNotificationPrefs()
	prefs.RoomReports = false
	if err := outbox.PutNotificationPrefs(tenant.NewContext(context.Background(), e.tenant), "ada", prefs); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodPost, path, wire.RoomReportRequest{Text: "Another gap.", Seq: 3}, e.as("jack")); r.status != http.StatusOK || r.json()["queued"] != false {
		t.Fatalf("to an admin who turned reports off = %d %s", r.status, r.body)
	}
}
