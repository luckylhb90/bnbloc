package shareagenthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func costOf(v float64) *float64 { return &v }

// billingService is newService plus a quota request store.
func billingService(t *testing.T) (*Service, *store.MemoryGrant) {
	t.Helper()
	svc, gs := newService(t, "")
	svc.QuotaRequests = store.NewMemoryQuotaRequests()
	return svc, gs
}

// seedOwnerAuthorization puts a live room authorization of owner's provider
// in room-1, with an optional run quota.
func seedOwnerAuthorization(t *testing.T, gs *store.MemoryGrant, owner, provider string, quota *int) {
	t.Helper()
	future := int64(1_000_000_000_000)
	ctx := tctx(tenant.Default)
	if err := gs.PutGrant(ctx, store.Grant{
		GrantID:       roomAuthGrantID(ctx, "room-1", owner, provider),
		RoomID:        "room-1",
		TargetID:      "shared-agent:" + provider,
		OwnerUserID:   owner,
		GranteeUserID: "*",
		MaxConcurrent: 2,
		RunQuota:      quota,
		ExpiresAtMs:   &future,
	}); err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
}

// call drives the full shareagent mux as caller (gateway-injected identity).
func call(t *testing.T, svc *Service, method, path, caller, body string) (int, map[string]any, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if caller != "" {
		r.Header.Set("X-Bnbloc-User", caller)
	}
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func record(t *testing.T, svc *Service, owner, provider, borrower string, cost *float64) {
	t.Helper()
	usage := map[string]any{}
	if cost != nil {
		usage["cost"] = *cost
	}
	code, out := borrowRun(t, svc, tenant.Default, owner, provider, borrower, "", usage)
	if code != http.StatusOK || out["recorded"] != true {
		t.Fatalf("record %s/%s by %s = %d %v", owner, provider, borrower, code, out)
	}
}

func TestQuotaRequestLifecycleRaisesLimitKeepingHistory(t *testing.T) {
	svc, gs := billingService(t)
	one := 1
	seedOwnerAuthorization(t, gs, "alice", "codex", &one)
	record(t, svc, "alice", "codex", "bob", nil)

	// The borrower's authorization read reports the exhausted quota.
	_, auth, _ := call(t, svc, http.MethodGet, "/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization?owner=alice", "bob", "")
	if auth["authorized"] != true || auth["quotaExhausted"] != true || auth["runQuota"] != float64(1) {
		t.Fatalf("authorization read = %v, want authorized + quotaExhausted + runQuota 1", auth)
	}

	// The requester is the header identity; a body "requester" is ignored.
	code, out, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests", "bob",
		`{"roomId":"room-1","owner":"alice","provider":"codex","requestedRuns":5,"note":"need more","requester":"jack"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	req := out["request"].(map[string]any)
	if req["requester"] != "bob" || req["status"] != "pending" {
		t.Fatalf("created request = %v, want requester bob pending", req)
	}
	id := req["requestId"].(string)

	if code, _, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests", "bob",
		`{"roomId":"room-1","owner":"alice","provider":"codex"}`); code != http.StatusConflict {
		t.Fatalf("duplicate pending create = %d, want 409", code)
	}

	// The owner sees it; the requester sees it on their side only.
	_, list, _ := call(t, svc, http.MethodGet, "/v1/borrow/quota-requests?role=owner&status=pending", "alice", "")
	if n := len(list["requests"].([]any)); n != 1 {
		t.Fatalf("owner pending list = %d, want 1", n)
	}
	_, list, _ = call(t, svc, http.MethodGet, "/v1/borrow/quota-requests?role=owner", "bob", "")
	if n := len(list["requests"].([]any)); n != 0 {
		t.Fatalf("requester's owner-side list = %d, want 0", n)
	}

	// Neither the requester nor a bystander may decide it.
	for _, who := range []string{"bob", "jack"} {
		if code, _, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/approve", who, `{"runs":100}`); code != http.StatusForbidden {
			t.Fatalf("approve by %s = %d, want 403", who, code)
		}
	}

	code, out, _ = call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/approve", "alice", "")
	if code != http.StatusOK || out["runQuota"] != float64(6) || out["runsUsed"] != float64(1) {
		t.Fatalf("approve = %d %v, want runQuota 6 with runsUsed kept at 1", code, out)
	}
	if code, _, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/decline", "alice", ""); code != http.StatusConflict {
		t.Fatalf("decide twice = %d, want 409", code)
	}
	_, auth, _ = call(t, svc, http.MethodGet, "/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization?owner=alice", "bob", "")
	if auth["quotaExhausted"] != false {
		t.Fatalf("after approve authorization = %v, want quota available", auth)
	}
}

func TestQuotaRequestDeclineAndPreconditions(t *testing.T) {
	svc, gs := billingService(t)
	two := 2
	seedOwnerAuthorization(t, gs, "alice", "codex", &two)
	seedOwnerAuthorization(t, gs, "jack", "claude", nil)

	cases := []struct {
		name, caller, body string
		want               int
	}{
		{"own agent", "alice", `{"roomId":"room-1","owner":"alice","provider":"codex"}`, http.StatusBadRequest},
		{"no authorization", "bob", `{"roomId":"room-1","owner":"alice","provider":"gemini"}`, http.StatusConflict},
		{"unlimited authorization", "bob", `{"roomId":"room-1","owner":"jack","provider":"claude"}`, http.StatusConflict},
		{"bad runs", "bob", `{"roomId":"room-1","owner":"alice","provider":"codex","requestedRuns":0}`, http.StatusBadRequest},
		{"non-member requester", "mallory", `{"roomId":"room-1","owner":"alice","provider":"codex"}`, http.StatusForbidden},
		{"unauthenticated", "", `{"roomId":"room-1","owner":"alice","provider":"codex"}`, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if code, out, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests", tc.caller, tc.body); code != tc.want {
			t.Errorf("%s: code = %d (%v), want %d", tc.name, code, out, tc.want)
		}
	}

	code, out, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests", "bob",
		`{"roomId":"room-1","owner":"alice","provider":"codex"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["request"].(map[string]any)["requestId"].(string)
	// No amount on the request and none on the approval: the owner must say.
	if code, _, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/approve", "alice", ""); code != http.StatusBadRequest {
		t.Fatalf("approve without runs = %d, want 400", code)
	}
	code, out, _ = call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/decline", "alice", "")
	if code != http.StatusOK || out["request"].(map[string]any)["status"] != "declined" {
		t.Fatalf("decline = %d %v", code, out)
	}
	// Declining reopens the slot: a new request may be made.
	if code, _, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests", "bob",
		`{"roomId":"room-1","owner":"alice","provider":"codex","requestedRuns":3}`); code != http.StatusCreated {
		t.Fatalf("re-request after decline = %d, want 201", code)
	}
	// A revoked authorization cannot be raised.
	_ = gs.RevokeGrant(tctx(tenant.Default), roomAuthGrantID(tctx(tenant.Default), "room-1", "alice", "codex"), 1)
	_, list, _ := call(t, svc, http.MethodGet, "/v1/borrow/quota-requests?role=owner&status=pending", "alice", "")
	pending := list["requests"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	pid := pending[0].(map[string]any)["requestId"].(string)
	if code, _, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+pid+"/approve", "alice", ""); code != http.StatusConflict {
		t.Fatalf("approve on revoked authorization = %d, want 409", code)
	}
}

