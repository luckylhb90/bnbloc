package store

import (
	"testing"
	"time"
)

var (
	_ AgentPrefsStore = (*MemoryAgentPrefs)(nil)
	_ AgentPrefsStore = (*MySQLAgentPrefs)(nil)
)

func TestMemoryAgentPrefs(t *testing.T) {
	exerciseAgentPrefs(t, NewMemoryAgentPrefs(), "r1")
}

func TestMySQLAgentPrefs(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	room := "uap_it" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM user_agent_prefs WHERE room_id = ?`, room)
		_ = b.DB.Close()
	})
	exerciseAgentPrefs(t, b.AgentPrefs, room)
}

// The selection persists per (user, room, channel) and restores exactly what
// was last written, owner included — the cross-device contract the composer
// relies on.
func exerciseAgentPrefs(t *testing.T, s AgentPrefsStore, room string) {
	t.Helper()
	ctx := tenantCtx("default")

	if _, ok := s.GetAgentPrefs(ctx, room, "ch1", "u1"); ok {
		t.Fatal("absent prefs reported present")
	}
	if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: room, ChannelID: "ch1", OwnerID: "Jack", Core: "nimbus", Provider: "codex", Model: "gpt-x", Reasoning: "high", UpdatedAtMs: 1}, "u1"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := s.GetAgentPrefs(ctx, room, "ch1", "u1")
	if !ok || got.OwnerID != "Jack" || got.Core != "nimbus" || got.Provider != "codex" || got.Model != "gpt-x" || got.Reasoning != "high" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}

	// Room-level (channel "") is a distinct row, and other users never see mine.
	if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: room, ChannelID: "", OwnerID: "u1", Core: "nimbus", Provider: "claude-code", UpdatedAtMs: 2}, "u1"); err != nil {
		t.Fatalf("put room-level: %v", err)
	}
	if got, _ := s.GetAgentPrefs(ctx, room, "", "u1"); got.Provider != "claude-code" || got.OwnerID != "u1" {
		t.Fatalf("room-level = %+v", got)
	}
	if _, ok := s.GetAgentPrefs(ctx, room, "ch1", "u2"); ok {
		t.Fatal("user isolation broken: u2 sees u1 prefs")
	}

	// Upsert replaces, not duplicates, owner included.
	if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: room, ChannelID: "ch1", OwnerID: "u1", Core: "nimbus", Provider: "opencode", UpdatedAtMs: 3}, "u1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, _ := s.GetAgentPrefs(ctx, room, "ch1", "u1"); got.Provider != "opencode" || got.Model != "" || got.OwnerID != "u1" {
		t.Fatalf("upsert = %+v, want replaced row", got)
	}
}
