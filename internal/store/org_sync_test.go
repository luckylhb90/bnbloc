package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// Organization sync state and membership sources round-trip on both backends.
func TestOrgSyncStoreAndMemberSource(t *testing.T) {
	ctx := context.Background()
	backends := map[string]*Backends{}
	mem, err := NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	backends["memory"] = mem
	if dsn := sharedMySQLDSN(t); dsn != "" {
		my, err := NewBackends(dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer my.DB.Close()
		backends["mysql"] = my
	}
	for name, b := range backends {
		t.Run(name, func(t *testing.T) {
			suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
			user, tid := "os-user-"+suffix, "os-t-"+suffix

			// A sync membership keeps its source; an unset source reads as manual.
			if err := b.Tenant.PutMember(ctx, TenantMember{TenantID: tid, UserID: user, Role: OrgRoleMember, JoinedAtMs: 1, Source: MemberSourceSync}); err != nil {
				t.Fatal(err)
			}
			if err := b.Tenant.PutMember(ctx, TenantMember{TenantID: tid + "-b", UserID: user, Role: OrgRoleAdmin, JoinedAtMs: 2}); err != nil {
				t.Fatal(err)
			}
			got := map[string]TenantMember{}
			for _, m := range b.Tenant.ListMembershipsForUser(ctx, user) {
				got[m.TenantID] = m
			}
			if got[tid].Source != MemberSourceSync || got[tid+"-b"].Source != MemberSourceManual || len(got) != 2 {
				t.Fatalf("memberships = %+v", got)
			}
			// An admin upsert takes the row over (source follows the write).
			if err := b.Tenant.PutMember(ctx, TenantMember{TenantID: tid, UserID: user, Role: OrgRoleAdmin, JoinedAtMs: 1, Source: MemberSourceManual}); err != nil {
				t.Fatal(err)
			}
			if m, _, _ := b.Tenant.GetMember(ctx, tid, user); m.Source != MemberSourceManual || m.Role != OrgRoleAdmin {
				t.Fatalf("after manual upsert = %+v", m)
			}
			if ms := b.Tenant.ListMembers(ctx, tid); len(ms) != 1 || ms[0].Source != MemberSourceManual {
				t.Fatalf("list members = %+v", ms)
			}

			// Subjects upsert and delete.
			sub := OrgSyncSubject{UserID: user, Email: "e@x", Department: "R&D", Groups: []string{"a", "b"}, SyncedAtMs: 5}
			if err := b.OrgSync.PutOrgSyncSubject(ctx, sub); err != nil {
				t.Fatal(err)
			}
			sub.Groups, sub.SyncedAtMs = nil, 6
			if err := b.OrgSync.PutOrgSyncSubject(ctx, sub); err != nil {
				t.Fatal(err)
			}
			found := false
			subs, err := b.OrgSync.ListOrgSyncSubjects(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range subs {
				if s.UserID == user {
					found = s.SyncedAtMs == 6 && len(s.Groups) == 0 && s.Department == "R&D"
				}
			}
			if !found {
				t.Fatalf("subject not upserted: %+v", subs)
			}
			if err := b.OrgSync.DeleteOrgSyncSubject(ctx, user); err != nil {
				t.Fatal(err)
			}

			// Default rooms are recorded once.
			if done, err := b.OrgSync.DefaultRoomGranted(ctx, user, "room-"+suffix); err != nil || done {
				t.Fatalf("granted before record = %v, %v", done, err)
			}
			for i := 0; i < 2; i++ {
				if err := b.OrgSync.RecordDefaultRoom(ctx, user, "room-"+suffix, 7); err != nil {
					t.Fatal(err)
				}
			}
			if done, err := b.OrgSync.DefaultRoomGranted(ctx, user, "room-"+suffix); err != nil || !done {
				t.Fatalf("granted after record = %v, %v", done, err)
			}
		})
	}
}
