package roomtools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// fakeWorkspace is a session workspace in memory.
type fakeWorkspace map[string]string

func (w fakeWorkspace) ListFiles(context.Context) ([]core.SessionFile, error) {
	var out []core.SessionFile
	for p, data := range w {
		out = append(out, core.SessionFile{Path: p, Bytes: int64(len(data))})
	}
	return out, nil
}

func (w fakeWorkspace) ReadFile(_ context.Context, rel string) ([]byte, error) {
	data, ok := w[rel]
	if !ok {
		return nil, errors.New("no such file")
	}
	return []byte(data), nil
}

// recordingGateway answers every call with a fixed reply and records it.
type recordingGateway struct {
	calls []recordedCall
	reply func(method, path string) ([]byte, error)
}

type recordedCall struct {
	Method, Path string
	Body         any
}

func (g *recordingGateway) Do(_ context.Context, method, path string, body any) ([]byte, error) {
	g.calls = append(g.calls, recordedCall{method, path, body})
	return g.reply(method, path)
}

func contentEnv(ws Workspace, gw Gateway, bound bool) *Env {
	sess := &Session{scope: Scope{RoomID: "room-1", Owner: "jack", Provider: "opencode"}}
	if bound {
		sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "ch-1", MessageSeq: 4, Workspace: ws})
	}
	return &Env{Session: sess, Gateway: gw}
}

const savedDetail = `{"component":{"id":"cc_1","kind":"expert","name":"reviewer","displayName":"Reviewer","scope":"personal","latestVersion":"1.0.0","version":"1.0.0","mine":true}}`

// save_component packs exactly the draft directory and posts it, as an
// archive, to the content routes with the bound job's grant.
func TestSaveComponentPacksDraftUnderJobGrant(t *testing.T) {
	ws := fakeWorkspace{
		"drafts/reviewer/component.json":        `{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.0.0","displayName":"Reviewer"}`,
		"drafts/reviewer/INSTRUCTIONS.md":       "Review.\n",
		"drafts/reviewer/skills/check/SKILL.md": "---\nname: check\ndescription: Use when checking\n---\nCheck.\n",
		"drafts/reviewer-old/component.json":    `{}`,
		"notes.md":                              "not part of it",
		"drafts/other/reviewer/component.json":  `{}`,
	}
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(savedDetail), nil }}
	res := saveComponent(context.Background(), contentEnv(ws, gw, true), json.RawMessage(`{"dir":"./drafts/reviewer/"}`))
	if res.Err != "" {
		t.Fatalf("save = %s", res.Err)
	}
	out := res.Value.(map[string]any)
	if out["id"] != "cc_1" || out["version"] != "1.0.0" || out["files"] != 3 {
		t.Fatalf("result = %+v", out)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("calls = %+v", gw.calls)
	}
	c := gw.calls[0]
	u, _ := url.Parse(c.Path)
	if c.Method != "POST" || u.Path != "/v1/content-components" || u.Query().Get("job") != "job-1" || u.Query().Get("room") != "room-1" {
		t.Fatalf("call = %s %s", c.Method, c.Path)
	}
	body, ok := c.Body.(RawBody)
	if !ok || body.ContentType != "application/x-tar" {
		t.Fatalf("body = %T %+v", c.Body, c.Body)
	}
	b, err := tarbundle.Parse(body.Data, "", tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.Paths(), ","); got != "INSTRUCTIONS.md,component.json,skills/check/SKILL.md" {
		t.Fatalf("packed = %s", got)
	}

	// With id: a new version of that component.
	gw.calls = nil
	if res := saveComponent(context.Background(), contentEnv(ws, gw, true), json.RawMessage(`{"dir":"drafts/reviewer","id":"cc_1"}`)); res.Err != "" {
		t.Fatalf("save version = %s", res.Err)
	}
	if u, _ := url.Parse(gw.calls[0].Path); u.Path != "/v1/content-components/cc_1/versions" || u.Query().Get("job") != "job-1" {
		t.Fatalf("version call = %s", gw.calls[0].Path)
	}
}

// Refusals reach the agent as tool errors it can act on.
func TestSaveComponentRefusals(t *testing.T) {
	ws := fakeWorkspace{"drafts/x/component.json": `{}`, "drafts/y/INSTRUCTIONS.md": "x"}
	ok := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(savedDetail), nil }}
	for _, tc := range []struct {
		name string
		env  *Env
		args string
		want string
	}{
		{"not answering", contentEnv(ws, ok, false), `{"dir":"drafts/x"}`, "only while you are answering"},
		{"escape", contentEnv(ws, ok, true), `{"dir":"../x"}`, "not a plain"},
		{"absolute", contentEnv(ws, ok, true), `{"dir":"/etc"}`, "relative to your workspace"},
		{"empty", contentEnv(ws, ok, true), `{"dir":"drafts/none"}`, "no files under drafts/none"},
		{"no manifest", contentEnv(ws, ok, true), `{"dir":"drafts/y"}`, "has no component.json"},
		{"no files seam", contentEnv(nil, ok, true), `{"dir":"drafts/x"}`, "Save as expert"},
		{"unknown arg", contentEnv(ws, ok, true), `{"dir":"drafts/x","kind":"expert"}`, "invalid arguments"},
	} {
		if res := saveComponent(context.Background(), tc.env, json.RawMessage(tc.args)); !strings.Contains(res.Err, tc.want) {
			t.Errorf("%s: err = %q, want %q", tc.name, res.Err, tc.want)
		}
	}
	// The gateway's validator message is passed through.
	refused := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return nil, &StatusError{Status: 400, Body: []byte("package has no INSTRUCTIONS.md\n")}
	}}
	res := saveComponent(context.Background(), contentEnv(ws, refused, true), json.RawMessage(`{"dir":"drafts/x"}`))
	if !strings.Contains(res.Err, "package has no INSTRUCTIONS.md") {
		t.Fatalf("validator message = %q", res.Err)
	}
}

