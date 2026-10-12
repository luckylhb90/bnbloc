// Package orgsync maps the directory attributes an auth provider asserts at
// sign-in (groups, department, email domain) onto tenant memberships, org
// roles, and default rooms through super-admin mapping rules (R11).
//
// Ownership is the safety model: a membership sync creates is recorded with
// source "sync", and sync only ever changes or removes sync-owned rows. A
// manual membership (an admin add, an approved join request, login
// provisioning) is never altered by sync. Rules can grant member or org_admin,
// never super_admin (a platform role that comes only from configuration).
package orgsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const (
	// SettingKey is the platform setting holding the rule set (JSON).
	SettingKey = "org_sync.rules"
	// AuditKind tags org-sync rows in the extension audit trail (name = the
	// affected user id, or "rules" for a rule-set change).
	AuditKind = "org-sync"
	// MaxRules bounds the rule set.
	MaxRules = 200
	// maxDefaultRooms bounds one rule's default rooms.
	maxDefaultRooms = 50
)

// Roles a rule may grant.
const (
	RoleMember   = string(store.OrgRoleMember)
	RoleOrgAdmin = string(store.OrgRoleAdmin)
)

// Change actions.
const (
	ActionAdd         = "add"         // a sync membership was created
	ActionRole        = "role"        // a sync membership's role changed
	ActionRemove      = "remove"      // a sync membership whose rule no longer matches was removed
	ActionKeepManual  = "keep-manual" // a rule targets a tenant the user holds manually: left untouched
	ActionRoom        = "room"        // the user was added to a default room (once)
	ActionDeprovision = "deprovision"
)

// Match selects users by exactly one directory attribute.
type Match struct {
	Group       string `json:"group,omitempty"`
	Department  string `json:"department,omitempty"`
	EmailDomain string `json:"emailDomain,omitempty"`
}

// Rule maps matching users to a tenant membership with a role and, once,
// to default rooms of that tenant.
type Rule struct {
	Match        Match    `json:"match"`
	Tenant       string   `json:"tenant"`
	Role         string   `json:"role"`
	DefaultRooms []string `json:"defaultRooms,omitempty"`
}

// RuleSet is the persisted rule list with its provenance.
type RuleSet struct {
	Rules       []Rule `json:"rules"`
	UpdatedAtMs int64  `json:"updatedAtMs,omitempty"`
	UpdatedBy   string `json:"updatedBy,omitempty"`
}

