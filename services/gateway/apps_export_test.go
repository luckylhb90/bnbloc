package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// recordedAudit collects the audit rows the room service writes.
type recordedAudit struct {
	mu   sync.Mutex
	recs []store.ExtAuditRecord
}

func (a *recordedAudit) AppendExtAudit(_ context.Context, rec store.ExtAuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, rec)
	return nil
}

func (a *recordedAudit) take() []store.ExtAuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.recs
	a.recs = nil
	return out
}

// End to end: the room entry's export is the owners' (a member's session or
// the job's agent), streamed from the room service and audited once; an
// agent's find through its job writes one audit row, a member's own read
// through a site ticket none.
func TestAppExportAndAgentReadsAudited(t *testing.T) {
	e := newAppGW(t)
	sink := &recordedAudit{}
	e.room.Audit = sink
	jack := e.ticketBase("jack", e.appA)
	for _, choice := range []string{"A", "B"} {
		if r := e.call(http.MethodPost, jack+"~app/c/votes", map[string]any{"doc": map[string]any{"choice": choice}}); r.status != http.StatusCreated {
			t.Fatalf("insert = %d %s", r.status, r.body)
		}
	}
	if r := e.call(http.MethodGet, jack+"~app/c/votes", nil); r.status != http.StatusOK || len(r.docs()) != 2 {
		t.Fatalf("member find = %d %s", r.status, r.body)
	}
	if recs := sink.take(); len(recs) != 0 {
		t.Fatalf("a member's own reads through a ticket were audited: %+v", recs)
	}

	room := "/v1/rooms/" + e.roomID + "/apps/" + e.appA
	r := e.call(http.MethodGet, room+"/export?collection=votes&format=csv", nil, "Authorization: Bearer "+e.session("maya"))
	if r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/csv") || strings.Count(string(r.body), "\r\n") != 3 {
		t.Fatalf("csv export = %d %q", r.status, r.body)
	}
	if recs := sink.take(); len(recs) != 1 || recs[0].Kind != wire.AuditKindApp || recs[0].Action != "export" || recs[0].Actor != "maya" ||
		!strings.Contains(recs[0].Detail, "rows=2") || !strings.Contains(recs[0].Detail, "format=csv") {
		t.Fatalf("export audit = %+v", recs)
	}
	var out struct {
		Collections map[string][]map[string]any `json:"collections"`
	}
	r = e.call(http.MethodGet, room+"/export", nil, "Authorization: Bearer "+e.session("jack"))
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil || len(out.Collections["votes"]) != 2 {
		t.Fatalf("json export = %d %s", r.status, r.body)
	}
	sink.take()
	// Someone outside the room has no export.
	if r := e.call(http.MethodGet, room+"/export", nil, "Authorization: Bearer "+e.session("zed")); r.status != http.StatusForbidden && r.status != http.StatusNotFound {
		t.Fatalf("a stranger's export = %d %s", r.status, r.body)
	}
	if recs := sink.take(); len(recs) != 0 {
		t.Fatalf("a refused export was audited: %+v", recs)
	}

	// The job's agent: one row per call, not per document.
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodGet, room+"/c/votes?job=job-1", nil, "Authorization: Bearer "+e.session("jack")); r.status != http.StatusOK || len(r.docs()) != 2 {
		t.Fatalf("agent find = %d %s", r.status, r.body)
	}
	recs := sink.take()
	if len(recs) != 1 || recs[0].Action != "find" || recs[0].Actor != "jack" || !strings.Contains(recs[0].Detail, "agent=jack/codex") || !strings.Contains(recs[0].Detail, "rows=2") {
		t.Fatalf("agent find audit = %+v", recs)
	}
}