// publish_component posts the scope to the publish route under the job's
// grant and reports the request; platform scope is refused locally with
// what a platform admin does.
func TestPublishComponentScopes(t *testing.T) {
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return []byte(`{"scope":"personal","note":"Waiting for an organization admin's approval.","request":{"id":"cpr_1","version":"1.2.0","status":"pending"}}`), nil
	}}
	res := publishComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1","scope":"org","note":"for reviews"}`))
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	out := res.Value.(map[string]any)
	if out["request"].(map[string]any)["status"] != "pending" || !strings.Contains(out["note"].(string), "approval") {
		t.Fatalf("org publish = %+v", out)
	}
	if len(gw.calls) != 1 || gw.calls[0].Method != "POST" || !strings.HasPrefix(gw.calls[0].Path, "/v1/content-components/cc_1/publish?") {
		t.Fatalf("calls = %+v", gw.calls)
	}
	if u, _ := url.Parse(gw.calls[0].Path); u.Query().Get("job") != "job-1" {
		t.Fatalf("publish without the job grant: %s", gw.calls[0].Path)
	}
	if body, _ := gw.calls[0].Body.(map[string]any); body["scope"] != "org" || body["note"] != "for reviews" {
		t.Fatalf("body = %+v", gw.calls[0].Body)
	}
	res = publishComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1","scope":"platform"}`))
	if !strings.Contains(res.Err, "platform admin") || len(gw.calls) != 1 {
		t.Fatalf("platform publish = %q (calls %d)", res.Err, len(gw.calls))
	}
	// The gateway's refusal is passed through.
	refused := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return nil, &StatusError{Status: 409, Body: []byte("version 1.2.0 is already published to your organization\n")}
	}}
	res = publishComponent(context.Background(), contentEnv(nil, refused, true), json.RawMessage(`{"id":"cc_1","scope":"org"}`))
	if !strings.Contains(res.Err, "already published") {
		t.Fatalf("refusal = %q", res.Err)
	}
}

// get_component reads the latest version's files under the job's grant.
func TestGetComponentReadsLatestFiles(t *testing.T) {
	gw := &recordingGateway{reply: func(_ string, p string) ([]byte, error) {
		if strings.Contains(p, "/versions/") {
			return []byte(`{"component":{"id":"cc_1","kind":"expert","name":"reviewer","version":"1.2.0","latestVersion":"1.2.0","mine":true},
				"files":[{"path":"INSTRUCTIONS.md","sizeBytes":8,"text":"Review.\n"},{"path":"logo.png","sizeBytes":9,"binary":true}]}`), nil
		}
		return []byte(`{"component":{"id":"cc_1","latestVersion":"1.2.0"}}`), nil
	}}
	res := getComponent(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1"}`))
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	if len(gw.calls) != 2 || !strings.HasPrefix(gw.calls[1].Path, "/v1/content-components/cc_1/versions/1.2.0?") {
		t.Fatalf("calls = %+v", gw.calls)
	}
	raw, _ := json.Marshal(res.Value)
	if !strings.Contains(string(raw), `"text":"Review.\n"`) || !strings.Contains(string(raw), "binary file") {
		t.Fatalf("result = %s", raw)
	}
}

// A save the organization's publication resubmits tells the agent to say
// so: the new version waits for an org admin.
func TestSaveComponentReportsResubmission(t *testing.T) {
	ws := fakeWorkspace{"drafts/reviewer/component.json": `{}`, "drafts/reviewer/INSTRUCTIONS.md": "Review.\n"}
	detail := `{"component":{"id":"cc_1","kind":"expert","name":"reviewer","scope":"personal","latestVersion":"1.1.0"},"resubmitted":{"id":"cpr_1","version":"1.1.0","approvedVersion":"1.0.0"}}`
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(detail), nil }}
	res := saveComponent(context.Background(), contentEnv(ws, gw, true), json.RawMessage(`{"dir":"drafts/reviewer","id":"cc_1"}`))
	if res.Err != "" {
		t.Fatalf("save = %s", res.Err)
	}
	out := res.Value.(map[string]any)
	note, _ := out["note"].(string)
	if out["resubmitted"] == nil || !strings.Contains(note, "version 1.1.0 was sent to its admins for approval; members keep using version 1.0.0 until then") {
		t.Fatalf("result = %+v", out)
	}
}
