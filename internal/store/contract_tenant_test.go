package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// TenantStore contract: tenants, memberships, join requests and the login
// landing rule behave the same on both backends. The store takes tenant ids
// as arguments; every case uses ids of its own.

func TestContractTenantStoreTenants(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.Tenant
		older, newer, tie := uniq("t-old-"), uniq("t-new-"), uniq("t-tie-")
		for _, tn := range []Tenant{{TenantID: newer, Name: "New", CreatedAtMs: 20}, {TenantID: older, Name: "Old", CreatedAtMs: 10}, {TenantID: tie, Name: "Tie", CreatedAtMs: 20}} {
			if err := s.PutTenant(ctx, tn); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.GetTenant(ctx, older)
		if err != nil || got.Status != TenantActive || got.Name != "Old" || got.CreatedAtMs != 10 || got.RunQuota != 0 {
			t.Fatalf("GetTenant = %+v, %v; want active by default", got, err)
		}
		if _, err := s.GetTenant(ctx, uniq("nope-")); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("GetTenant of a missing tenant = %v", err)
		}
		var order []string
		for _, tn := range must(s.ListTenants(ctx))(t) {
			if tn.TenantID == older || tn.TenantID == newer || tn.TenantID == tie {
				order = append(order, tn.TenantID)
			}
		}
		if want := []string{older, minKey(newer, tie), maxKey(newer, tie)}; !slices.Equal(order, want) {
			t.Fatalf("ListTenants order = %v, want %v (oldest first, then id)", order, want)
		}

		// Quotas, policy and retention have their own setters; a re-put of
		// the tenant changes only name, status and issuer.
		if err := s.SetQuota(ctx, older, 5, 1.5); err != nil {
			t.Fatal(err)
		}
		if err := s.SetQuota(ctx, uniq("nope-"), 1, 1); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("SetQuota of a missing tenant = %v", err)
		}
		on, off := true, false
		if p, err := s.SetTenantPolicy(ctx, older, TenantPolicyUpdate{RequireSandboxForBorrow: &on}); err != nil || !p.RequireSandboxForBorrow || p.AnonymousLinksDisabled {
			t.Fatalf("set sandbox = %+v, %v", p, err)
		}
		if p, err := s.SetTenantPolicy(ctx, older, TenantPolicyUpdate{AnonymousLinksDisabled: &on}); err != nil || !p.RequireSandboxForBorrow || !p.AnonymousLinksDisabled || p.OutsideLinksDisabled {
			t.Fatalf("set public shares = %+v, %v (an unset switch is kept)", p, err)
		}
		if p, err := s.SetTenantPolicy(ctx, older, TenantPolicyUpdate{OutsideLinksDisabled: &on}); err != nil || !p.OutsideLinksDisabled || !p.AnonymousLinksDisabled ||
			!must(s.GetTenant(ctx, older))(t).OutsideLinksDisabled {
			t.Fatalf("set outside links = %+v, %v", p, err)
		}
		if p, err := s.SetTenantPolicy(ctx, older, TenantPolicyUpdate{OutsideLinksDisabled: &off}); err != nil || p.OutsideLinksDisabled || !p.AnonymousLinksDisabled {
			t.Fatalf("clear outside links = %+v, %v", p, err)
		}
		days := 30
		if r, err := s.SetTenantRetention(ctx, older, wire.RetentionPolicy{}.With(wire.RetentionKinds[0], &days)); err != nil || r.Retention.Days(wire.RetentionKinds[0]) == nil || *r.Retention.Days(wire.RetentionKinds[0]) != 30 {
			t.Fatalf("SetTenantRetention = %+v, %v", r, err)
		}
		if err := s.PutTenant(ctx, Tenant{TenantID: older, Name: "Renamed", Status: TenantSuspended, CreatedAtMs: 99}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetTenant(ctx, older)
		if got.Name != "Renamed" || got.Status != TenantSuspended || got.CreatedAtMs != 10 || got.RunQuota != 5 || got.CostAllowance != 1.5 ||
			!got.RequireSandboxForBorrow || !got.AnonymousLinksDisabled || got.Retention.Days(wire.RetentionKinds[0]) == nil {
			t.Fatalf("tenant after a re-put = %+v; only name, status and issuer may change", got)
		}
		if p, err := s.SetTenantPolicy(ctx, older, TenantPolicyUpdate{RequireSandboxForBorrow: &off}); err != nil || p.RequireSandboxForBorrow || !p.AnonymousLinksDisabled {
			t.Fatalf("clear sandbox = %+v, %v", p, err)
		}
		if r, err := s.SetTenantRetention(ctx, older, wire.RetentionPolicy{}); err != nil || r.Retention.Days(wire.RetentionKinds[0]) != nil {
			t.Fatalf("clear retention = %+v, %v", r, err)
		}
		if _, err := s.SetTenantPolicy(ctx, uniq("nope-"), TenantPolicyUpdate{AnonymousLinksDisabled: &on}); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("SetTenantPolicy of a missing tenant = %v", err)
		}
		if _, err := s.SetTenantRetention(ctx, uniq("nope-"), wire.RetentionPolicy{}); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("SetTenantRetention of a missing tenant = %v", err)
		}
	})
}

