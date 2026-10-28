package room

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// openPeopleChat opens (lazily creating) a room's people chat as user.
func (e *handoffEnv) openPeopleChat(roomID, user string) wire.RoomChannel {
	e.t.Helper()
	var ch wire.RoomChannel
	if err := json.Unmarshal(e.must(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/people-chat", user, nil), &ch); err != nil {
		e.t.Fatal(err)
	}
	return ch
}

// The people chat is opened lazily by any member, is one per room, never
// appears in the channel list (which the bridge pages history from), and is
// neither renamable nor deletable.
func TestPeopleChatLifecycle(t *testing.T) {
	e := newHandoffEnv(t)
	roomID, _ := seedRoom(e)
	// Reading never creates: 404 until a member opens it.
	if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/people-chat", "alice", nil); code != http.StatusNotFound {
		t.Fatalf("get before open = %d, want 404", code)
	}
	pc := e.openPeopleChat(roomID, "bob")
	var got wire.RoomChannel
	if err := json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/people-chat", "alice", nil), &got); err != nil || got.ChannelID != pc.ChannelID {
		t.Fatalf("get after open = %+v %v", got, err)
	}
	if pc.Kind != "people" || pc.IsDefault || pc.ChannelID == "" {
		t.Fatalf("people chat = %+v", pc)
	}
	if again := e.openPeopleChat(roomID, "alice"); again.ChannelID != pc.ChannelID {
		t.Fatalf("second open = %s, want the same channel %s", again.ChannelID, pc.ChannelID)
	}
	if code, _ := e.do(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/people-chat", "mallory", nil); code != http.StatusForbidden {
		t.Fatalf("non-member open = %d, want 403", code)
	}
	for _, c := range e.channels(roomID, "alice") {
		if c.ChannelID == pc.ChannelID || c.Kind != "" {
			t.Fatalf("channel list includes the people chat: %+v", c)
		}
	}
	if code, _ := e.do(e.roomSrv, http.MethodPatch, "/v1/rooms/"+roomID+"/channels/"+pc.ChannelID, "alice", map[string]any{"name": "x"}); code != http.StatusConflict {
		t.Fatalf("rename people chat = %d, want 409", code)
	}
	if code, _ := e.do(e.roomSrv, http.MethodDelete, "/v1/rooms/"+roomID+"/channels/"+pc.ChannelID, "alice", nil); code != http.StatusConflict {
		t.Fatalf("delete people chat = %d, want 409", code)
	}
}

// A handoff export carries the people chat marked kind=people (and labels it
// in the transcript); an import restores it as the new room's people chat.
func TestPeopleChatHandoffRoundTrip(t *testing.T) {
	e := newHandoffEnv(t)
	roomID, _ := seedRoom(e)
	pc := e.openPeopleChat(roomID, "bob")
	e.chat(roomID, "bob", map[string]any{"text": "between us humans", "channelId": pc.ChannelID})

	files := readZip(t, e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/export", "alice", nil))
	var man wire.RoomExportManifest
	if err := json.Unmarshal(files["manifest.json"], &man); err != nil {
		t.Fatal(err)
	}
	var marked int
	for _, c := range man.Channels {
		if c.Kind == wire.RoomExportChannelPeople {
			marked++
			if c.ID != pc.ChannelID {
				t.Fatalf("people channel id = %s, want %s", c.ID, pc.ChannelID)
			}
		}
	}
	if marked != 1 {
		t.Fatalf("manifest channels = %+v, want one kind=people", man.Channels)
	}
	if tr := string(files["transcript.md"]); !strings.Contains(tr, "people chat") || !strings.Contains(tr, "between us humans") {
		t.Fatalf("transcript lacks the labeled people chat:\n%s", tr)
	}

	code, out := e.importPackage("dave", writeZip(t, files))
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, out)
	}
	var res wire.RoomImportResult
	_ = json.Unmarshal(out, &res)
	restored := e.openPeopleChat(res.RoomID, "dave")
	hist := e.history(res.RoomID, "dave", restored.ChannelID)
	if len(hist) != 1 || !strings.Contains(string(hist[0].Payload), "between us humans") {
		t.Fatalf("restored people chat history = %+v", hist)
	}
	for _, c := range e.channels(res.RoomID, "dave") {
		if c.ChannelID == restored.ChannelID {
			t.Fatal("restored people chat is listed as an ordinary channel")
		}
	}
}

// A package whose people chat holds agent traffic, or that declares two
// people chats, is refused.
func TestPeopleChatImportRejectsForgedPackages(t *testing.T) {
	e := newHandoffEnv(t)
	manifest := func(chs ...wire.RoomExportChannel) []byte {
		b, _ := json.Marshal(wire.RoomExportManifest{
			Format: wire.RoomExportFormat, ExportedAtMs: 1, ExportedBy: "alice",
			Room:     wire.RoomExportRoom{ID: "src", Name: "Source"},
			Channels: append([]wire.RoomExportChannel{{ID: "ch_a", Name: "general", IsDefault: true}}, chs...),
		})
		return b
	}
	people := wire.RoomExportChannel{ID: "pc_1", Name: "People chat", Kind: wire.RoomExportChannelPeople}
	cases := map[string]map[string][]byte{
		"agent recipient": {
			"manifest.json":  manifest(people),
			"messages.jsonl": []byte(`{"sequence":1,"channelId":"pc_1","kind":"chat_message","payload":{"userId":"alice","text":"hi","recipient":{"ownerId":"bob","provider":"codex"}},"atUnixMs":1}`),
		},
		"agent reply": {
			"manifest.json":  manifest(people),
			"messages.jsonl": []byte(`{"sequence":1,"channelId":"pc_1","kind":"agent_messages","payload":{"messages":[]},"atUnixMs":1}`),
		},
		"two people chats": {
			"manifest.json":  manifest(people, wire.RoomExportChannel{ID: "pc_2", Name: "Again", Kind: wire.RoomExportChannelPeople}),
			"messages.jsonl": nil,
		},
		"unknown kind": {
			"manifest.json":  manifest(wire.RoomExportChannel{ID: "x", Name: "x", Kind: "robots"}),
			"messages.jsonl": nil,
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if code, out := e.importPackage("dave", writeZip(t, files)); code != http.StatusBadRequest {
				t.Fatalf("import = %d %s, want 400", code, out)
			}
		})
	}
}
