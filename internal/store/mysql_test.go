package store

import (
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// TestMySQLBackendsRoundTrip exercises the real MySQL stores end to end. It is
// gated on BNBLOC_TEST_MYSQL_DSN so it runs in CI/compose but skips locally.
//
//	BNBLOC_TEST_MYSQL_DSN='bnbloc:bnbloc-pass@tcp(127.0.0.1:3306)/bnbloc?parseTime=true' go test ./internal/store -run MySQL
func TestMySQLBackendsRoundTrip(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	defer b.DB.Close()

	now := time.Now().UnixMilli()
	roomID := "itest-" + itoaBase(now)

	// Room + membership persist and read back.
	if err := b.Room.CreateRoom(testCtx(), Room{RoomID: roomID, Name: "it", CreatedBy: "alice", CreatedAtMs: now}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if err := b.Room.AddMember(testCtx(), Membership{RoomID: roomID, UserID: "alice", Role: RoleOwner, JoinedAtMs: now}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if !b.Room.IsMember(testCtx(), roomID, "alice") {
		t.Fatal("expected alice to be a member after persistence")
	}
	if got := b.Room.RoomsForUser(testCtx(), "alice"); !contains(got, roomID) {
		t.Fatalf("RoomsForUser missing %s: %v", roomID, got)
	}

	// Room creation with its default channel is one transaction: both rows
	// land together, and the conditional rename cannot resurrect a deleted one.
	chanRoom := roomID + "-ch"
	def := RoomChannel{ChannelID: "ch_" + itoaBase(now), RoomID: chanRoom, Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: now}
	if err := b.Room.CreateRoomWithOwnerAndChannel(testCtx(), Room{RoomID: chanRoom, Name: "it2", CreatedBy: "alice", CreatedAtMs: now}, Membership{RoomID: chanRoom, UserID: "alice", Role: RoleOwner, JoinedAtMs: now}, def); err != nil {
		t.Fatalf("CreateRoomWithOwnerAndChannel: %v", err)
	}
	if got, ok := b.Channel.DefaultChannel(testCtx(), chanRoom); !ok || got.ChannelID != def.ChannelID || !got.IsDefault {
		t.Fatalf("default channel after transactional create = %+v,%v want %s default", got, ok, def.ChannelID)
	}
	if err := b.Channel.RenameChannel(testCtx(), chanRoom, def.ChannelID, "lobby"); err != nil {
		t.Fatalf("RenameChannel: %v", err)
	}
	if err := b.Channel.DeleteChannel(testCtx(), chanRoom, def.ChannelID, now); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if err := b.Channel.RenameChannel(testCtx(), chanRoom, def.ChannelID, "zombie"); err != ErrChannelNotFound {
		t.Fatalf("rename of deleted channel = %v, want ErrChannelNotFound", err)
	}

	// Activity monotonicity persists.
	if _, err := b.Activity.PutSessionState(testCtx(), wire.ReportSessionStateInput{WorkspaceID: roomID, AgentSessionID: "s1", MessageVersion: 5}); err != nil {
		t.Fatalf("PutSessionState: %v", err)
	}
	cur, _ := b.Activity.PutSessionState(testCtx(), wire.ReportSessionStateInput{WorkspaceID: roomID, AgentSessionID: "s1", MessageVersion: 2})
	if cur != 5 {
		t.Fatalf("stale write advanced version to %d, want 5", cur)
	}

	// A grant persists.
	g := Grant{GrantID: "g-" + itoaBase(now), RoomID: roomID, TargetID: "shared-agent:codex", OwnerUserID: "alice", GranteeUserID: "bob", MaxConcurrent: 1, CreatedAtMs: now}
	if err := b.Grant.PutGrant(testCtx(), g); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}
	if got, err := b.Grant.GetGrant(testCtx(), g.GrantID); err != nil || got.OwnerUserID != "alice" {
		t.Fatalf("GetGrant = %+v, %v", got, err)
	}

	// Platform settings persist independent of tenant scope.
	setting := PlatformSetting{Key: "updates.enabled", Value: "true", UpdatedAtMs: now, UpdatedBy: "root"}
	if err := b.Settings.PutPlatformSetting(testCtx(), setting); err != nil {
		t.Fatalf("PutPlatformSetting: %v", err)
	}
	got, ok, err := b.Settings.GetPlatformSetting(testCtx(), setting.Key)
	if err != nil {
		t.Fatalf("GetPlatformSetting: %v", err)
	}
	if !ok || got.Value != setting.Value || got.UpdatedBy != setting.UpdatedBy {
		t.Fatalf("platform setting = %+v,%v want %+v", got, ok, setting)
	}

	componentName := "itest-core-" + itoaBase(now)
	if err := b.ComponentInstalls.PutComponentInstall(testCtx(), "alice", "core", componentName, now); err != nil {
		t.Fatalf("PutComponentInstall alice: %v", err)
	}
	if err := b.ComponentInstalls.PutComponentInstall(testCtx(), "alice", "core", componentName, now+1); err != nil {
		t.Fatalf("PutComponentInstall alice repeated: %v", err)
	}
	if err := b.ComponentInstalls.PutComponentInstall(testCtx(), "bob", "core", componentName, now+2); err != nil {
		t.Fatalf("PutComponentInstall bob: %v", err)
	}
	initialInstalls, err := b.ComponentInstalls.ListComponentInstalls(testCtx(), "alice")
	if err != nil {
		t.Fatalf("ListComponentInstalls before delete: %v", err)
	}
	for _, install := range initialInstalls {
		if install.Kind == "core" && install.Name == componentName && install.InstalledAtMs != now {
			t.Fatalf("repeated install timestamp = %d, want original %d", install.InstalledAtMs, now)
		}
	}
	counts, err := b.ComponentInstalls.CountComponentInstalls(testCtx(), "core")
	if err != nil {
		t.Fatalf("CountComponentInstalls: %v", err)
	}
	if counts[componentName] != 2 {
		t.Fatalf("component install counts[%q] = %d, want 2 distinct subjects", componentName, counts[componentName])
	}
	if err := b.ComponentInstalls.DeleteComponentInstall(testCtx(), "alice", "core", componentName); err != nil {
		t.Fatalf("DeleteComponentInstall: %v", err)
	}
	installs, err := b.ComponentInstalls.ListComponentInstalls(testCtx(), "alice")
	if err != nil {
		t.Fatalf("ListComponentInstalls: %v", err)
	}
	for _, install := range installs {
		if install.Kind == "core" && install.Name == componentName {
			t.Fatalf("deleted install still listed: %+v", installs)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func itoaBase(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