func TestUsageReportScopesToCaller(t *testing.T) {
	svc, gs := billingService(t)
	seedOwnerAuthorization(t, gs, "alice", "codex", nil)
	seedOwnerAuthorization(t, gs, "jack", "claude", nil)
	record(t, svc, "alice", "codex", "bob", costOf(0.25))
	record(t, svc, "alice", "codex", "bob", costOf(0.5))
	record(t, svc, "alice", "codex", "jack", nil)
	record(t, svc, "jack", "claude", "bob", nil)

	const span = "&from=0&to=2678400000"
	report := func(caller, query string) map[string]any {
		t.Helper()
		code, out, _ := call(t, svc, http.MethodGet, "/v1/borrow/usage-report?"+query+span, caller, "")
		if code != http.StatusOK {
			t.Fatalf("report %s %s = %d %v", caller, query, code, out)
		}
		return out
	}
	// As owner: alice sees borrows of her agent only, grouped per borrower.
	out := report("alice", "as=owner&groupBy=month")
	rows := out["rows"].([]any)
	if len(rows) != 2 || out["totalRuns"] != float64(3) {
		t.Fatalf("alice owner report = %v, want 2 rows / 3 runs", out)
	}
	for _, r := range rows {
		row := r.(map[string]any)
		if row["owner"] != "alice" || row["provider"] != "codex" || row["period"] != "1970-01" {
			t.Fatalf("row = %v", row)
		}
		switch row["borrower"] {
		case "bob":
			if row["runs"] != float64(2) || row["cost"] != 0.75 {
				t.Fatalf("bob row = %v, want 2 runs cost 0.75", row)
			}
		case "jack":
			if _, has := row["cost"]; has {
				t.Fatalf("jack row reports a cost nobody recorded: %v", row)
			}
		}
	}
	// As borrower: bob sees his borrows of alice and jack, never jack's own view.
	out = report("bob", "as=borrower&groupBy=day")
	if out["totalRuns"] != float64(3) {
		t.Fatalf("bob borrower report = %v, want 3 runs", out)
	}
	// A member who owns nothing borrowed sees nothing as owner.
	if out := report("bob", "as=owner"); len(out["rows"].([]any)) != 0 {
		t.Fatalf("bob owner report = %v, want empty", out)
	}
	// The tenant report (gateway-gated manage_org) sees every borrow.
	code, all, _ := call(t, svc, http.MethodGet, "/v1/tenant/borrow/usage-report?groupBy=month"+span, "alice", "")
	if code != http.StatusOK || all["totalRuns"] != float64(4) {
		t.Fatalf("tenant report = %d %v, want 4 runs", code, all)
	}
	// CSV export carries the same rows with a header.
	code, _, body := call(t, svc, http.MethodGet, "/v1/borrow/usage-report?as=owner&format=csv"+span, "alice", "")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if code != http.StatusOK || lines[0] != "period,owner,borrower,provider,room_id,runs,cost,input_tokens,output_tokens" || len(lines) != 3 {
		t.Fatalf("csv = %d %q", code, body)
	}
	// Bad parameters are refused.
	for _, q := range []string{"as=everyone", "groupBy=year", "from=2026-13-01", "from=10&to=5"} {
		if code, _, _ := call(t, svc, http.MethodGet, "/v1/borrow/usage-report?"+q, "alice", ""); code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", q, code)
		}
	}
}

