package orgsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

type fixture struct {
	sync     *Syncer
	tenants  *store.MemoryTenant
	rooms    *store.MemoryRoom
	sessions *store.MemoryRefreshToken
	audit    *store.MemoryExtensionAudit
	settings *store.MemoryPlatformSettings
}

func newFixture(t *testing.T, tenantIDs ...string) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{
		tenants:  store.NewMemoryTenant(),
		rooms:    store.NewMemoryRoom(),
		sessions: store.NewMemoryRefreshToken(),
		audit:    store.NewMemoryExtensionAudit(),
		settings: store.NewMemoryPlatformSettings(),
	}
	for _, id := range tenantIDs {
		if err := f.tenants.PutTenant(ctx, store.Tenant{TenantID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	f.sync = &Syncer{
		Settings: f.settings, Tenants: f.tenants, Rooms: f.rooms, State: store.NewMemoryOrgSync(),
		Sessions: f.sessions, Audit: f.audit, Now: func() time.Time { return time.UnixMilli(1_000_000) },
	}
	return f
}

func (f *fixture) room(t *testing.T, tenantID, roomID string) {
	t.Helper()
	ctx := tenant.NewContext(context.Background(), tenantID)
	if err := f.rooms.CreateRoomWithOwner(ctx, store.Room{RoomID: roomID, Name: roomID},
		store.Membership{RoomID: roomID, UserID: "owner", Role: store.RoleOwner}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) save(t *testing.T, rules ...Rule) {
	t.Helper()
	if _, err := f.sync.SaveRules(context.Background(), rules, "root"); err != nil {
		t.Fatalf("save rules: %v", err)
	}
}

func (f *fixture) apply(t *testing.T, a Attributes) Result {
	t.Helper()
	res, err := f.sync.Apply(context.Background(), a, "test", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

func (f *fixture) member(t *testing.T, tenantID, userID string) (store.TenantMember, bool) {
	t.Helper()
	m, ok, err := f.tenants.GetMember(context.Background(), tenantID, userID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	return m, ok
}

func TestPlan_firstMatchingRulePerTenantWins(t *testing.T) {
	// Given two rules for the same tenant and a later rule for another tenant.
	rules := []Rule{
		{Match: Match{Group: "eng"}, Tenant: "acme", Role: RoleMember},
		{Match: Match{Group: "eng-leads"}, Tenant: "acme", Role: RoleOrgAdmin},
		{Match: Match{EmailDomain: "acme.com"}, Tenant: "labs", Role: RoleOrgAdmin},
	}
	// When a user in both groups is planned.
	got := Plan(rules, Attributes{UserID: "u", Email: "u@ACME.com", Groups: []string{"ENG-leads", "eng"}})
	// Then the first rule decides acme's role and the landing tenant.
	if len(got) != 2 || got[0].Tenant != "acme" || got[0].Role != RoleMember || got[0].Rule != 0 {
		t.Fatalf("targets = %+v, want acme/member from rule 0 first", got)
	}
	if got[1].Tenant != "labs" || got[1].Role != RoleOrgAdmin {
		t.Fatalf("second target = %+v, want labs/org_admin", got[1])
	}

	// And reordering the rules changes the outcome.
	rules[0], rules[1] = rules[1], rules[0]
	got = Plan(rules, Attributes{UserID: "u", Groups: []string{"eng", "eng-leads"}})
	if len(got) != 1 || got[0].Role != RoleOrgAdmin {
		t.Fatalf("reordered targets = %+v, want acme/org_admin", got)
	}
}

func TestPlan_matchesDepartmentCaseInsensitively(t *testing.T) {
	got := Plan([]Rule{{Match: Match{Department: "Research"}, Tenant: "acme"}}, Attributes{UserID: "u", Department: "research"})
	if len(got) != 1 || got[0].Role != RoleMember {
		t.Fatalf("targets = %+v, want acme/member (empty role = member)", got)
	}
	if got := Plan([]Rule{{Match: Match{Department: "Research"}, Tenant: "acme"}}, Attributes{UserID: "u"}); len(got) != 0 {
		t.Fatalf("no department matched %+v", got)
	}
}

func TestRoleNeverEscalatesToSuperAdmin(t *testing.T) {
	f := newFixture(t, "acme")
	ctx := context.Background()
	// Given a rule set naming super_admin.
	_, err := f.sync.SaveRules(ctx, []Rule{{Match: Match{Group: "root"}, Tenant: "acme", Role: "super_admin"}}, "root")
	// Then validation refuses it.
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("save err = %v, want a validation error", err)
	}
	// And a hand-written setting carrying it is skipped, never applied.
	raw := `{"rules":[{"match":{"group":"root"},"tenant":"acme","role":"super_admin"},{"match":{"group":"root"},"tenant":"acme","role":"viewer"}]}`
	if err := f.settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: SettingKey, Value: raw}); err != nil {
		t.Fatal(err)
	}
	res := f.apply(t, Attributes{UserID: "eve", Groups: []string{"root"}})
	if len(res.Targets) != 0 || len(res.Changes) != 0 {
		t.Fatalf("result = %+v, want no targets", res)
	}
	if _, ok := f.member(t, "acme", "eve"); ok {
		t.Fatal("eve got a membership from a super_admin rule")
	}
}

func TestValidate_refusesMissingTenantForeignRoomAndBadMatch(t *testing.T) {
	f := newFixture(t, "acme", "labs")
	f.room(t, "labs", "r-labs")
	cases := []Rule{
		{Match: Match{Group: "g"}, Tenant: "nope"},
		{Match: Match{Group: "g"}, Tenant: "acme", DefaultRooms: []string{"r-labs"}},
		{Match: Match{Group: "g"}, Tenant: "acme", DefaultRooms: []string{"r-missing"}},
		{Match: Match{}, Tenant: "acme"},
		{Match: Match{Group: "g", Department: "d"}, Tenant: "acme"},
	}
	for i, r := range cases {
		if err := f.sync.Validate(context.Background(), []Rule{r}); err == nil {
			t.Fatalf("case %d (%+v): validated, want refusal", i, r)
		}
	}
	if err := f.sync.Validate(context.Background(), []Rule{{Match: Match{Group: "g"}, Tenant: "labs", DefaultRooms: []string{"r-labs"}}}); err != nil {
		t.Fatalf("valid rule refused: %v", err)
	}
}

func TestApply_addsChangesRoleAndRemovesSyncMembershipsAndRevokesSessions(t *testing.T) {
	f := newFixture(t, "acme", "labs")
	ctx := context.Background()
	f.save(t,
		Rule{Match: Match{Group: "eng"}, Tenant: "acme", Role: RoleMember},
		Rule{Match: Match{Group: "lab"}, Tenant: "labs", Role: RoleMember},
	)
	// Given a first sign-in in both groups.
	res := f.apply(t, Attributes{UserID: "ann", Groups: []string{"eng", "lab"}})
	if res.Tenant != "acme" || len(res.Changes) != 2 {
		t.Fatalf("first apply = %+v, want two adds landing in acme", res)
	}
	m, ok := f.member(t, "labs", "ann")
	if !ok || m.Source != store.MemberSourceSync || m.Role != store.OrgRoleMember {
		t.Fatalf("labs membership = %+v ok=%v, want sync member", m, ok)
	}
	// And a sign-in that landed in labs.
	if err := f.sessions.CreateRefreshFamily(ctx, store.RefreshFamily{
		FamilyID: "fam-labs", Subject: "ann", TenantID: "labs", CurrentJTI: "j", ExpiresAtMs: 9_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.CreateRefreshFamily(ctx, store.RefreshFamily{
		FamilyID: "fam-acme", Subject: "ann", TenantID: "acme", CurrentJTI: "j", ExpiresAtMs: 9_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	// When the rules promote eng and ann leaves the lab group.
	f.save(t, Rule{Match: Match{Group: "eng"}, Tenant: "acme", Role: RoleOrgAdmin}, Rule{Match: Match{Group: "lab"}, Tenant: "labs"})
	res = f.apply(t, Attributes{UserID: "ann", Groups: []string{"eng"}})
	// Then the acme role follows the rule and the labs membership is removed.
	if m, _ := f.member(t, "acme", "ann"); m.Role != store.OrgRoleAdmin {
		t.Fatalf("acme role = %q, want org_admin", m.Role)
	}
	if _, ok := f.member(t, "labs", "ann"); ok {
		t.Fatal("labs sync membership survived its rule")
	}
	var removed Change
	for _, c := range res.Changes {
		if c.Action == ActionRemove {
			removed = c
		}
	}
	if removed.Tenant != "labs" || removed.RevokedSessions != 1 {
		t.Fatalf("removal = %+v, want labs with 1 revoked session", removed)
	}
	// And only the labs sign-in was revoked.
	labs, _, _ := f.sessions.GetRefreshFamily(ctx, "fam-labs")
	acme, _, _ := f.sessions.GetRefreshFamily(ctx, "fam-acme")
	if labs.RevokedAtMs == 0 || acme.RevokedAtMs != 0 {
		t.Fatalf("revoked labs=%d acme=%d, want only labs", labs.RevokedAtMs, acme.RevokedAtMs)
	}
	// And every change is audited.
	recs, _ := f.audit.ExtAuditRecords(ctx, AuditKind, 0, 0)
	actions := map[string]int{}
	for _, r := range recs {
		actions[r.Action]++
	}
	if actions[ActionAdd] != 2 || actions[ActionRole] != 1 || actions[ActionRemove] != 1 || actions["rules"] != 2 {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestApply_leavesManualMembershipsUntouched(t *testing.T) {
	f := newFixture(t, "acme", "labs")
	ctx := context.Background()
	// Given manual memberships in acme (admin-added) and labs (no source).
	_ = f.tenants.PutMember(ctx, store.TenantMember{TenantID: "acme", UserID: "bob", Role: store.OrgRoleViewer, Source: store.MemberSourceManual})
	_ = f.tenants.PutMember(ctx, store.TenantMember{TenantID: "labs", UserID: "bob", Role: store.OrgRoleAdmin})
	f.save(t, Rule{Match: Match{Group: "eng"}, Tenant: "acme", Role: RoleOrgAdmin})
	// When a sign-in matches acme only.
	res := f.apply(t, Attributes{UserID: "bob", Groups: []string{"eng"}})
	// Then acme keeps its manual role and labs (no rule) is not removed.
	if m, _ := f.member(t, "acme", "bob"); m.Role != store.OrgRoleViewer || m.Source != store.MemberSourceManual {
		t.Fatalf("acme = %+v, want untouched manual viewer", m)
	}
	if _, ok := f.member(t, "labs", "bob"); !ok {
		t.Fatal("manual labs membership was removed by sync")
	}
	if len(res.Changes) != 1 || res.Changes[0].Action != ActionKeepManual {
		t.Fatalf("changes = %+v, want one keep-manual", res.Changes)
	}
}

func TestApply_addsDefaultRoomsOnce(t *testing.T) {
	f := newFixture(t, "acme")
	f.room(t, "acme", "lobby")
	f.save(t, Rule{Match: Match{EmailDomain: "acme.com"}, Tenant: "acme", DefaultRooms: []string{"lobby"}})
	tctx := tenant.NewContext(context.Background(), "acme")
	attrs := Attributes{UserID: "cy@acme.com", Email: "cy@acme.com"}
	f.apply(t, attrs)
	isMember, err := f.rooms.IsMember(tctx, "lobby", "cy@acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if !isMember {
		t.Fatal("not added to the default room")
	}
	// When the member leaves the room and signs in again.
	if err := f.rooms.RemoveMember(tctx, "lobby", "cy@acme.com"); err != nil {
		t.Fatal(err)
	}
	res := f.apply(t, attrs)
	// Then they are not re-added.
	if member, err := f.rooms.IsMember(tctx, "lobby", "cy@acme.com"); err != nil || member || len(res.Changes) != 0 {
		t.Fatalf("re-added to a left default room (changes %+v, %v)", res.Changes, err)
	}
}

func TestApply_dryRunWritesNothingAndNoRulesIsOff(t *testing.T) {
	f := newFixture(t, "acme")
	ctx := context.Background()
	_ = f.tenants.PutMember(ctx, store.TenantMember{TenantID: "acme", UserID: "dee", Role: store.OrgRoleMember, Source: store.MemberSourceSync})
	// With no rules saved, sync is off: even a sync row is left alone.
	if res := f.apply(t, Attributes{UserID: "dee"}); len(res.Changes) != 0 {
		t.Fatalf("no-rules apply changed %+v", res.Changes)
	}
	f.save(t, Rule{Match: Match{Group: "eng"}, Tenant: "acme", Role: RoleOrgAdmin})
	res, err := f.sync.Apply(ctx, Attributes{UserID: "dee", Groups: []string{"eng"}}, "root", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Changes) != 1 || res.Changes[0].Action != ActionRole {
		t.Fatalf("preview = %+v, want one role change", res)
	}
	if m, _ := f.member(t, "acme", "dee"); m.Role != store.OrgRoleMember {
		t.Fatalf("dry run wrote role %q", m.Role)
	}
	if subs, _ := f.sync.State.ListOrgSyncSubjects(ctx); len(subs) != 0 {
		t.Fatalf("dry run recorded subjects %+v", subs)
	}
}

func TestResyncReappliesChangedRules(t *testing.T) {
	f := newFixture(t, "acme", "labs")
	f.save(t, Rule{Match: Match{Group: "eng"}, Tenant: "acme"})
	f.apply(t, Attributes{UserID: "eli", Groups: []string{"eng"}})
	// When the rule moves eng to labs and a resync runs.
	f.save(t, Rule{Match: Match{Group: "eng"}, Tenant: "labs"})
	out, err := f.sync.Resync(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if out.Subjects != 1 || len(out.Changed) != 1 {
		t.Fatalf("resync = %+v", out)
	}
	if _, ok := f.member(t, "acme", "eli"); ok {
		t.Fatal("acme sync membership kept after resync")
	}
	if _, ok := f.member(t, "labs", "eli"); !ok {
		t.Fatal("labs membership missing after resync")
	}
}

func TestDeprovisionRemovesEveryMembershipAndRevokesSessions(t *testing.T) {
	f := newFixture(t, "acme", "labs")
	ctx := context.Background()
	f.save(t, Rule{Match: Match{Group: "eng"}, Tenant: "acme"})
	f.apply(t, Attributes{UserID: "fay", Groups: []string{"eng"}})
	_ = f.tenants.PutMember(ctx, store.TenantMember{TenantID: "labs", UserID: "fay", Role: store.OrgRoleMember})
	_ = f.sessions.CreateRefreshFamily(ctx, store.RefreshFamily{FamilyID: "f1", Subject: "fay", TenantID: "acme", CurrentJTI: "j", ExpiresAtMs: 9_000_000})
	out, err := f.sync.Deprovision(ctx, []string{"fay", " fay "}, "feed")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].RemovedTenants) != 2 || out[0].RevokedSessions != 1 {
		t.Fatalf("deprovision = %+v", out)
	}
	got, err := f.tenants.ListMembershipsForUser(ctx, "fay")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("memberships left: %+v", got)
	}
	// And a resync cannot restore the departed user.
	if res, _ := f.sync.Resync(ctx, "root"); res.Subjects != 0 {
		t.Fatalf("resync still knows the departed user: %+v", res)
	}
}
