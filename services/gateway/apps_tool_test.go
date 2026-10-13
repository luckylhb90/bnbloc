package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// bridgeGateway is a bridge's gateway client for the room tools: every
// call carries the bridge owner's session, as the bridge's own client does
// (services/bridge roomToolsGateway), a RawBody with its content type and
// headers.
type bridgeGateway struct {
	e      *appGW
	bearer string
}

func (g bridgeGateway) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	header := http.Header{"Authorization": {"Bearer " + g.bearer}}
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case roomtools.RawBody:
		for k, v := range b.Header {
			header[k] = v
		}
		if b.ContentType != "" {
			header.Set("Content-Type", b.ContentType)
			rd = bytes.NewReader(b.Data)
		}
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		header.Set("Content-Type", "application/json")
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.e.srv.URL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header = header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &roomtools.StatusError{Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}

// The app_data room tool end to end: the tool → the gateway's agent entry
// (?job=) → the room service → the memory store. jack's bridge runs maya's
// job; its agent is one of the app's owners.
func TestAppDataToolEndToEnd(t *testing.T) {
	e := newAppGW(t)
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	gw := bridgeGateway{e: e, bearer: e.session("jack")}
	srv, err := roomtools.Start(roomtools.Config{Gateway: gw})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	_, sess, err := srv.Open(roomtools.Scope{RoomID: e.roomID, TenantID: e.tenant, Owner: "jack", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	sess.Bind(roomtools.Job{JobID: "job-1", RequesterUserID: "maya"})
	tool, _ := roomtools.DefaultRegistry().Lookup("app_data")
	call := func(args map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		r := tool.Call(context.Background(), &roomtools.Env{Session: sess, Gateway: gw}, raw)
		if r.Err != "" {
			t.Fatalf("app_data %v: %s", args, r.Err)
		}
		return r.Value.(map[string]any)
	}
	fencedDoc := func(out map[string]any, key string) map[string]any {
		t.Helper()
		text, _ := out[key].(string)
		lines := strings.Split(text, "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[0], "<<<untrusted-content ") || !strings.HasPrefix(lines[len(lines)-1], "<<<end untrusted-content ") {
			t.Fatalf("%s is not fenced:\n%s", key, text)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(lines[1]), &doc); err != nil {
			t.Fatalf("fenced %s: %v", key, err)
		}
		return doc
	}

	// The agent seeds a public option: stamped as the agent, not as its
	// bridge's owner or the job's requester.
	out := call(map[string]any{"action": "insert", "artifactId": e.appA, "collection": "options", "doc": map[string]any{"label": "Lake"}, "public": true})
	opt := fencedDoc(out, "document")
	agentID := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/apps/"+e.appA+"/me?job=job-1", nil, "Authorization: Bearer "+gw.bearer).json()["id"]
	jackID := e.me(e.ticketBase("jack", e.appA))
	if author, _ := opt["_author"].(map[string]any); author["kind"] != "agent" || author["id"] != "jack/codex" || opt["_by"] != agentID || opt["_by"] == jackID || opt["label"] != "Lake" {
		t.Fatalf("the agent's insert = %v (agent id %v, jack %v)", opt, agentID, jackID)
	}

	// A visitor votes privately through the public link; the agent, an
	// owner, reads the vote and who cast it.
	tok, _ := e.share(e.appA)
	vis := visitorHeader(e.visitor(tok, ""))
	pub := shareBase(tok)
	visID := e.me(pub, vis)
	if r := e.call(http.MethodPut, pub+"~app/c/votes/"+visID, map[string]any{"doc": map[string]any{"choice": "Lake"}}, vis, "If-None-Match: *"); r.status != http.StatusCreated {
		t.Fatalf("the visitor's vote = %d %s", r.status, r.body)
	}
	out = call(map[string]any{"action": "find", "artifactId": e.appA, "collection": "votes"})
	vote := fencedDoc(out, "documents")
	if out["count"] != 1 || vote["_id"] != visID || vote["_by"] != visID || vote["_author"].(map[string]any)["kind"] != "visitor" || vote["choice"] != "Lake" {
		t.Fatalf("the agent's find = %v", out)
	}
	if got := fencedDoc(call(map[string]any{"action": "get", "artifactId": e.appA, "collection": "votes", "id": visID}), "document"); got["choice"] != "Lake" {
		t.Fatalf("the agent's get = %v", got)
	}

	// The room's apps and the app's collections.
	apps, _ := call(map[string]any{"action": "list"})["apps"].([]map[string]any)
	if len(apps) != 1 || apps[0]["artifactId"] != e.appA || apps[0]["documents"] != int64(2) {
		t.Fatalf("the room's apps = %v", apps)
	}
	if st := call(map[string]any{"action": "list", "artifactId": e.appA}); st["documents"] != int64(2) || st["collections"].(map[string]int64)["votes"] != 1 {
		t.Fatalf("the app's collections = %v", st)
	}

	// ifAbsent is the API's create-only: a second put of the id conflicts.
	raw, _ := json.Marshal(map[string]any{"action": "put", "artifactId": e.appA, "collection": "votes", "id": visID, "doc": map[string]any{"choice": "Hill"}, "ifAbsent": true})
	if r := tool.Call(context.Background(), &roomtools.Env{Session: sess, Gateway: gw}, raw); !strings.Contains(r.Err, "document exists") {
		t.Fatalf("put ifAbsent over the visitor's vote = %+v", r)
	}

	// wipe needs confirm; with it, every document goes.
	raw, _ = json.Marshal(map[string]any{"action": "wipe", "artifactId": e.appA})
	if r := tool.Call(context.Background(), &roomtools.Env{Session: sess, Gateway: gw}, raw); !strings.Contains(r.Err, "confirm") {
		t.Fatalf("wipe without confirm = %+v", r)
	}
	if st := call(map[string]any{"action": "list", "artifactId": e.appA}); st["documents"] != int64(2) {
		t.Fatalf("a refused wipe deleted data: %v", st)
	}
	if out := call(map[string]any{"action": "wipe", "artifactId": e.appA, "confirm": true}); out["deleted"] != int64(2) {
		t.Fatalf("wipe = %v", out)
	}
	if st := call(map[string]any{"action": "list", "artifactId": e.appA}); st["documents"] != int64(0) {
		t.Fatalf("after the wipe = %v", st)
	}
}
