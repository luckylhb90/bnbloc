package shareagenthttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// borrowCall drives one reserve/record handler as caller in tenantID.
func borrowCall(t *testing.T, svc *Service, tenantID, caller, owner, provider, action string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/rooms/room-1/shared-agents/shared-agent:"+provider+"/authorization/"+action+"?owner="+owner,
		bytes.NewReader(raw))
	r = r.WithContext(tctx(tenantID))
	r.SetPathValue("roomId", "room-1")
	r.SetPathValue("targetId", "shared-agent:"+provider)
	r.Header.Set("X-Bnbloc-User", caller)
	w := httptest.NewRecorder()
	if action == "reserve" {
		svc.handleReserveBorrow(w, r)
	} else {
		svc.handleRecordBorrow(w, r)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// reserveRun reserves one run of owner's provider for borrower, as the owner.
func reserveRun(t *testing.T, svc *Service, tenantID, owner, provider, borrower, channelID string) (int, map[string]any) {
	t.Helper()
	return borrowCall(t, svc, tenantID, owner, owner, provider, "reserve",
		reserveBorrowBody{Borrower: borrower, Model: "gpt-5", ChannelId: channelID})
}

// borrowRun is one whole borrowed run as the owner's bridge performs it:
// reserve, then settle with usage (record body fields). It returns the
// reserve's answer when the run was refused, else the settle's.
func borrowRun(t *testing.T, svc *Service, tenantID, owner, provider, borrower, channelID string, usage map[string]any) (int, map[string]any) {
	t.Helper()
	code, out := reserveRun(t, svc, tenantID, owner, provider, borrower, channelID)
	if code != http.StatusOK || out["reserved"] != true {
		return code, out
	}
	body := map[string]any{"reservationId": out["reservationId"]}
	for k, v := range usage {
		body[k] = v
	}
	return borrowCall(t, svc, tenantID, owner, owner, provider, "record", body)
}

// R8: concurrent borrows reserve against the quota atomically; never more
// runs than the quota are admitted, and every refusal says quota_exhausted.
func TestReserveBorrowConcurrentNeverExceedsQuota(t *testing.T) {
	svc, gs := newService(t)
	quota := 3
	future := int64(1_000_000_000_000)
	if err := gs.PutGrant(dctx(), store.Grant{
		GrantID: roomAuthGrantID(dctx(), "room-1", "alice", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "alice", GranteeUserID: "*",
		RunQuota: &quota, MaxConcurrent: 2, ExpiresAtMs: &future,
	}); err != nil {
		t.Fatal(err)
	}
	var ok, denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "")
			switch {
			case code == http.StatusOK && out["reserved"] == true:
				ok.Add(1)
			case code == http.StatusForbidden && out["denyReason"] == "quota_exhausted":
				denied.Add(1)
			default:
				t.Errorf("reserve = %d %v", code, out)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 || denied.Load() != 17 {
		t.Fatalf("reserved=%d denied=%d, want 3/17", ok.Load(), denied.Load())
	}
	if n := runsUsed(t, gs); n != 3 {
		t.Fatalf("runsUsed = %d, want 3", n)
	}
}

// R8: only the owner reserves; the borrower must be another room member; an
// absent or revoked authorization is refused (audited, nothing counted).
func TestReserveBorrowRefusals(t *testing.T) {
	svc, gs := newService(t)
	seedAuthorizedBorrow(t, gs, tenant.Default)
	if code, _ := borrowCall(t, svc, tenant.Default, "bob", "alice", "codex", "reserve", reserveBorrowBody{Borrower: "jack"}); code != http.StatusForbidden {
		t.Fatalf("non-owner reserve = %d, want 403", code)
	}
	for name, tc := range map[string]struct {
		borrower string
		want     int
	}{
		"missing borrower":    {"", http.StatusBadRequest},
		"owner as borrower":   {"alice", http.StatusBadRequest},
		"non-member borrower": {"mallory", http.StatusForbidden},
	} {
		if code, _ := borrowCall(t, svc, tenant.Default, "alice", "alice", "codex", "reserve", reserveBorrowBody{Borrower: tc.borrower}); code != tc.want {
			t.Errorf("%s: code = %d, want %d", name, code, tc.want)
		}
	}
	if code, out := reserveRun(t, svc, tenant.Default, "alice", "claude", "bob", ""); code != http.StatusForbidden || out["denyReason"] != shareagent.DenyOwnerNotAuthorized {
		t.Fatalf("unauthorized provider = %d %v, want 403 owner_not_authorized", code, out)
	}
	if err := gs.RevokeGrant(dctx(), roomAuthGrantID(dctx(), "room-1", "alice", "codex"), 500); err != nil {
		t.Fatal(err)
	}
	if code, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", ""); code != http.StatusForbidden || out["denyReason"] != shareagent.DenyOwnerNotAuthorized {
		t.Fatalf("revoked authorization = %d %v, want 403 owner_not_authorized", code, out)
	}
	if n := runsUsed(t, gs); n != 0 {
		t.Fatalf("runsUsed = %d after refusals, want 0", n)
	}
	var denies int
	for _, rec := range gs.AuditRecords(dctx(), "room-1", 50, 0) {
		if rec.Decision == "deny" && rec.DenyReason == shareagent.DenyOwnerNotAuthorized {
			denies++
		}
	}
	if denies != 2 {
		t.Fatalf("audited owner_not_authorized denials = %d, want 2", denies)
	}
}

// R8: the settle attaches usage to the reserved run; it is owner-only,
// validates the usage, and applies once.
func TestRecordSettlesReservation(t *testing.T) {
	svc, gs := newService(t)
	seedAuthorizedBorrow(t, gs, tenant.Default)
	code, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "ch_1")
	if code != http.StatusOK || out["reserved"] != true {
		t.Fatalf("reserve = %d %v", code, out)
	}
	rid := out["reservationId"]
	if n := runsUsed(t, gs); n != 1 {
		t.Fatalf("runsUsed after reserve = %d, want 1 (reserved runs count at once)", n)
	}
	settle := func(caller string, body map[string]any) (int, map[string]any) {
		return borrowCall(t, svc, tenant.Default, caller, "alice", "codex", "record", body)
	}
	if code, _ := settle("bob", map[string]any{"reservationId": rid}); code != http.StatusForbidden {
		t.Fatalf("non-owner settle = %d, want 403", code)
	}
	for name, body := range map[string]map[string]any{
		"missing reservation": {},
		"negative cost":       {"reservationId": rid, "cost": -5},
		"absurd cost":         {"reservationId": rid, "cost": 1e12},
		"negative tokens":     {"reservationId": rid, "inputTokens": -1},
	} {
		if code, _ := settle("alice", body); code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", name, code)
		}
	}
	if code, _ := settle("alice", map[string]any{"reservationId": "rsv_unknown"}); code != http.StatusNotFound {
		t.Fatalf("unknown reservation = %d, want 404", code)
	}
	if code, out := settle("alice", map[string]any{"reservationId": rid, "model": "gpt-5.1", "cost": 0.5, "inputTokens": 10, "outputTokens": 2}); code != http.StatusOK || out["recorded"] != true {
		t.Fatalf("settle = %d %v", code, out)
	}
	if code, _ := settle("alice", map[string]any{"reservationId": rid, "cost": 0.5}); code != http.StatusConflict {
		t.Fatalf("second settle = %d, want 409", code)
	}
	g, _ := gs.GetGrant(dctx(), roomAuthGrantID(dctx(), "room-1", "alice", "codex"))
	if g.RunsUsed != 1 || g.CostUsed != 0.5 {
		t.Fatalf("grant runs=%d cost=%v, want 1 / 0.5", g.RunsUsed, g.CostUsed)
	}
	turns := gs.BorrowTurns(dctx(), store.BorrowTurnQuery{})
	if len(turns) != 1 || turns[0].Model != "gpt-5.1" || turns[0].ChannelID != "ch_1" || turns[0].ActorUserID != "bob" ||
		turns[0].Cost == nil || *turns[0].Cost != 0.5 || turns[0].InputTokens == nil || *turns[0].InputTokens != 10 {
		t.Fatalf("recorded turn = %+v", turns)
	}
}

// R8: a reservation that is never settled expires but keeps counting.
func TestRecordAfterReservationExpiry(t *testing.T) {
	svc, gs := newService(t)
	seedAuthorizedBorrow(t, gs, tenant.Default)
	_, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "")
	svc.Now = func() int64 { return 1000 + store.RunReservationTTLms }
	if code, body := borrowCall(t, svc, tenant.Default, "alice", "alice", "codex", "record", map[string]any{"reservationId": out["reservationId"], "cost": 1}); code != http.StatusConflict {
		t.Fatalf("late settle = %d %v, want 409", code, body)
	}
	if n := runsUsed(t, gs); n != 1 {
		t.Fatalf("runsUsed = %d, want 1 (the expired run still counts)", n)
	}
}
