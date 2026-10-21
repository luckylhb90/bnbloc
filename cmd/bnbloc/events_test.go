package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// bnbloc events send posts the member's event (type, subject, data inline
// or from a file) to the room's events route with the API token's session
// and prints how many tasks it set; a refusal fails the command.
func TestEventsSend(t *testing.T) {
	isolateHome(t)
	var got []wire.MemberEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/api-token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": "sess-ci", "subject": "svc:ci", "tenant": "home", "role": "member"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer sess-ci" || r.Method != http.MethodPost || r.URL.Path != "/v1/rooms/room-1/events" {
			http.Error(w, `{"error":"no"}`, http.StatusForbidden)
			return
		}
		var in wire.MemberEvent
		_ = json.NewDecoder(r.Body).Decode(&in)
		got = append(got, in)
		if strings.HasPrefix(in.Type, "platform.") {
			http.Error(w, `{"error":"type is the platform's"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(wire.MemberEventReply{Event: wire.CloudEvent{ID: "evt_1", Type: in.Type}, Triggered: 2})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("BNBLOC_TOKEN", "")
	t.Setenv(apiTokenEnv, "bnbloc_at_ci")

	var out bytes.Buffer
	if err := eventsCmd([]string{"send", "--gateway", srv.URL, "room-1", "dev.finished", "--subject", "v1.4.0", "--data", `{"branch":"main"}`}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "sent dev.finished (evt_1): 2 task(s) set to run\n" {
		t.Fatalf("out = %q", out.String())
	}
	file := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(file, []byte(`{"build":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eventsCmd([]string{"send", "--gateway", srv.URL, "--data", "@" + file, "room-1", "ci.done"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Subject != "v1.4.0" || string(got[0].Data) != `{"branch":"main"}` || got[1].Type != "ci.done" || string(got[1].Data) != `{"build":42}` {
		t.Fatalf("sent = %+v", got)
	}
	if err := eventsCmd([]string{"send", "--gateway", srv.URL, "room-1", "x", "--data", "{nope"}, &out); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("bad data = %v", err)
	}
	if err := eventsCmd([]string{"send", "--gateway", srv.URL, "room-1", "platform.deployed"}, &out); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("a refused event = %v", err)
	}
	if err := eventsCmd([]string{"send", "--gateway", srv.URL, "room-1"}, &out); err == nil {
		t.Fatal("send without a type passed")
	}
}
