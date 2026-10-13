package wire

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite internal/wire/testdata golden fixtures")

// Golden fixtures pin the JSON the web client receives for room events and
// history rows whose payload is free-form (no generated type covers the
// payload's inside). The web's node tests run normalizeRoomEvent over the
// same files (web/src/lib/roomEvents.test.ts), so a field Go stops sending
// (or renames) fails on both sides. Regenerate with
// `go test ./internal/wire -run TestGoldenRoomEvents -update`.
func TestGoldenRoomEvents(t *testing.T) {
	edited := int64(1760000005000)
	fixtures := map[string]any{
		// activity republishes the bridge's report as is (services/activity).
		"agent_session_state.json": Event{
			Sequence: 0,
			RoomID:   "room-1",
			Kind:     "agent_session_state",
			AtUnixMs: 1760000000000,
			Payload: ReportSessionStateInput{
				WorkspaceID:      "room-1",
				AgentSessionID:   "sess-1",
				OwnerUserID:      "maya",
				Provider:         "codex",
				Core:             "acme",
				State:            map[string]any{"phase": "working"},
				MessageVersion:   1760000000000,
				OccurredAtUnixMs: 1760000000000,
			},
		},
		// A reply whose core reported only part of its metrics: the keys
		// TurnUsageFromMetrics treats as optional are absent, and the reply
		// is still billed, so the web still shows its usage.
		"agent_messages.json": Event{
			Sequence:  12,
			RoomID:    "room-1",
			ChannelID: "chan-1",
			Kind:      KindAgentMessages,
			AtUnixMs:  1760000001000,
			Payload: ReportMessagesInput{
				WorkspaceID:    "room-1",
				AgentSessionID: "sess-1",
				ChannelID:      "chan-1",
				Cursor:         3,
				Messages: []Message{{
					Seq:             3,
					CreatedAtUnixMs: 1760000001000,
					Payload: map[string]any{
						"role": "assistant", "text": "Done.", "ownerUserId": "maya", "provider": "codex", "core": "acme",
						"jobId": "job-1", "sourceSeq": 11,
						"metrics": map[string]any{"model": "m-1", "inputTokens": 1200, "outputTokens": 340, "costUsd": 0.0042},
					},
				}},
			},
		},
		// A failed turn's notice: bnbloc's sentence as its text, the agent's
		// own error output as its detail (rendered fenced, never a reply).
		"job_notice.json": Event{
			Sequence:  13,
			RoomID:    "room-1",
			ChannelID: "chan-1",
			Kind:      KindAgentMessages,
			AtUnixMs:  1760000003000,
			Payload: JobNotice{
				Kind: "turn-failed", JobID: "job-1", SourceSeq: 11, Text: "The codex agent reported an error and stopped.",
				Detail: "Missing environment variable: `EXAMPLE_API_KEY`", OwnerUserID: "maya", Provider: "codex",
				FailReason: "agent_error", RoomID: "room-1", ChannelID: "chan-1", AtUnixMs: 1760000003000,
			}.Report(),
		},
		// A history page (GET .../history) with a chat message edited since
		// it was posted: the edit stamp rides the row, not the payload.
		"history_page.json": FeedPage{Messages: []FeedMessage{{
			Sequence:   11,
			ChannelID:  "chan-1",
			Kind:       KindChatMessage,
			Payload:    json.RawMessage(`{"userId":"maya","text":"Please fix the build","clientMsgId":"c-1"}`),
			AtUnixMs:   1760000000500,
			EditedAtMs: &edited,
		}}},
		// The job projection rides the job events (decision O4).
		"agent_job_updated.json": Event{
			Sequence:  0,
			RoomID:    "room-1",
			ChannelID: "chan-1",
			Kind:      "agent_job_updated",
			AtUnixMs:  1760000002000,
			Payload: AgentJob{
				JobID: "job-1", RoomID: "room-1", ChannelID: "chan-1", MessageSeq: 11,
				RequesterUserID: "jack", OwnerUserID: "maya", Core: "acme", Provider: "codex",
				MessageText: "Please fix the build", Status: "failed", FailureReason: "runner_failed",
				Attempt: 1, CreatedAtMs: 1760000000500, UpdatedAtMs: 1760000002000, StartedAtMs: 1760000001000,
				FinishedAtMs: 1760000002000, Error: "the core exited", ContentMissing: []string{"lint@^1"},
			},
		},
	}
	for name, v := range fixtures {
		got, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		golden(t, name, append(got, '\n'))
	}
}

// TestGoldenTurnUsage pins how a reply's free-form metrics map reads as
// usage (TurnUsageFromMetrics). The web reads the same cases
// (readTurnMetrics in web/src/lib/roomEvents.ts) and must agree, so a reply
// is billed and shown alike.
func TestGoldenTurnUsage(t *testing.T) {
	type tcase struct {
		Name    string         `json:"name"`
		Metrics map[string]any `json:"metrics"`
		Usage   struct {
			Model        string   `json:"model"`
			Reported     bool     `json:"reported"`
			InputTokens  int64    `json:"inputTokens"`
			OutputTokens int64    `json:"outputTokens"`
			CostUsd      *float64 `json:"costUsd"`
		} `json:"usage"`
	}
	inputs := []struct {
		name    string
		metrics map[string]any
	}{
		{"full", map[string]any{"model": "m-1", "reported": true, "inputTokens": 1200, "outputTokens": 340, "cacheCreationTokens": 10, "cacheReadTokens": 20, "thinkingTokens": 5, "durationApiMs": 900, "ttftMs": 300, "costUsd": 0.0042}},
		{"partial", map[string]any{"model": "m-1", "inputTokens": 1200, "outputTokens": 340}},
		{"not reported", map[string]any{"model": "m-2", "reported": false, "inputTokens": 99}},
		{"negative counts", map[string]any{"inputTokens": -5, "outputTokens": 7, "costUsd": -1}},
		{"empty", map[string]any{}},
	}
	var cases []tcase
	for _, in := range inputs {
		// Round-trip the input as the reply's JSON (numbers become float64).
		raw, _ := json.Marshal(in.metrics)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		u := TurnUsageFromMetrics(m)
		c := tcase{Name: in.name, Metrics: m}
		c.Usage.Model, c.Usage.Reported, c.Usage.InputTokens, c.Usage.OutputTokens, c.Usage.CostUsd = u.Model, u.Reported, u.InputTokens, u.OutputTokens, u.CostUsd
		cases = append(cases, c)
	}
	got, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "turn_usage.json", append(got, '\n'))
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update to create it)", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s drifted from the wire shape; run `go test ./internal/wire -update` and update the web reader.\n got: %s\nwant: %s", name, got, want)
	}
}

// The member-facing projections never carry the server-side fields decision
// O4 keeps out of them (a tenant, the claiming bridge, raw diagnostics, a
// blob key). A field added to a projection under one of these names fails
// here, not in a member's browser.
func TestProjectionsOmitServerFields(t *testing.T) {
	forbidden := []string{"tenantId", "leaseBridgeId", "leaseExpiresAtMs", "diagnostics", "blobKey", "thumbnailBlobKey"}
	for _, v := range []any{AgentJob{}, Artifact{}, ArtifactVersion{}, RoomSummary{}, RoomDetail{}, RoomPreview{}, RoomMember{}, PlazaRoom{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			for _, f := range forbidden {
				if name == f {
					t.Errorf("%s.%s carries %q, a server-side field", typ.Name(), typ.Field(i).Name, f)
				}
			}
		}
	}
}
