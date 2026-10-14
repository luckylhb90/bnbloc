package roomtools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

// writtenWorkspace records the files read_attachment saves.
type writtenWorkspace struct{ files map[string][]byte }

func (w *writtenWorkspace) ListFiles(context.Context) ([]core.SessionFile, error) { return nil, nil }
func (w *writtenWorkspace) ReadFile(context.Context, string) ([]byte, error)      { return nil, nil }
func (w *writtenWorkspace) WriteFile(_ context.Context, rel string, data []byte) error {
	w.files[rel] = data
	return nil
}

// attachmentGateway serves room-1's channels, a history whose messages
// carry files (one in the people chat, which is never listed), and the
// files themselves.
func attachmentGateway() *fakeGateway {
	return &fakeGateway{handle: func(method, path string, _ []byte) (int, any) {
		switch {
		case path == "/v1/rooms/room-1/channels":
			return 200, map[string]any{"channels": []any{map[string]any{"channelId": "ch_general", "name": "general"}}}
		case strings.HasPrefix(path, "/v1/rooms/room-1/history") && strings.Contains(path, "channelId=ch_general"):
			return 200, map[string]any{"messages": []any{
				map[string]any{"sequence": 5, "kind": "chat_message", "payload": map[string]any{"userId": "maya", "text": "the brief and the deck",
					"attachments": []any{
						map[string]any{"id": "att_text", "filename": "brief.md", "contentType": "text/markdown", "size": 18},
						map[string]any{"id": "att_bin", "filename": "Deck.PPTX", "contentType": "application/vnd.openxmlformats-officedocument.presentationml.presentation", "size": 4},
					}}},
			}}
		case path == "/v1/rooms/room-1/attachments/att_text":
			return 200, []byte("# Brief\nprice: 99\n")
		case path == "/v1/rooms/room-1/attachments/att_bin":
			return 200, []byte("PK\x03\x04")
		case path == "/v1/rooms/room-1/attachments/att_people":
			return 200, []byte("people only")
		}
		return 404, map[string]any{"error": "no route"}
	}}
}

// Any agent of the room reads a file a person attached: text inline and
// fenced, any other file saved into its workspace; a file no record shows
// (the people chat's) is refused before it is downloaded.
func TestReadAttachment(t *testing.T) {
	gw := attachmentGateway()
	srv, token, sess := startServer(t, gw)
	ws := &writtenWorkspace{files: map[string][]byte{}}
	sess.Bind(Job{JobID: "job_1", ChannelID: "ch_general", Workspace: ws})

	r := post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "read_attachment", "arguments": map[string]any{"attachmentId": "att_text"}}), nil)
	text, isErr := toolText(t, r)
	if isErr || !strings.Contains(text, `price: 99`) || !strings.Contains(text, "untrusted-content") || !strings.Contains(text, `"attachedBy":"maya"`) {
		t.Fatalf("text attachment = %s", text)
	}

	r = post(t, srv, token, rpc(2, "tools/call", map[string]any{"name": "read_attachment", "arguments": map[string]any{"attachmentId": "att_bin"}}), nil)
	text, isErr = toolText(t, r)
	if isErr || !strings.Contains(text, `"path":".room/attachments/att_bin.pptx"`) || string(ws.files[".room/attachments/att_bin.pptx"]) != "PK\x03\x04" {
		t.Fatalf("binary attachment = %s (files %v)", text, ws.files)
	}

	r = post(t, srv, token, rpc(3, "tools/call", map[string]any{"name": "read_attachment", "arguments": map[string]any{"attachmentId": "att_people"}}), nil)
	if text, isErr = toolText(t, r); !isErr || !strings.Contains(text, "not a file attached to a message you can read") {
		t.Fatalf("people-chat attachment = %s", text)
	}
	if calls := gw.callsTo(http.MethodGet, "/v1/rooms/room-1/attachments/att_people"); len(calls) != 0 {
		t.Fatalf("a file no record shows was downloaded: %v", calls)
	}
}
