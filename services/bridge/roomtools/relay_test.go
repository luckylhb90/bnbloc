package roomtools

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
)

// relayPost posts one relayed call with the session's bearer.
func relayPost(t *testing.T, srv *Server, token, path string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.RelayURL()+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// The room relay reaches only its routes, as the job the session drives
// NOW: the grant follows each turn's Bind (a session's second turn acts for
// its own job, not the first's), the tool can neither name nor change it,
// and a revoked bearer is refused.
func TestRoomRelay(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		return 200, map[string]any{"images": []any{}}
	}}
	srv, token, sess := startServer(t, gw)

	if code, _ := relayPost(t, srv, "", "/v1/media/image", []byte(`{}`)); code != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d", code)
	}
	if code, _ := relayPost(t, srv, token, "/v1/media/image", []byte(`{}`)); code != http.StatusConflict {
		t.Fatalf("unbound = %d; want 409 (no turn running)", code)
	}
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	if code, _ := relayPost(t, srv, token, "/v1/rooms/room-1/messages", []byte(`{}`)); code != http.StatusNotFound {
		t.Fatalf("a route off the list = %d; want 404", code)
	}
	// The tool names only a job its session drove; the relay sets the room.
	if code, _ := relayPost(t, srv, token, "/v1/media/image?job=other&room=room-9", []byte(`{}`)); code != http.StatusForbidden {
		t.Fatalf("another job = %d; want 403", code)
	}
	if code, _ := relayPost(t, srv, token, "/v1/media/image?room=room-9", []byte(`{}`)); code != http.StatusOK {
		t.Fatalf("relayed = %d", code)
	}
	sess.Bind(Job{JobID: "job-2", RequesterUserID: "maya"})
	if code, _ := relayPost(t, srv, token, "/v1/media/image", []byte(`{"prompt":"a cat"}`)); code != http.StatusOK {
		t.Fatalf("second turn = %d", code)
	}
	// An earlier job of the session, named (a view outliving its turn).
	if code, _ := relayPost(t, srv, token, "/v1/media/image?job=job-1", []byte(`{}`)); code != http.StatusOK {
		t.Fatalf("an earlier job = %d", code)
	}
	calls := gw.callsTo(http.MethodPost, "/v1/media/image")
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.HasSuffix(calls[0].Path, "?job=job-1&room=room-1") || !strings.HasSuffix(calls[1].Path, "?job=job-2&room=room-1") || !strings.HasSuffix(calls[2].Path, "?job=job-1&room=room-1") {
		t.Fatalf("grants = %s ; %s ; %s", calls[0].Path, calls[1].Path, calls[2].Path)
	}
	var sent RawBody
	if err := json.Unmarshal(calls[1].Body, &sent); err != nil || sent.ContentType != "application/json" || !strings.Contains(string(sent.Data), "a cat") {
		t.Fatalf("body = %s (%v)", calls[1].Body, err)
	}
	srv.Revoke(sess)
	if code, _ := relayPost(t, srv, token, "/v1/media/image", []byte(`{}`)); code != http.StatusUnauthorized {
		t.Fatalf("revoked = %d", code)
	}
}

// GET <relay>/session answers the bound turn's facts from the bridge
// itself (never the gateway): who the calls act for, whose agent it is, the
// prompting message and whether its requester typed it. Unbound: 409.
func TestRoomRelaySession(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) { return 200, map[string]any{} }}
	srv, token, sess := startServer(t, gw)
	get := func(tok string) (int, component.RelaySession) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.RelayURL()+component.RelaySessionPath, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out component.RelaySession
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := get(""); code != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d", code)
	}
	if code, _ := get(token); code != http.StatusConflict {
		t.Fatalf("unbound = %d; want 409", code)
	}
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "ch-1", MessageSeq: 7, ByRequester: true})
	code, got := get(token)
	want := component.RelaySession{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", Requester: "maya", Owner: "jack", MessageSeq: 7, ByRequester: true}
	if code != http.StatusOK || got != want {
		t.Fatalf("session = %d %+v; want %+v", code, got, want)
	}
	if len(gw.callsTo(http.MethodGet, component.RelaySessionPath)) != 0 {
		t.Fatal("the session route reached the gateway")
	}
}

// A revoked session's bearer still posts the live view of a job it drove
// (a sign-in waits on it after the agent's session was released, PRD
// §3.7), and nothing else; once the gateway refuses the view, the bearer
// is refused for it too.
func TestRoomRelayRetiredSessionKeepsItsLiveView(t *testing.T) {
	refuse := false
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		if refuse {
			return http.StatusConflict, map[string]any{"error": "job is not running"}
		}
		return 200, map[string]any{}
	}}
	srv, token, sess := startServer(t, gw)
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	srv.Revoke(sess)

	if code, _ := relayPost(t, srv, token, "/v1/live?job=job-1", []byte(`{"waiting":"sign in"}`)); code != http.StatusOK {
		t.Fatalf("retired live post = %d", code)
	}
	if calls := gw.callsTo(http.MethodPost, "/v1/live"); len(calls) != 1 || !strings.HasSuffix(calls[0].Path, "?job=job-1&room=room-1") {
		t.Fatalf("calls = %+v", calls)
	}
	for _, path := range []string{"/v1/live?job=job-2", "/v1/live", "/v1/media/image?job=job-1"} {
		if code, _ := relayPost(t, srv, token, path, []byte(`{}`)); code != http.StatusUnauthorized {
			t.Fatalf("retired %s = %d; want 401", path, code)
		}
	}
	refuse = true
	if code, _ := relayPost(t, srv, token, "/v1/live?job=job-1", []byte(`{}`)); code != http.StatusConflict {
		t.Fatalf("refused view = %d", code)
	}
	refuse = false
	if code, _ := relayPost(t, srv, token, "/v1/live?job=job-1", []byte(`{"waiting":"sign in"}`)); code != http.StatusUnauthorized {
		t.Fatalf("after the refusal = %d; want 401", code)
	}
}