// G2: recorded token usage is stored on the audit row and summed per row and
// in the totals; turns that reported none contribute nothing (absent, never 0).
func TestUsageReportCarriesTokens(t *testing.T) {
	svc, gs := billingService(t)
	seedOwnerAuthorization(t, gs, "alice", "codex", nil)
	// post is one borrowed run: the body's borrower reserves it, the rest of
	// the body is the settle's usage.
	post := func(body string) (int, map[string]any) {
		t.Helper()
		var usage map[string]any
		if err := json.Unmarshal([]byte(body), &usage); err != nil {
			t.Fatal(err)
		}
		borrower, _ := usage["borrower"].(string)
		delete(usage, "borrower")
		return borrowRun(t, svc, tenant.Default, "alice", "codex", borrower, "", usage)
	}
	for _, body := range []string{
		`{"borrower":"bob","model":"m-1","inputTokens":100,"outputTokens":20,"cost":0.25}`,
		`{"borrower":"bob","inputTokens":50,"outputTokens":5}`,
		`{"borrower":"bob"}`,
		`{"borrower":"jack"}`,
	} {
		if code, out := post(body); code != http.StatusOK || out["recorded"] != true {
			t.Fatalf("record %s = %d %v", body, code, out)
		}
	}
	// Negative or absurd token counts are refused (nothing recorded).
	for _, body := range []map[string]any{
		{"reservationId": "rsv_x", "inputTokens": -1},
		{"reservationId": "rsv_x", "outputTokens": 2000000000000},
	} {
		if code, _ := borrowCall(t, svc, tenant.Default, "alice", "alice", "codex", "record", body); code != http.StatusBadRequest {
			t.Fatalf("record %v = %d, want 400", body, code)
		}
	}
	turns := gs.BorrowTurns(tctx(tenant.Default), store.BorrowTurnQuery{})
	if len(turns) != 4 || turns[0].Model != "m-1" || turns[0].InputTokens == nil || *turns[0].InputTokens != 100 ||
		turns[2].InputTokens != nil || turns[2].OutputTokens != nil {
		t.Fatalf("audit turns = %+v", turns)
	}
	code, out, _ := call(t, svc, http.MethodGet, "/v1/borrow/usage-report?as=owner&groupBy=month&from=0&to=2678400000", "alice", "")
	if code != http.StatusOK {
		t.Fatalf("report = %d %v", code, out)
	}
	if out["totalInputTokens"] != float64(150) || out["totalOutputTokens"] != float64(25) || out["totalCost"] != 0.25 {
		t.Fatalf("totals = %v", out)
	}
	for _, r := range out["rows"].([]any) {
		row := r.(map[string]any)
		switch row["borrower"] {
		case "bob":
			if row["runs"] != float64(3) || row["inputTokens"] != float64(150) || row["outputTokens"] != float64(25) {
				t.Fatalf("bob row = %v", row)
			}
		case "jack":
			if _, has := row["inputTokens"]; has {
				t.Fatalf("jack row reports tokens nobody recorded: %v", row)
			}
		}
	}
	_, _, csvBody := call(t, svc, http.MethodGet, "/v1/borrow/usage-report?as=owner&format=csv&from=0&to=2678400000", "alice", "")
	if !strings.Contains(csvBody, ",bob,codex,room-1,3,0.25,150,25") || !strings.Contains(csvBody, ",jack,codex,room-1,1,,,") {
		t.Fatalf("csv = %q", csvBody)
	}
}

func TestCSVCellNeutralizesFormulas(t *testing.T) {
	if got := csvCell("=HYPERLINK(1)"); got != "'=HYPERLINK(1)" {
		t.Fatalf("csvCell = %q", got)
	}
	if got := csvCell("alice"); got != "alice" {
		t.Fatalf("csvCell = %q", got)
	}
}
