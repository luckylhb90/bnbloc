package manifestcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

const fullProviderManifest = `{
  "name": "full-provider-test",
  "discover": { "probeUrl": "REPLACED", "probePath": "/v1/agent-targets" },
  "createWorkspace": { "method": "POST", "path": "/v1/workspaces",
    "body": {}, "extract": { "workspaceId": "workspace.id" } },
  "createSession": { "method": "POST", "path": "/v1/workspaces/{workspaceId}/agent-sessions",
    "body": {
      "agentSessionId": "{sessionId}",
      "agentTargetId": "{targetId}",
      "initialContent": [{ "type": "text", "text": "{prompt}" }],
      "agentOptions": { "model": "{model}", "reasoningEffort": "{reasoningEffort}" }
    },
    "extract": { "sessionId": "request.sessionId", "turnId": "session.activeTurn.turnId", "phase": "session.activeTurn.phase" } },
  "sendInput": { "method": "POST", "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/input",
    "body": { "content": [{ "type": "text", "text": "{text}" }], "agentOptions": { "model": "{model}", "reasoningEffort": "{reasoningEffort}" } } },
  "writeWorkspaceFile": { "method": "PUT", "path": "/v1/workspaces/{workspaceId}/room-file",
    "body": { "path": "{relPath}", "content": "{content}" } },
  "interrupt": { "method": "POST", "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/interrupt",
    "body": {} },
  "listTargets": { "method": "GET", "path": "/v1/agent-targets",
    "targets": { "list": "targets", "id": "id", "provider": "provider" } },
  "messages": { "method": "GET", "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/messages?order=asc&limit=100",
    "pathWhen": { "after": "&afterVersion={after}" },
    "items": {
      "list": "messages",
      "fields": {
        "version": "version",
        "role": "role",
        "text": "payload.text",
        "userVisible": "semantics.userVisibleAssistantResponse",
        "messageId": "messageId",
        "seq": "sequence",
        "createdAtMs": "occurredAtUnixMs"
      },
      "completedWhen": { "field": "status", "equalsAnyOf": ["", "completed"] }
    },
    "extract": { "latestVersion": "latestVersion" } },
  "pollTurn": { "method": "GET", "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}",
    "idleWhen": { "field": "session.activeTurn.phase", "notIn": ["running", "streaming", "working", "executing"] },
    "extract": { "failed": "session.latestTurn.outcome", "failMessage": "session.latestTurn.error.message", "failCode": "session.latestTurn.error.code" } }
}`

func TestManifestMessages_mapsReflowPayload_whenMessagesOpUsesItems(t *testing.T) {
	// Given
	var gotQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQueries = append(gotQueries, r.URL.RawQuery)
		if r.URL.Path != "/v1/workspaces/ws-1/agent-sessions/sess-1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"latestVersion": 8,
			"messages": []any{
				map[string]any{
					"role": "assistant", "version": 6, "status": "", "messageId": "m-6", "sequence": 60,
					"occurredAtUnixMs": float64(111),
					"payload":          map[string]any{"text": "first"},
					"semantics":        map[string]any{"userVisibleAssistantResponse": true},
				},
				map[string]any{
					"role": "assistant", "version": 7, "status": "completed", "messageId": "m-7", "sequence": 70,
					"occurredAtUnixMs": float64(222),
					"payload":          map[string]any{"text": "second"},
					"semantics":        map[string]any{"userVisibleAssistantResponse": true},
				},
				map[string]any{
					"role": "assistant", "version": 8, "status": "streaming", "messageId": "m-8", "sequence": 80,
					"occurredAtUnixMs": float64(333),
					"payload":          map[string]any{"text": "third"},
					"semantics":        map[string]any{"userVisibleAssistantResponse": false},
				},
			},
		})
	}))
	defer srv.Close()
	p, err := Parse([]byte(fullProviderManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// When
	msgs, latest, err := p.Messages(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-1", "sess-1", 5)

	// Then
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if latest != 8 {
		t.Fatalf("latest = %d; want 8", latest)
	}
	want := []core.Message{
		{Version: 6, Role: "assistant", Text: "first", UserVisible: true, Completed: true, MessageID: "m-6", Seq: 60, CreatedAtMs: 111},
		{Version: 7, Role: "assistant", Text: "second", UserVisible: true, Completed: true, MessageID: "m-7", Seq: 70, CreatedAtMs: 222},
		{Version: 8, Role: "assistant", Text: "third", UserVisible: false, Completed: false, MessageID: "m-8", Seq: 80, CreatedAtMs: 333},
	}
	if !messagesEqual(msgs, want) {
		t.Fatalf("messages = %+v; want %+v", msgs, want)
	}
	if len(gotQueries) != 1 || gotQueries[0] != "order=asc&limit=100&afterVersion=5" {
		t.Fatalf("query = %v; want afterVersion only for after > 0", gotQueries)
	}

	// When
	_, _, err = p.Messages(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-1", "sess-1", 0)

	// Then
	if err != nil {
		t.Fatalf("Messages after zero: %v", err)
	}
	if len(gotQueries) != 2 || gotQueries[1] != "order=asc&limit=100" {
		t.Fatalf("query = %v; want no afterVersion for after == 0", gotQueries)
	}
}

func TestManifestPollTurn_reportsFailedTurn_whenLatestTurnFailed(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{
				"activeTurn": nil,
				"latestTurn": map[string]any{
					"outcome": "failed",
					"error":   map[string]any{"code": "nimbus_turn_failed", "message": "runner exploded"},
				},
			},
		})
	}))
	defer srv.Close()
	p, err := Parse([]byte(fullProviderManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// When
	got, err := p.PollTurn(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-1", "sess-1")

	// Then
	if err != nil {
		t.Fatalf("PollTurn: %v", err)
	}
	want := core.TurnStatus{Idle: true, Failed: true, FailMessage: "runner exploded"}
	if got != want {
		t.Fatalf("PollTurn = %+v; want %+v", got, want)
	}
}

func TestManifestOptionalVerbs_sendWriteFileAndInterruptRequests(t *testing.T) {
	// Given
	var caps []capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		caps = append(caps, capture{method: r.Method, path: r.URL.Path, body: body})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p, err := Parse([]byte(fullProviderManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// When
	writer, ok := core.As[core.WorkspaceFileWriter](context.Background(), p, core.Endpoint{BaseURL: srv.URL}, core.CapabilityWriteFile)
	if !ok {
		t.Fatalf("manifest with a writeWorkspaceFile op must expose WorkspaceFileWriter")
	}
	if err := writer.WriteWorkspaceFile(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-1", ".room/history.md", "hello"); err != nil {
		t.Fatalf("WriteWorkspaceFile: %v", err)
	}
	if err := p.Interrupt(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-1", "sess-1"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// Then
	if len(caps) != 2 {
		t.Fatalf("captures = %d; want 2", len(caps))
	}
	if caps[0].method != http.MethodPut || caps[0].path != "/v1/workspaces/ws-1/room-file" || caps[0].body["path"] != ".room/history.md" || caps[0].body["content"] != "hello" {
		t.Fatalf("write capture = %+v", caps[0])
	}
	if caps[1].method != http.MethodPost || caps[1].path != "/v1/workspaces/ws-1/agent-sessions/sess-1/interrupt" {
		t.Fatalf("interrupt capture = %+v", caps[1])
	}
}

func messagesEqual(got, want []core.Message) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		// core.Message now carries a Metrics map (not comparable with ==), so
		// compare structurally.
		if !reflect.DeepEqual(got[i], want[i]) {
			return false
		}
	}
	return true
}