func TestContractTenantStoreMembers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.Tenant
		tid, tid2 := uniq("t-mem-"), uniq("t-mem2-")
		maya, jack, bea := uniq("maya-"), uniq("jack-"), uniq("bea-")
		if got := must(s.ListMembers(ctx, tid))(t); got == nil || len(got) != 0 {
			t.Fatalf("members of an empty tenant = %#v, want []", got)
		}
		if got := must(s.ListMembershipsForUser(ctx, maya))(t); got == nil || len(got) != 0 {
			t.Fatalf("memberships of nobody = %#v, want []", got)
		}
		for _, m := range []TenantMember{
			{TenantID: tid, UserID: jack, JoinedAtMs: 5},
			{TenantID: tid, UserID: maya, Role: OrgRoleAdmin, JoinedAtMs: 5, Source: MemberSourceSync},
			{TenantID: tid, UserID: bea, Role: OrgRoleViewer, JoinedAtMs: 1},
			{TenantID: tid2, UserID: maya, Role: OrgRoleMember, JoinedAtMs: 3},
		} {
			if err := s.PutMember(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		if m, ok, err := s.GetMember(ctx, tid, jack); err != nil || !ok || m.Role != OrgRoleMember || m.Source != MemberSourceManual {
			t.Fatalf("defaulted member = %+v %v %v; want member, manual", m, ok, err)
		}
		if _, ok, err := s.GetMember(ctx, tid2, jack); err != nil || ok {
			t.Fatalf("GetMember in another tenant = %v %v", ok, err)
		}
		var order []string
		for _, m := range must(s.ListMembers(ctx, tid))(t) {
			order = append(order, m.UserID)
		}
		if want := []string{bea, minKey(jack, maya), maxKey(jack, maya)}; !slices.Equal(order, want) {
			t.Fatalf("ListMembers = %v, want %v (join order, then user id)", order, want)
		}
		var tenants []string
		for _, m := range must(s.ListMembershipsForUser(ctx, maya))(t) {
			tenants = append(tenants, m.TenantID+"/"+string(m.Source))
		}
		if want := []string{tid2 + "/manual", tid + "/sync"}; !slices.Equal(tenants, want) {
			t.Fatalf("ListMembershipsForUser = %v, want %v", tenants, want)
		}
		if n := must(s.MemberCount(ctx, tid))(t); n != 3 {
			t.Fatalf("MemberCount = %d", n)
		}
		// A re-put (an admin decision) takes the role and source over and
		// keeps the join time.
		if err := s.PutMember(ctx, TenantMember{TenantID: tid, UserID: maya, Role: OrgRoleViewer, JoinedAtMs: 999, Source: MemberSourceManual}); err != nil {
			t.Fatal(err)
		}
		if m, _, _ := s.GetMember(ctx, tid, maya); m.Role != OrgRoleViewer || m.Source != MemberSourceManual || m.JoinedAtMs != 5 {
			t.Fatalf("re-put member = %+v; want viewer, manual, joined at 5", m)
		}
		if err := s.RemoveMember(ctx, tid, bea); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveMember(ctx, tid, bea); err != nil {
			t.Fatalf("removing an absent member = %v", err)
		}
		if n := must(s.MemberCount(ctx, tid))(t); n != 2 {
			t.Fatalf("MemberCount after remove = %d", n)
		}
	})
}

// The login landing rule: a real org outranks the default tenant even when
// the default membership is newer; among orgs the most recent join wins,
// then the smallest tenant id.
func TestContractTenantStorePrimaryTenant(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.Tenant
		put := func(tid, user string, at int64) {
			t.Helper()
			if err := s.PutMember(ctx, TenantMember{TenantID: tid, UserID: user, JoinedAtMs: at}); err != nil {
				t.Fatal(err)
			}
		}
		primary := func(user string) string {
			t.Helper()
			tid, ok, err := s.PrimaryTenantForUser(ctx, user)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return ""
			}
			return tid
		}
		ghost := uniq("ghost-")
		if got := primary(ghost); got != "" {
			t.Fatalf("no membership = %q", got)
		}
		solo := uniq("solo-")
		put(tenant.Default, solo, 1)
		if got := primary(solo); got != tenant.Default {
			t.Fatalf("only the default tenant = %q", got)
		}
		dave, acme, globex, zeta := uniq("dave-"), uniq("acme-"), uniq("globex-"), uniq("zeta-")
		put(acme, dave, 100)
		put(tenant.Default, dave, 999)
		if got := primary(dave); got != acme {
			t.Fatalf("org vs newer default = %q, want %s", got, acme)
		}
		put(globex, dave, 200)
		if got := primary(dave); got != globex {
			t.Fatalf("most recent org = %q, want %s", got, globex)
		}
		put(zeta, dave, 200)
		if got := primary(dave); got != minKey(globex, zeta) {
			t.Fatalf("tie on join time = %q, want the smaller id %s", got, minKey(globex, zeta))
		}
	})
}

