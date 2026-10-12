package wire

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite internal/wire/testdata golden fixtures")

// Golden fixtures pin the JSON the web client receives for room events whose
// payload has no TypeScript type of its own. The web's node tests run
// normalizeRoomEvent over the same files (web/src/lib/roomEvents.test.ts), so
// a field Go stops sending (or renames) fails on both sides. Regenerate with
// `go test ./internal/wire -run TestGoldenRoomEvents -update`.
func TestGoldenRoomEvents(t *testing.T) {
	fixtures := map[string]Event{
		// activity republishes the bridge's report as is (services/activity).
		"agent_session_state.json": {
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
	}
	for name, ev := range fixtures {
		got, err := json.MarshalIndent(ev, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		path := filepath.Join("testdata", name)
		if *updateGolden {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update to create it)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s drifted from the wire shape; run `go test ./internal/wire -run TestGoldenRoomEvents -update` and update the web reader.\n got: %s\nwant: %s", name, got, want)
		}
	}
}
