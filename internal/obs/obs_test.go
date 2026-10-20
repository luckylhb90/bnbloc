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

// Capability tokens in a path (invite codes, relay keys, site tickets,
// share tokens, previewer tickets) never reach the request log; the rest
// of the path does.
func TestRedactPathHidesCapabilityTokens(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/invites/CODE-SECRET/redeem":                           "/v1/invites/[REDACTED]/redeem",
		"/relay/stream/KEY-SECRET":                                 "/relay/stream/[REDACTED]",
		"/v1/sites/TICKET-SECRET/~app/c/votes":                     "/v1/sites/[REDACTED]/~app/c/votes",
		"/v1/sites/TICKET-SECRET":                                  "/v1/sites/[REDACTED]",
		"/v1/public/shares/SHARE-SECRET/site/~app/events":          "/v1/public/shares/[REDACTED]/site/~app/events",
		"/v1/public/shares/SHARE-SECRET/visitor":                   "/v1/public/shares/[REDACTED]/visitor",
		"/v1/previewers/sprites/1.0.0/TICKET-SECRET/index.html":    "/v1/previewers/sprites/1.0.0/[REDACTED]/index.html",
		"/v1/settings-pages/mcp/notes/1.0.0/TICKET-SECRET/page.js": "/v1/settings-pages/mcp/notes/1.0.0/[REDACTED]/page.js",
		"/s/SHARESECRET00000000000":                                "/s/[REDACTED]",
		"/v1/rooms/room-1/s/x":                                     "/v1/rooms/room-1/s/x",
		"/v1/rooms/room-1/apps/art_1/c/votes":                      "/v1/rooms/room-1/apps/art_1/c/votes",
	} {
		if got := redactPath(path); got != want || strings.Contains(got, "SECRET") {
			t.Errorf("redactPath(%q) = %q, want %q", path, got, want)
		}
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
