package shareagent

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// tctx scopes controller tests to a tenant; the store fails closed without one.
func tctx() context.Context { return tenant.NewContext(context.Background(), "t-test") }

type fakePresence struct{ online bool }

func (f fakePresence) IsOwnerOnline(string, string) bool { return f.online }

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

func newController(t *testing.T, online bool) (*Controller, *store.MemoryGrant) {
	t.Helper()
	gs := store.NewMemoryGrant()
	var seq int
	c := &Controller{
		Grants:   gs,
		Presence: fakePresence{online: online},
		Audit:    gs,
		NewLeaseID: func() string {
			seq++
			return "lease-" + string(rune('0'+seq))
		},
		Now: func() int64 { return 1000 },
	}
	return c, gs
}

func baseGrant() store.Grant {
	return store.Grant{
		GrantID:       "g1",
		RoomID:        "room-1",
		TargetID:      "shared-agent:codex",
		OwnerUserID:   "alice",
		GranteeUserID: "bob",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}},
		Capabilities:  store.Capabilities{Consult: true},
		MaxConcurrent: 1,
	}
}

func turnReq() Request {
	return Request{
		RoomID: "room-1", TargetID: "shared-agent:codex", GrantID: "g1",
		ActorUserID: "bob", Action: ActionTurn, Model: "gpt-5", TurnCapability: "consult",
	}
}

// A fully valid turn is allowed, acquires a lease, and writes one allow audit.
func TestAuthorizeAllowsValidTurn(t *testing.T) {
	c, gs := newController(t, true)
	_ = gs.PutGrant(tctx(), baseGrant())
	d := c.Authorize(tctx(), turnReq())
	if !d.Allowed || d.LeaseID == "" {
		t.Fatalf("decision = %+v, want allowed with lease", d)
	}
	if got := gs.AuditRecords(tctx(), "room-1", 100, 0); len(got) != 1 || got[0].Decision != "allow" {
		t.Fatalf("audit = %+v, want one allow", got)
	}
	if gs.ActiveLeases(tctx(), "g1") != 1 {
		t.Fatalf("active leases = %d, want 1", gs.ActiveLeases(tctx(), "g1"))
	}
	c.Release(tctx(), d.LeaseID)
	if gs.ActiveLeases(tctx(), "g1") != 0 {
		t.Fatalf("active leases after release = %d, want 0", gs.ActiveLeases(tctx(), "g1"))
	}
}

// An expired grant (ExpiresAtMs <= now) is invalid; a future TTL is allowed.
func TestAuthorizeRejectsExpiredGrant(t *testing.T) {
	// Now() is 1000 (see newController).
	past := int64(500)
	c, gs := newController(t, true)
	g := baseGrant()
	g.ExpiresAtMs = &past
	_ = gs.PutGrant(tctx(), g)
	if d := c.Authorize(tctx(), turnReq()); d.Allowed || d.DenyReason != DenyGrantInvalid {
		t.Fatalf("expired grant decision = %+v, want deny grant_invalid", d)
	}

	future := int64(2000)
	c2, gs2 := newController(t, true)
	g2 := baseGrant()
	g2.ExpiresAtMs = &future
	_ = gs2.PutGrant(tctx(), g2)
	if d := c2.Authorize(tctx(), turnReq()); !d.Allowed {
		t.Fatalf("unexpired grant decision = %+v, want allowed", d)
	}
}

