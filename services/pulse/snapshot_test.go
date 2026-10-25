package pulsesvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func snapshotFor(t *testing.T, srv *Server, tnt string) (*httptest.ResponseRecorder, Snapshot) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/pulse/snapshot", nil)
	if tnt != "" {
		req = req.WithContext(tenant.NewContext(req.Context(), tnt))
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var snap Snapshot
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
			t.Fatal(err)
		}
	}
	return rec, snap
}

// People counts below k are withheld and reported as suppressed, never shown as
// small numbers. Agents and totals are not people counts and are shown.
func TestSnapshotWithholdsSmallPeopleCounts(t *testing.T) {
	rdb := testRedis(t)
	tnt := uniq("tenant")
	now := time.Now()
	state := NewLive(rdb)
	rows := newFakeRows()
	minute := now.UnixMilli() - now.UnixMilli()%60_000
	if err := rows.AddPulseMinutes(context.Background(), []store.PulseMinuteDelta{
		{Tenant: tnt, MinuteMs: minute, Metric: MetricReplies, Dim: "codex", Value: 4},
		{Tenant: tnt, MinuteMs: minute, Metric: MetricRepliesReported, Dim: "codex", Value: 3},
	}, minute); err != nil {
		t.Fatal(err)
	}
	srv := &Server{State: state, Rows: rows, Now: func() time.Time { return now }}

	// Three people online: below k, so the count is withheld.
	for i := 0; i < 3; i++ {
		member := pulse.Digest(tnt + string(rune('a'+i)))
		emitAll(t, uniq("pulse:test"), pulse.Event{ID: "s" + member, Tenant: tnt, Kind: pulse.KindPresenceSeen, At: now.UnixMilli()}.With(pulse.Hash("member", member)))
	}
	rec, snap := snapshotFor(t, srv, tnt)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if snap.Live.PeopleOnline != nil || !snap.Live.PeopleSuppressed {
		t.Fatalf("three people must be withheld: %+v", snap.Live)
	}
	if snap.Today.Replies != 4 || snap.Today.Coverage == nil || *snap.Today.Coverage != 0.75 {
		t.Fatalf("today = %+v", snap.Today)
	}
	// One lane per core, with its own coverage (the token pump's lanes).
	if lane := snap.Today.RepliesByCore["codex"]; lane.Replies != 4 || lane.Reported != 3 {
		t.Fatalf("codex lane = %+v, want 4 replies and 3 reported", lane)
	}
	if snap.DataAsOfMs != minute {
		t.Fatalf("dataAsOfMs = %d, want the rollup's last write %d", snap.DataAsOfMs, minute)
	}
	if snap.Week.ActivePeople != nil || !snap.Week.ActivePeopleSuppressed {
		t.Fatalf("week must be withheld with no active people: %+v", snap.Week)
	}
}

func TestSnapshotRefusesARequestWithoutATenant(t *testing.T) {
	srv := &Server{State: NewLive(nil), Rows: newFakeRows(), Now: time.Now}
	rec, _ := snapshotFor(t, srv, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: a snapshot is always for one tenant", rec.Code)
	}
}
