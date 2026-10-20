package roomtools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
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

// Deliverable names a file's deliverable id after "publishing" it; a file
// that is not there cannot be one.
func (w fakeWorkspace) CopyDeliverables(context.Context) map[string]string { return nil }

func (w fakeWorkspace) Deliverable(_ context.Context, rel string) (string, error) {
	if _, ok := w[rel]; !ok {
		return "", errors.New(rel + " cannot be a deliverable: no such file")
	}
	return "artf_" + strings.ReplaceAll(rel, "/", "_"), nil
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

// componentsCall calls the components tool with action and args (a JSON
// object without the action).
func componentsCall(action string, env *Env, args json.RawMessage) Result {
	var in map[string]any
	if err := json.Unmarshal(args, &in); err != nil || in == nil {
		in = map[string]any{}
	}
	in["action"] = action
	raw, _ := json.Marshal(in)
	return componentsTool().Call(context.Background(), env, raw)
}

// The components tool takes each action's own parameters: a parameter of
// another action is refused naming the action that takes it (before any
// call), an unknown action names the actions, and list keeps its kinds.
func TestComponentsActionsTakeTheirOwnParameters(t *testing.T) {
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(savedDetail), nil }}
	env := contentEnv(fakeWorkspace{}, gw, true)
	for _, tc := range []struct {
		args, want string
	}{
		{`{"action":"save","dir":"drafts/x","scope":"org"}`, "scope is not a parameter of save (it is publish's); save takes dir, file,"},
		{`{"action":"get","id":"cc_1","task":"t"}`, "task is not a parameter of get (it is try's)"},
		{`{"action":"publish","id":"cc_1","scope":"org","dir":"d"}`, "dir is not a parameter of publish (it is try and save's)"},
		{`{"action":"list","color":"red"}`, `invalid arguments: unknown parameter "color"; list takes kind`},
		{`{"action":"list","kind":"room"}`, "list's kind is one of expert, skill, prompt, mcp"},
		{`{"action":"delete","id":"cc_1"}`, "action must be one of list, get, try, save, publish"},
		{`{}`, "action must be one of"},
		{`{"action":7}`, "action is a string"},
	} {
		if res := componentsTool().Call(context.Background(), env, json.RawMessage(tc.args)); !strings.Contains(res.Err, tc.want) {
			t.Errorf("%s: err = %q, want %q", tc.args, res.Err, tc.want)
		}
	}
	if len(gw.calls) != 0 {
		t.Fatalf("a refused call reached the gateway: %+v", gw.calls)
	}
	// Every parameter of the schema belongs to an action, and every action's
	// parameter is in the schema: one list, no orphan.
	props := componentsTool().InputSchema["properties"].(map[string]any)
	taken := map[string]bool{"action": true}
	for _, a := range componentsActions {
		for _, p := range a.params {
			if _, ok := props[p]; !ok {
				t.Errorf("an action takes %q, which the schema lacks", p)
			}
			taken[p] = true
		}
	}
	for p := range props {
		if !taken[p] {
			t.Errorf("the schema lists %q, which no action takes", p)
		}
	}
}

const savedDetail = `{"component":{"id":"cc_1","kind":"expert","name":"reviewer","displayName":"Reviewer","scope":"personal","latestVersion":"1.0.0","version":"1.0.0","mine":true}}`

