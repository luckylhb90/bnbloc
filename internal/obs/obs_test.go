package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The middleware logs request tags and never emits secret-like fields.
func TestMiddlewareLogsTagsWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Middleware(logger, NewMetrics("test"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/rooms/room-1/agent-activity/session-state", nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	// A secret sneaking in via a header must not be logged.
	req.Header.Set("Authorization", "Bearer sk-SECRET-DO-NOT-LOG")
	h.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &entry); err != nil {
		t.Fatalf("log line not json: %q", line)
	}
	if entry["roomId"] != "room-1" || entry["sub"] != "alice" {
		t.Fatalf("log tags = %v, want room-1/alice", entry)
	}
	if strings.Contains(line, "sk-SECRET-DO-NOT-LOG") {
		t.Fatalf("secret leaked into logs: %s", line)
	}
}

// BE-17: every service server bounds header reads and idle connections.
func TestNewServerSetsTimeouts(t *testing.T) {
	srv := newServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("timeouts unset: header=%v idle=%v", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatal("whole-request timeouts would cut SSE streams and large uploads")
	}
}