// Attributes is what an auth provider asserted about a user.
type Attributes struct {
	UserID      string   `json:"userId"`
	Email       string   `json:"email,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Department  string   `json:"department,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

// Target is one tenant membership the rules give a user.
type Target struct {
	Tenant       string   `json:"tenant"`
	Role         string   `json:"role"`
	DefaultRooms []string `json:"defaultRooms,omitempty"`
	// Rule is the index of the first matching rule for the tenant.
	Rule int `json:"rule"`
}

// Change is one membership/room effect of applying the rules.
type Change struct {
	Action   string `json:"action"`
	Tenant   string `json:"tenant"`
	Role     string `json:"role,omitempty"`
	FromRole string `json:"fromRole,omitempty"`
	RoomID   string `json:"roomId,omitempty"`
	// RevokedSessions counts the sign-ins a removal ended.
	RevokedSessions int `json:"revokedSessions,omitempty"`
}

// Result is what applying (or previewing) the rules for one user did.
type Result struct {
	UserID string `json:"userId"`
	// Tenant is the landing tenant: the tenant of the first matching rule
	// ("" when no rule matched).
	Tenant  string   `json:"tenant,omitempty"`
	Targets []Target `json:"targets"`
	Changes []Change `json:"changes"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

// Plan evaluates rules in order against attrs. The first matching rule for a
// tenant decides its role and default rooms; later rules for the same tenant
// are ignored. A rule naming any role other than member or org_admin is
// skipped (Validate refuses it; this keeps a hand-edited setting from ever
// escalating).
func Plan(rules []Rule, attrs Attributes) []Target {
	out := []Target{}
	seen := map[string]bool{}
	for i, r := range rules {
		role, ok := grantableRole(r.Role)
		tid := strings.TrimSpace(r.Tenant)
		if !ok || tid == "" || seen[tid] || !r.Match.matches(attrs) {
			continue
		}
		seen[tid] = true
		out = append(out, Target{Tenant: tid, Role: role, DefaultRooms: cleanList(r.DefaultRooms), Rule: i})
	}
	return out
}

// grantableRole normalizes a rule role; empty means member. Anything that is
// not member or org_admin (super_admin included) is refused.
func grantableRole(v string) (string, bool) {
	switch strings.TrimSpace(v) {
	case "", RoleMember:
		return RoleMember, true
	case RoleOrgAdmin:
		return RoleOrgAdmin, true
	default:
		return "", false
	}
}

func (m Match) matches(a Attributes) bool {
	switch {
	case strings.TrimSpace(m.Group) != "":
		want := strings.TrimSpace(m.Group)
		for _, g := range a.Groups {
			if strings.EqualFold(strings.TrimSpace(g), want) {
				return true
			}
		}
		return false
	case strings.TrimSpace(m.Department) != "":
		return a.Department != "" && strings.EqualFold(strings.TrimSpace(a.Department), strings.TrimSpace(m.Department))
	case strings.TrimSpace(m.EmailDomain) != "":
		return strings.EqualFold(emailDomain(a.Email), strings.TrimPrefix(strings.TrimSpace(m.EmailDomain), "@"))
	default:
		return false
	}
}

func (m Match) keys() int {
	n := 0
	for _, v := range []string{m.Group, m.Department, m.EmailDomain} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	return n
}

func emailDomain(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at+1 >= len(email) {
		return ""
	}
	return strings.TrimSpace(email[at+1:])
}

func cleanList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ValidationError names the rule a rule set is refused for.
type ValidationError struct {
	Rule int
	Msg  string
}

func (e *ValidationError) Error() string {
	if e.Rule < 0 {
		return e.Msg
	}
	return fmt.Sprintf("rule %d: %s", e.Rule+1, e.Msg)
}

// Rooms is the room-store surface sync needs (store.RoomStore satisfies it).
type Rooms interface {
	RoomTenant(ctx context.Context, roomID string) (string, bool, error)
	IsMember(ctx context.Context, roomID, userID string) (bool, error)
	AddMember(ctx context.Context, m store.Membership) error
}

// SessionRevoker ends sign-ins (store.RefreshTokenStore satisfies it).
type SessionRevoker interface {
	RevokeRefreshFamilies(ctx context.Context, filter store.RefreshRevokeFilter, nowMs int64) (int, error)
}

// Syncer applies the persisted rules. Every field but Now is required; Audit
// may be nil (no audit trail, tests).
type Syncer struct {
	Settings store.PlatformSettingsStore
	Tenants  store.TenantStore
	Rooms    Rooms
	State    store.OrgSyncStore
	Sessions SessionRevoker
	Audit    store.ExtensionAuditStore
	Now      func() time.Time
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Rules returns the persisted rule set (empty when none was saved).
func (s *Syncer) Rules(ctx context.Context) (RuleSet, error) {
	row, ok, err := s.Settings.GetPlatformSetting(ctx, SettingKey)
	if err != nil || !ok {
		return RuleSet{Rules: []Rule{}}, err
	}
	var set RuleSet
	if err := json.Unmarshal([]byte(row.Value), &set); err != nil {
		return RuleSet{}, fmt.Errorf("org sync rules setting is corrupt: %w", err)
	}
	if set.Rules == nil {
		set.Rules = []Rule{}
	}
	set.UpdatedAtMs, set.UpdatedBy = row.UpdatedAtMs, row.UpdatedBy
	return set, nil
}

// Validate refuses a rule set that names a missing tenant, a room outside its
// rule's tenant, an ungrantable role, or a match without exactly one key.
func (s *Syncer) Validate(ctx context.Context, rules []Rule) error {
	if len(rules) > MaxRules {
		return &ValidationError{Rule: -1, Msg: fmt.Sprintf("at most %d rules", MaxRules)}
	}
	for i, r := range rules {
		if r.Match.keys() != 1 {
			return &ValidationError{Rule: i, Msg: "match exactly one of group, department, emailDomain"}
		}
		if _, ok := grantableRole(r.Role); !ok {
			return &ValidationError{Rule: i, Msg: fmt.Sprintf("role %q is not member or org_admin", r.Role)}
		}
		tid := strings.TrimSpace(r.Tenant)
		if tid == "" {
			return &ValidationError{Rule: i, Msg: "tenant is required"}
		}
		if _, err := s.Tenants.GetTenant(ctx, tid); err != nil {
			return &ValidationError{Rule: i, Msg: fmt.Sprintf("tenant %q does not exist", tid)}
		}
		rooms := cleanList(r.DefaultRooms)
		if len(rooms) > maxDefaultRooms {
			return &ValidationError{Rule: i, Msg: fmt.Sprintf("at most %d default rooms", maxDefaultRooms)}
		}
		for _, room := range rooms {
			rt, ok, err := s.Rooms.RoomTenant(ctx, room)
			if err != nil {
				return fmt.Errorf("org sync: resolve room %s: %w", room, err)
			}
			if !ok || rt != tid {
				return &ValidationError{Rule: i, Msg: fmt.Sprintf("room %q does not exist in tenant %q", room, tid)}
			}
		}
	}
	return nil
}

// SaveRules validates and persists a rule set, auditing the change.
func (s *Syncer) SaveRules(ctx context.Context, rules []Rule, actor string) (RuleSet, error) {
	if err := s.Validate(ctx, rules); err != nil {
		return RuleSet{}, err
	}
	clean := make([]Rule, 0, len(rules))
	for _, r := range rules {
		role, _ := grantableRole(r.Role)
		clean = append(clean, Rule{
			Match: Match{
				Group:       strings.TrimSpace(r.Match.Group),
				Department:  strings.TrimSpace(r.Match.Department),
				EmailDomain: strings.TrimPrefix(strings.TrimSpace(r.Match.EmailDomain), "@"),
			},
			Tenant: strings.TrimSpace(r.Tenant), Role: role, DefaultRooms: cleanList(r.DefaultRooms),
		})
	}
	raw, err := json.Marshal(RuleSet{Rules: clean})
	if err != nil {
		return RuleSet{}, err
	}
	at := s.now().UnixMilli()
	if err := s.Settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: SettingKey, Value: string(raw), UpdatedAtMs: at, UpdatedBy: actor}); err != nil {
		return RuleSet{}, err
	}
	s.audit(ctx, "rules", "", "rules", actor, fmt.Sprintf("%d rule(s)", len(clean)))
	return RuleSet{Rules: clean, UpdatedAtMs: at, UpdatedBy: actor}, nil
}

// Apply evaluates the persisted rules for one user and converges their
// sync-owned memberships, recording the attributes for later resyncs. With
// no rules saved, sync is off: nothing is changed or recorded. dryRun
// computes the same result without writing (the preview).
func (s *Syncer) Apply(ctx context.Context, attrs Attributes, actor string, dryRun bool) (Result, error) {
	attrs.UserID = strings.TrimSpace(attrs.UserID)
	if attrs.UserID == "" {
		return Result{}, errors.New("org sync: user id required")
	}
	set, err := s.Rules(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{UserID: attrs.UserID, Targets: Plan(set.Rules, attrs), Changes: []Change{}, DryRun: dryRun}
	if len(res.Targets) > 0 {
		res.Tenant = res.Targets[0].Tenant
	}
	if len(set.Rules) == 0 {
		return res, nil
	}
	if err := s.converge(ctx, attrs.UserID, &res, actor, dryRun); err != nil {
		return res, err
	}
	if !dryRun {
		if err := s.State.PutOrgSyncSubject(ctx, store.OrgSyncSubject{
			UserID: attrs.UserID, Email: attrs.Email, DisplayName: attrs.DisplayName,
			Department: attrs.Department, Groups: cleanList(attrs.Groups), SyncedAtMs: s.now().UnixMilli(),
		}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// converge moves the user's memberships to res.Targets: create missing sync
// memberships, fix the role of sync-owned ones, keep manual ones untouched,
// add default rooms once, and remove sync-owned memberships no rule targets
// (revoking the sign-ins that landed in that tenant).
func (s *Syncer) converge(ctx context.Context, userID string, res *Result, actor string, dryRun bool) error {
	memberships, err := s.Tenants.ListMembershipsForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("org sync: read memberships: %w", err)
	}
	current := map[string]store.TenantMember{}
	for _, m := range memberships {
		current[m.TenantID] = m
	}
	nowMs := s.now().UnixMilli()
	wanted := map[string]bool{}
	for _, t := range res.Targets {
		wanted[t.Tenant] = true
		existing, ok := current[t.Tenant]
		switch {
		case ok && existing.Source != store.MemberSourceSync:
			res.Changes = append(res.Changes, Change{Action: ActionKeepManual, Tenant: t.Tenant, Role: string(existing.Role)})
		case ok && string(existing.Role) == t.Role:
			// Already converged.
		case ok:
			res.Changes = append(res.Changes, Change{Action: ActionRole, Tenant: t.Tenant, Role: t.Role, FromRole: string(existing.Role)})
			if !dryRun {
				existing.Role = store.OrgRole(t.Role)
				if err := s.Tenants.PutMember(ctx, existing); err != nil {
					return fmt.Errorf("org sync: set role in %s: %w", t.Tenant, err)
				}
				s.audit(ctx, userID, t.Tenant, ActionRole, actor, string(existing.Role))
			}
		default:
			res.Changes = append(res.Changes, Change{Action: ActionAdd, Tenant: t.Tenant, Role: t.Role})
			if !dryRun {
				if err := s.Tenants.PutMember(ctx, store.TenantMember{
					TenantID: t.Tenant, UserID: userID, Role: store.OrgRole(t.Role), JoinedAtMs: nowMs, Source: store.MemberSourceSync,
				}); err != nil {
					return fmt.Errorf("org sync: add to %s: %w", t.Tenant, err)
				}
				s.audit(ctx, userID, t.Tenant, ActionAdd, actor, t.Role)
			}
		}
		if err := s.defaultRooms(ctx, userID, t, res, actor, dryRun); err != nil {
			return err
		}
	}
	// Removals in a stable order (map iteration is random).
	var stale []string
	for tid, m := range current {
		if m.Source == store.MemberSourceSync && !wanted[tid] {
			stale = append(stale, tid)
		}
	}
	sort.Strings(stale)
	for _, tid := range stale {
		ch := Change{Action: ActionRemove, Tenant: tid, FromRole: string(current[tid].Role)}
		if !dryRun {
			if err := s.Tenants.RemoveMember(ctx, tid, userID); err != nil {
				return fmt.Errorf("org sync: remove from %s: %w", tid, err)
			}
			n, err := s.Sessions.RevokeRefreshFamilies(ctx, store.RefreshRevokeFilter{Subject: userID, TenantID: tid}, nowMs)
			if err != nil {
				return fmt.Errorf("org sync: revoke sessions in %s: %w", tid, err)
			}
			ch.RevokedSessions = n
			s.audit(ctx, userID, tid, ActionRemove, actor, fmt.Sprintf("revoked %d session(s)", n))
		}
		res.Changes = append(res.Changes, ch)
	}
	return nil
}

// defaultRooms adds the user to the target's default rooms they were never
// added to by sync before. A room they already belong to is recorded without
// a change; a room they left after sync added them stays left.
func (s *Syncer) defaultRooms(ctx context.Context, userID string, t Target, res *Result, actor string, dryRun bool) error {
	tctx := tenant.NewContext(ctx, t.Tenant)
	for _, room := range t.DefaultRooms {
		done, err := s.State.DefaultRoomGranted(ctx, userID, room)
		if err != nil {
			return fmt.Errorf("org sync: default room %s: %w", room, err)
		}
		if done {
			continue
		}
		rt, ok, err := s.Rooms.RoomTenant(ctx, room)
		if err != nil {
			return fmt.Errorf("org sync: resolve room %s: %w", room, err)
		}
		if !ok || rt != t.Tenant {
			continue // deleted or moved since the rules were saved
		}
		member, err := s.Rooms.IsMember(tctx, room, userID)
		if err != nil {
			return fmt.Errorf("org sync: room %s membership: %w", room, err)
		}
		if !member {
			res.Changes = append(res.Changes, Change{Action: ActionRoom, Tenant: t.Tenant, RoomID: room})
		}
		if dryRun {
			continue
		}
		if !member {
			if err := s.Rooms.AddMember(tctx, store.Membership{RoomID: room, UserID: userID, Role: store.RoleMember, JoinedAtMs: s.now().UnixMilli()}); err != nil {
				return fmt.Errorf("org sync: add to room %s: %w", room, err)
			}
			s.audit(ctx, userID, t.Tenant, ActionRoom, actor, room)
		}
		if err := s.State.RecordDefaultRoom(ctx, userID, room, s.now().UnixMilli()); err != nil {
			return fmt.Errorf("org sync: record default room %s: %w", room, err)
		}
	}
	return nil
}

// ResyncResult summarizes a resync over every recorded subject.
type ResyncResult struct {
	Subjects int      `json:"subjects"`
	Changed  []Result `json:"changed"`
}

// Resync re-applies the current rules to every subject whose attributes a
// sign-in recorded, so a rule change takes effect without waiting for each
// member's next sign-in. Run it after editing rules or on a schedule.
func (s *Syncer) Resync(ctx context.Context, actor string) (ResyncResult, error) {
	subjects, err := s.State.ListOrgSyncSubjects(ctx)
	if err != nil {
		return ResyncResult{}, err
	}
	out := ResyncResult{Subjects: len(subjects), Changed: []Result{}}
	for _, sub := range subjects {
		res, err := s.Apply(ctx, Attributes{
			UserID: sub.UserID, Email: sub.Email, DisplayName: sub.DisplayName,
			Department: sub.Department, Groups: sub.Groups,
		}, actor, false)
		if err != nil {
			return out, fmt.Errorf("resync %s: %w", sub.UserID, err)
		}
		if len(res.Changes) > 0 {
			out.Changed = append(out.Changed, res)
		}
	}
	return out, nil
}

// DeprovisionResult is what deprovisioning one user did.
type DeprovisionResult struct {
	UserID          string   `json:"userId"`
	RemovedTenants  []string `json:"removedTenants"`
	RevokedSessions int      `json:"revokedSessions"`
}

// Deprovision ends a departed user's access: it removes every tenant
// membership (manual ones included — the directory says the person is gone),
// revokes all of their sign-ins, and forgets their recorded attributes so a
// resync cannot restore them. Room memberships are kept as history; without a
// tenant membership or a session they grant nothing until the user signs in
// again, which their identity provider no longer allows.
func (s *Syncer) Deprovision(ctx context.Context, userIDs []string, actor string) ([]DeprovisionResult, error) {
	ids := cleanList(userIDs)
	out := make([]DeprovisionResult, 0, len(ids))
	for _, uid := range ids {
		res := DeprovisionResult{UserID: uid, RemovedTenants: []string{}}
		members, err := s.Tenants.ListMembershipsForUser(ctx, uid)
		if err != nil {
			return out, fmt.Errorf("deprovision %s: read memberships: %w", uid, err)
		}
		sort.Slice(members, func(i, j int) bool { return members[i].TenantID < members[j].TenantID })
		for _, m := range members {
			if err := s.Tenants.RemoveMember(ctx, m.TenantID, uid); err != nil {
				return out, fmt.Errorf("deprovision %s: remove from %s: %w", uid, m.TenantID, err)
			}
			res.RemovedTenants = append(res.RemovedTenants, m.TenantID)
		}
		n, err := s.Sessions.RevokeRefreshFamilies(ctx, store.RefreshRevokeFilter{Subject: uid}, s.now().UnixMilli())
		if err != nil {
			return out, fmt.Errorf("deprovision %s: revoke sessions: %w", uid, err)
		}
		res.RevokedSessions = n
		if err := s.State.DeleteOrgSyncSubject(ctx, uid); err != nil {
			return out, fmt.Errorf("deprovision %s: %w", uid, err)
		}
		s.audit(ctx, uid, "", ActionDeprovision, actor,
			fmt.Sprintf("removed from [%s], revoked %d session(s)", strings.Join(res.RemovedTenants, ","), n))
		out = append(out, res)
	}
	return out, nil
}

func (s *Syncer) audit(ctx context.Context, name, tenantID, action, actor, detail string) {
	if s.Audit == nil {
		return
	}
	audit.Record(ctx, s.Audit, store.ExtAuditRecord{
		Kind: AuditKind, Name: name, Action: action, Actor: actor, TenantID: tenantID,
		Outcome: wire.AuditOutcomeOK, Detail: detail, AtMs: s.now().UnixMilli(),
	})
}