// components save packs exactly the draft directory and posts it, as an
// archive, to the content routes with the bound job's grant.
func TestComponentsSavePacksDraftUnderJobGrant(t *testing.T) {
	ws := fakeWorkspace{
		"drafts/reviewer/component.json":        `{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.0.0","displayName":"Reviewer"}`,
		"drafts/reviewer/INSTRUCTIONS.md":       "Review.\n",
		"drafts/reviewer/skills/check/SKILL.md": "---\nname: check\ndescription: Use when checking\n---\nCheck.\n",
		"drafts/reviewer-old/component.json":    `{}`,
		"notes.md":                              "not part of it",
		"drafts/other/reviewer/component.json":  `{}`,
	}
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(savedDetail), nil }}
	res := componentsCall("save", contentEnv(ws, gw, true), json.RawMessage(`{"dir":"./drafts/reviewer/"}`))
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
	if res := componentsCall("save", contentEnv(ws, gw, true), json.RawMessage(`{"dir":"drafts/reviewer","id":"cc_1"}`)); res.Err != "" {
		t.Fatalf("save version = %s", res.Err)
	}
	if u, _ := url.Parse(gw.calls[0].Path); u.Path != "/v1/content-components/cc_1/versions" || u.Query().Get("job") != "job-1" {
		t.Fatalf("version call = %s", gw.calls[0].Path)
	}
}

// Refusals reach the agent as tool errors it can act on.
func TestComponentsSaveRefusals(t *testing.T) {
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
		{"unknown arg", contentEnv(ws, ok, true), `{"dir":"drafts/x","color":"red"}`, "invalid arguments"},
		{"room and a dir", contentEnv(ws, ok, true), `{"dir":"drafts/x","kind":"room"}`, "give one of"},
		{"another kind", contentEnv(ws, ok, true), `{"kind":"expert"}`, "kind is \"room\""},
		{"room fields on a package", contentEnv(ws, ok, true), `{"dir":"drafts/x","tags":["a"]}`, "tags are a room's"},
		{"a recipe on a room", contentEnv(ws, ok, true), `{"kind":"room","request":"x"}`, "carries no recipe"},
		{"two sources", contentEnv(ws, ok, true), `{"dir":"drafts/x","file":"notes.md"}`, "give one of"},
		{"no such file", contentEnv(ws, ok, true), `{"file":"out/missing.pptx"}`, "cannot be a deliverable"},
		{"work fields on a package", contentEnv(ws, ok, true), `{"dir":"drafts/x","request":"make a deck"}`, "are a work's"},
	} {
		if res := componentsCall("save", tc.env, json.RawMessage(tc.args)); !strings.Contains(res.Err, tc.want) {
			t.Errorf("%s: err = %q, want %q", tc.name, res.Err, tc.want)
		}
	}
	// The gateway's validator message is passed through.
	refused := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return nil, &StatusError{Status: 400, Body: []byte("package has no INSTRUCTIONS.md\n")}
	}}
	res := componentsCall("save", contentEnv(ws, refused, true), json.RawMessage(`{"dir":"drafts/x"}`))
	if !strings.Contains(res.Err, "package has no INSTRUCTIONS.md") {
		t.Fatalf("validator message = %q", res.Err)
	}
}