func TestContractTenantStoreJoinRequests(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := context.Background()
		s := b.Tenant
		tid, tid2 := uniq("t-jr-"), uniq("t-jr2-")
		maya, jack, bea := uniq("maya-"), uniq("jack-"), uniq("bea-")
		if got := must(s.ListJoinRequests(ctx, tid, ""))(t); got == nil || len(got) != 0 {
			t.Fatalf("join requests of an empty tenant = %#v, want []", got)
		}
		if got := must(s.ListJoinRequestsForUser(ctx, maya))(t); got == nil || len(got) != 0 {
			t.Fatalf("join requests of nobody = %#v, want []", got)
		}
		for _, jr := range []JoinRequest{
			{TenantID: tid, UserID: jack, Message: "hi", Status: JoinPending, CreatedAtMs: 5},
			{TenantID: tid, UserID: maya, Status: JoinPending, CreatedAtMs: 5},
			{TenantID: tid, UserID: bea, Status: JoinRejected, CreatedAtMs: 1, DecidedAtMs: 2, DecidedBy: "admin"},
			{TenantID: tid2, UserID: maya, Status: JoinPending, CreatedAtMs: 9},
		} {
			if err := s.PutJoinRequest(ctx, jr); err != nil {
				t.Fatal(err)
			}
		}
		if jr, ok, err := s.GetJoinRequest(ctx, tid, bea); err != nil || !ok || jr.DecidedBy != "admin" || jr.DecidedAtMs != 2 || jr.Status != JoinRejected {
			t.Fatalf("GetJoinRequest = %+v %v %v", jr, ok, err)
		}
		if _, ok, err := s.GetJoinRequest(ctx, tid2, bea); err != nil || ok {
			t.Fatalf("GetJoinRequest in another tenant = %v %v", ok, err)
		}
		users := func(list []JoinRequest) []string {
			out := []string{}
			for _, jr := range list {
				out = append(out, jr.UserID)
			}
			return out
		}
		if got, want := users(must(s.ListJoinRequests(ctx, tid, ""))(t)), []string{bea, minKey(jack, maya), maxKey(jack, maya)}; !slices.Equal(got, want) {
			t.Fatalf("ListJoinRequests = %v, want %v", got, want)
		}
		if got, want := users(must(s.ListJoinRequests(ctx, tid, JoinPending))(t)), []string{minKey(jack, maya), maxKey(jack, maya)}; !slices.Equal(got, want) {
			t.Fatalf("pending requests = %v, want %v", got, want)
		}
		var tenants []string
		for _, jr := range must(s.ListJoinRequestsForUser(ctx, maya))(t) {
			tenants = append(tenants, jr.TenantID)
		}
		if want := []string{tid2, tid}; !slices.Equal(tenants, want) {
			t.Fatalf("ListJoinRequestsForUser = %v, want newest first %v", tenants, want)
		}
		// A re-put replaces the request.
		if err := s.PutJoinRequest(ctx, JoinRequest{TenantID: tid, UserID: jack, Status: JoinApproved, CreatedAtMs: 7, DecidedAtMs: 8, DecidedBy: "admin"}); err != nil {
			t.Fatal(err)
		}
		if jr, _, _ := s.GetJoinRequest(ctx, tid, jack); jr.Status != JoinApproved || jr.Message != "" || jr.CreatedAtMs != 7 || jr.DecidedBy != "admin" {
			t.Fatalf("replaced request = %+v", jr)
		}
	})
}

// TestTenantShareAccessAllowed: the organization's one rule of which link
// access modes it allows. Org links always; outside links off disables
// signed_in and anyone; anonymous links off disables anyone only; an
// unknown or empty mode is never allowed.
func TestTenantShareAccessAllowed(t *testing.T) {
	for _, c := range []struct {
		outside, anonymous bool
		want               []string
	}{
		{false, false, []string{wire.ShareAccessOrg, wire.ShareAccessSignedIn, wire.ShareAccessAnyone}},
		{false, true, []string{wire.ShareAccessOrg, wire.ShareAccessSignedIn}},
		{true, false, []string{wire.ShareAccessOrg}},
		{true, true, []string{wire.ShareAccessOrg}},
	} {
		tn := Tenant{OutsideLinksDisabled: c.outside, AnonymousLinksDisabled: c.anonymous}
		got := []string{}
		for _, a := range append([]string{"", "public"}, wire.ShareAccessModes...) {
			if tn.ShareAccessAllowed(a) {
				got = append(got, a)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("outside off %v, anonymous off %v: allowed %v, want %v", c.outside, c.anonymous, got, c.want)
		}
	}
}