// Each denial condition maps to its stable reason and writes a deny audit.
func TestAuthorizeDenyReasons(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*Controller, *store.MemoryGrant)
		req    Request
		reason string
	}{
		{
			name: "grant invalid: wrong grantee",
			setup: func(_ *Controller, gs *store.MemoryGrant) {
				g := baseGrant()
				g.GranteeUserID = "eve"
				_ = gs.PutGrant(tctx(), g)
			},
			req:    turnReq(),
			reason: DenyGrantInvalid,
		},
		{
			name: "binding revoked",
			setup: func(_ *Controller, gs *store.MemoryGrant) {
				g := baseGrant()
				ms := int64(1)
				g.RevokedAtMs = &ms
				_ = gs.PutGrant(tctx(), g)
			},
			req:    turnReq(),
			reason: DenyBindingRevoked,
		},
		{
			name: "owner offline",
			setup: func(c *Controller, gs *store.MemoryGrant) {
				c.Presence = fakePresence{online: false}
				_ = gs.PutGrant(tctx(), baseGrant())
			},
			req:    turnReq(),
			reason: DenyOwnerOffline,
		},
		{
			name:   "model out of range",
			setup:  func(_ *Controller, gs *store.MemoryGrant) { _ = gs.PutGrant(tctx(), baseGrant()) },
			req:    func() Request { r := turnReq(); r.Model = "forbidden-model"; return r }(),
			reason: DenyModelOutOfRange,
		},
		{
			name: "capability denied",
			setup: func(_ *Controller, gs *store.MemoryGrant) {
				g := baseGrant()
				g.Capabilities = store.Capabilities{Consult: false}
				_ = gs.PutGrant(tctx(), g)
			},
			req:    turnReq(),
			reason: DenyCapabilityDenied,
		},
		{
			name: "quota exhausted",
			setup: func(_ *Controller, gs *store.MemoryGrant) {
				g := baseGrant()
				g.RunQuota = intPtr(0)
				_ = gs.PutGrant(tctx(), g)
			},
			req:    turnReq(),
			reason: DenyQuotaExhausted,
		},
		{
			name: "cost exhausted",
			setup: func(_ *Controller, gs *store.MemoryGrant) {
				g := baseGrant()
				g.CostAllowance = floatPtr(0.5)
				_ = gs.PutGrant(tctx(), g)
			},
			req:    func() Request { r := turnReq(); r.Cost = 1.0; return r }(),
			reason: DenyCostExhausted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, gs := newController(t, true)
			tc.setup(c, gs)
			d := c.Authorize(tctx(), tc.req)
			if d.Allowed || d.DenyReason != tc.reason {
				t.Fatalf("decision = %+v, want deny %q", d, tc.reason)
			}
			recs := gs.AuditRecords(tctx(), "room-1", 100, 0)
			if len(recs) != 1 || recs[0].Decision != "deny" || recs[0].DenyReason != tc.reason {
				t.Fatalf("audit = %+v, want one deny %q", recs, tc.reason)
			}
		})
	}
}

// Concurrency ceiling: a second concurrent turn is denied concurrency_full.
func TestAuthorizeConcurrencyFull(t *testing.T) {
	c, gs := newController(t, true)
	_ = gs.PutGrant(tctx(), baseGrant()) // MaxConcurrent 1
	first := c.Authorize(tctx(), turnReq())
	if !first.Allowed {
		t.Fatalf("first turn = %+v, want allowed", first)
	}
	second := c.Authorize(tctx(), turnReq())
	if second.Allowed || second.DenyReason != DenyConcurrencyFull {
		t.Fatalf("second turn = %+v, want concurrency_full", second)
	}
}

// A missing dependency fails closed (grant_invalid), never allow.
func TestAuthorizeFailsClosedWithoutDeps(t *testing.T) {
	c := &Controller{} // nothing wired
	d := c.Authorize(tctx(), turnReq())
	if d.Allowed {
		t.Fatal("unwired controller must fail closed")
	}
}

// An empty allowed-model range means "no restriction" (BE-21: no invented
// default list); a non-empty range still restricts.
func TestModelInRangeEmptyMeansUnrestricted(t *testing.T) {
	if !modelInRange(nil, "", "any-model") {
		t.Fatal("empty range must allow any model")
	}
	if modelInRange([]store.AllowedModel{{Model: "m1"}}, "", "m2") {
		t.Fatal("non-empty range must restrict")
	}
}

// A plan-less allowed model matches any plan id.
func TestModelInRangePlanless(t *testing.T) {
	if !modelInRange([]store.AllowedModel{{Model: "gpt-5"}}, "plan-x", "gpt-5") {
		t.Fatal("plan-less entry should match any plan")
	}
	if modelInRange([]store.AllowedModel{{ModelPlanID: "plan-a", Model: "gpt-5"}}, "plan-b", "gpt-5") {
		t.Fatal("plan-scoped entry should not match a different plan")
	}
}

// racingConsume simulates a concurrent turn exhausting a ceiling between the
// controller's snapshot pre-checks and its atomic ConsumeRun.
type racingConsume struct {
	store.GrantStore
	err error
}

func (r racingConsume) ConsumeRun(context.Context, string, float64) (store.Grant, error) {
	return store.Grant{}, r.err
}

// An atomic ConsumeRun refusal maps to its real deny reason, not grant_invalid.
func TestAuthorizeMapsConsumeRefusalToDenyReason(t *testing.T) {
	cases := map[error]string{
		store.ErrRunQuotaExhausted:      DenyQuotaExhausted,
		store.ErrCostAllowanceExhausted: DenyCostExhausted,
		store.ErrGrantNotFound:          DenyGrantInvalid,
	}
	for consumeErr, want := range cases {
		c, gs := newController(t, true)
		_ = gs.PutGrant(tctx(), baseGrant())
		c.Grants = racingConsume{GrantStore: gs, err: consumeErr}
		d := c.Authorize(tctx(), turnReq())
		if d.Allowed || d.DenyReason != want {
			t.Fatalf("ConsumeRun %v: decision = %+v, want deny %q", consumeErr, d, want)
		}
	}
}