// components publish never shares by itself: "org" reads the component
// under the job's grant and puts a card for the requester in the room (a
// publish-proposal notice naming what would be shared and with whom), with
// no call to the publish route; "personal" withdraws through the route;
// "platform" is refused locally; another author's component is refused.
func TestComponentsPublishScopes(t *testing.T) {
	mine := `{"component":{"id":"cc_1","kind":"room","name":"weekly-review","displayName":"Weekly review","latestVersion":"1.2.0","mine":true}}`
	gw := &recordingGateway{reply: func(method, p string) ([]byte, error) {
		switch {
		case method == "GET":
			return []byte(mine), nil
		case strings.Contains(p, "/publish"):
			return []byte(`{"scope":"personal","note":"Taken out of your organization."}`), nil
		}
		return []byte(`{}`), nil
	}}
	res := componentsCall("publish", contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1","scope":"org","note":"for reviews"}`))
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	out := res.Value.(map[string]any)
	if out["proposed"] != "org" || !strings.Contains(out["note"].(string), "Nothing is shared yet") || !strings.Contains(out["note"].(string), "click Publish") {
		t.Fatalf("org publish = %+v", out)
	}
	if len(gw.calls) != 2 || gw.calls[0].Method != "GET" || gw.calls[1].Path != "/v1/rooms/room-1/agent-activity/messages" {
		t.Fatalf("calls = %+v", gw.calls)
	}
	if u, _ := url.Parse(gw.calls[0].Path); u.Query().Get("job") != "job-1" {
		t.Fatalf("read without the job grant: %s", gw.calls[0].Path)
	}
	batch := gw.calls[1].Body.(wire.ReportMessagesInput)
	payload := batch.Messages[0].Payload.(map[string]any)
	p, _ := payload["proposal"].(wire.PublishProposal)
	if payload["notice"] != wire.NoticePublishProposal || p.ComponentID != "cc_1" || p.Version != "1.2.0" || p.Scope != "org" || p.For != "maya" || p.Note != "for reviews" ||
		!strings.Contains(payload["text"].(string), "maya's agent proposes publishing Weekly review v1.2.0") {
		t.Fatalf("card = %+v", payload)
	}

	res = componentsCall("publish", contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1","scope":"personal"}`))
	last := gw.calls[len(gw.calls)-1]
	if res.Err != "" || last.Method != "POST" || !strings.HasPrefix(last.Path, "/v1/content-components/cc_1/publish?") {
		t.Fatalf("withdraw = %+v, calls %+v", res, gw.calls)
	}
	if body, _ := last.Body.(map[string]any); body["scope"] != "personal" {
		t.Fatalf("body = %+v", last.Body)
	}
	for _, c := range gw.calls[:2] {
		if strings.Contains(c.Path, "/publish") {
			t.Fatalf("org reached the publish route: %+v", c)
		}
	}
	n := len(gw.calls)
	res = componentsCall("publish", contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1","scope":"platform"}`))
	if !strings.Contains(res.Err, "platform admin") || len(gw.calls) != n {
		t.Fatalf("platform publish = %q (calls %d)", res.Err, len(gw.calls))
	}
	// Another author's component is not the requester's to share.
	theirs := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return []byte(`{"component":{"id":"cc_2","kind":"expert","name":"reviewer","latestVersion":"1.0.0","mine":false}}`), nil
	}}
	res = componentsCall("publish", contentEnv(nil, theirs, true), json.RawMessage(`{"id":"cc_2","scope":"org"}`))
	if !strings.Contains(res.Err, "only its author shares it") || len(theirs.calls) != 1 {
		t.Fatalf("another author's = %q, calls %+v", res.Err, theirs.calls)
	}
}

// components get reads the latest version's files under the job's grant.
func TestComponentsGetReadsLatestFiles(t *testing.T) {
	gw := &recordingGateway{reply: func(_ string, p string) ([]byte, error) {
		if strings.Contains(p, "/versions/") {
			return []byte(`{"component":{"id":"cc_1","kind":"expert","name":"reviewer","version":"1.2.0","latestVersion":"1.2.0","mine":true},
				"files":[{"path":"INSTRUCTIONS.md","sizeBytes":8,"text":"Review.\n"},{"path":"logo.png","sizeBytes":9,"binary":true}]}`), nil
		}
		return []byte(`{"component":{"id":"cc_1","latestVersion":"1.2.0"}}`), nil
	}}
	res := componentsCall("get", contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1"}`))
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

// Another author's component (a shared copy's included) comes back fenced
// as that author's data; the requester's own is not.
func TestComponentsGetFencesOthersFiles(t *testing.T) {
	for _, c := range []struct {
		name, component, author string
	}{
		{"own", `"mine":true,"owner":"maya"`, ""},
		{"org", `"mine":false,"owner":"jack","scope":"org"`, "jack"},
		{"shared copy", `"mine":true,"owner":"maya","sharedBy":"jill"`, "jill"},
	} {
		gw := &recordingGateway{reply: func(_ string, p string) ([]byte, error) {
			return []byte(`{"component":{"id":"cc_1","kind":"expert","name":"reviewer","version":"1.2.0","latestVersion":"1.2.0",` + c.component + `},
				"files":[{"path":"INSTRUCTIONS.md","sizeBytes":8,"text":"Review.\n"}]}`), nil
		}}
		res := componentsCall("get", contentEnv(nil, gw, true), json.RawMessage(`{"id":"cc_1","version":"1.2.0"}`))
		if res.Err != "" {
			t.Fatal(res.Err)
		}
		raw, _ := json.Marshal(res.Value)
		want := "Review.\n"
		if c.author != "" {
			want = roomlog.Fence(c.author, want)
		}
		wantJSON, _ := json.Marshal(want)
		if !strings.Contains(string(raw), `"text":`+string(wantJSON)) {
			t.Errorf("%s: result = %s; want text %s", c.name, raw, wantJSON)
		}
	}
}

// A new version of a component the organization uses stays the
// requester's (the gateway proposes it, orgProposed): the tool puts the
// publish card for that version in the room and tells the agent it
// reaches the organization only by their click.
func TestComponentsSaveProposesTheNewVersion(t *testing.T) {
	ws := fakeWorkspace{"drafts/reviewer/component.json": `{}`, "drafts/reviewer/INSTRUCTIONS.md": "Review.\n"}
	detail := `{"component":{"id":"cc_1","kind":"expert","name":"reviewer","displayName":"Reviewer","scope":"personal","latestVersion":"1.1.0"},"orgProposed":"1.1.0"}`
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(detail), nil }}
	res := componentsCall("save", contentEnv(ws, gw, true), json.RawMessage(`{"dir":"drafts/reviewer","id":"cc_1"}`))
	if res.Err != "" {
		t.Fatalf("save = %s", res.Err)
	}
	out := res.Value.(map[string]any)
	note, _ := out["note"].(string)
	if out["proposed"] != "org" || !strings.Contains(note, "version 1.1.0 reaches it only when they click Publish") {
		t.Fatalf("result = %+v", out)
	}
	card := gw.calls[len(gw.calls)-1]
	payload := card.Body.(wire.ReportMessagesInput).Messages[0].Payload.(map[string]any)
	if p, _ := payload["proposal"].(wire.PublishProposal); card.Path != "/v1/rooms/room-1/agent-activity/messages" || p.ComponentID != "cc_1" || p.Version != "1.1.0" || p.For != "maya" {
		t.Fatalf("card = %+v", payload)
	}
	// A save the organization does not use puts no card.
	plain := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(savedDetail), nil }}
	if res := componentsCall("save", contentEnv(ws, plain, true), json.RawMessage(`{"dir":"drafts/reviewer","id":"cc_1"}`)); res.Err != "" || len(plain.calls) != 1 {
		t.Fatalf("plain save = %+v, calls %d", res, len(plain.calls))
	}
}

