package store

import "testing"

// The selection persists per (user, room, channel) and restores exactly what
// was last written — the cross-device contract the composer relies on.
func TestAgentPrefsRoundTripPerRoomChannel(t *testing.T) {
	s := NewMemoryAgentPrefs()
	ctx := tenantCtx("default")

	if _, ok := s.GetAgentPrefs(ctx, "r1", "ch1", "u1"); ok {
		t.Fatal("absent prefs reported present")
	}
	if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: "r1", ChannelID: "ch1", Core: "nimbus", Provider: "codex", Model: "gpt-x", Reasoning: "high"}, "u1"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := s.GetAgentPrefs(ctx, "r1", "ch1", "u1")
	if !ok || got.Core != "nimbus" || got.Provider != "codex" || got.Model != "gpt-x" || got.Reasoning != "high" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}

	// Room-level (channel "") is a distinct row, and other users never see mine.
	if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: "r1", ChannelID: "", Core: "nimbus", Provider: "claude-code"}, "u1"); err != nil {
		t.Fatalf("put room-level: %v", err)
	}
	if got, _ := s.GetAgentPrefs(ctx, "r1", "", "u1"); got.Provider != "claude-code" {
		t.Fatalf("room-level = %+v", got)
	}
	if _, ok := s.GetAgentPrefs(ctx, "r1", "ch1", "u2"); ok {
		t.Fatal("user isolation broken: u2 sees u1 prefs")
	}

	// Upsert replaces, not duplicates.
	if err := s.PutAgentPrefs(ctx, AgentPrefs{RoomID: "r1", ChannelID: "ch1", Core: "nimbus", Provider: "opencode"}, "u1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, _ := s.GetAgentPrefs(ctx, "r1", "ch1", "u1"); got.Provider != "opencode" || got.Model != "" {
		t.Fatalf("upsert = %+v, want replaced row", got)
	}
}