// A workspace file saves as a work through its deliverable, published first
// (one the agent wrote this turn is in no room record yet), with the
// agent's own description and "how it was made" request.
func TestComponentsSaveSavesAWorkspaceFileAsAWork(t *testing.T) {
	ws := fakeWorkspace{"work/out/deck.pptx": "PK"}
	work := `{"component":{"id":"cc_9","kind":"work","name":"deck","displayName":"Deck","scope":"personal","latestVersion":"1.0.0","mine":true}}`
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(work), nil }}
	res := componentsCall("save", contentEnv(ws, gw, true), json.RawMessage(
		`{"file":"work/out/deck.pptx","name":"deck","category":"office","description":"A 39-slide layout gallery.","request":"Make a layout gallery deck from our template"}`))
	if res.Err != "" {
		t.Fatalf("save = %s", res.Err)
	}
	if len(gw.calls) != 1 || gw.calls[0].Path != "/v1/rooms/room-1/artifacts/artf_work_out_deck.pptx/publish-work?job=job-1" {
		t.Fatalf("calls = %+v", gw.calls)
	}
	body := gw.calls[0].Body.(wire.WorkPublishRequest)
	if body.Description != "A 39-slide layout gallery." || body.RecipePrompt != "Make a layout gallery deck from our template" || body.Name != "deck" {
		t.Fatalf("publish request = %+v", body)
	}
}
