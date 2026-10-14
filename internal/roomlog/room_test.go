package roomlog

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden .room/ files in testdata")

// golden compares a rendered .room/ file with testdata/golden/<name>.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from %s (run with -update after reviewing):\n--- got ---\n%s\n--- want ---\n%s", name, path, got, want)
	}
}

// fakeFetch serves fixed bodies by path and counts the calls.
type fakeFetch struct {
	bodies map[string]string
	calls  []string
}

func (f *fakeFetch) fetch(_ context.Context, path string) ([]byte, error) {
	f.calls = append(f.calls, path)
	body, ok := f.bodies[path]
	if !ok {
		return nil, errors.New("404 " + path)
	}
	return []byte(body), nil
}

// roomFetch is a room with three deliverables: one agent A (maya/opencode on
// core acme) published, one a person uploaded, and one the reading agent
// published itself.
func roomFetch() *fakeFetch {
	ff := &fakeFetch{bodies: map[string]string{
		"/v1/rooms/r1/artifacts?limit=30": `{"artifacts":[
			{"artifactId":"artf_a","title":"drafts/weekly-report-assistant/component.json","kind":"code","currentVersion":2,"createdBy":"maya","updatedAtMs":3},
			{"artifactId":"art_b","title":"brief.md","kind":"markdown","currentVersion":1,"createdBy":"maya","updatedAtMs":2},
			{"artifactId":"artf_c","title":"notes.md","kind":"markdown","currentVersion":3,"createdBy":"maya","updatedAtMs":1}]}`,
		"/v1/rooms/r1/artifacts/artf_a/versions/2": `{"version":{"artifactId":"artf_a","version":2,"sourceJobId":"job-a","authorUserId":"maya"}}`,
		"/v1/rooms/r1/artifacts/art_b/versions/1":  `{"version":{"artifactId":"art_b","version":1,"authorUserId":"maya"}}`,
		"/v1/rooms/r1/artifacts/artf_c/versions/3": `{"version":{"artifactId":"artf_c","version":3,"sourceJobId":"job-c","authorUserId":"maya"}}`,
		"/v1/rooms/r1/jobs/job-a":                  `{"jobId":"job-a","agent":{"owner":"maya","core":"acme","provider":"opencode"}}`,
		"/v1/rooms/r1/jobs/job-c":                  `{"jobId":"job-c","agent":{"owner":"maya","core":"nimbus","provider":"codex"}}`,
	}}
	// The index pages through every deliverable (listAllDeliverables).
	ff.bodies["/v1/rooms/r1/artifacts?limit=200"] = ff.bodies["/v1/rooms/r1/artifacts?limit=30"]
	return ff
}

// pullTools are the facts of a session with the room tools and .room/.
var pullTools = Guidance{HasTools: true, HasRoomFiles: true}

// Session guidance states the room model (own workspaces, deliverables
// stored by bnbloc, the shared Library, how to verify), the .room/ legend
// only where bnbloc writes .room/, and the room tools' usage only where
// they are attached, every limit from its owner's constant.
func TestSessionGuidanceGolden(t *testing.T) {
	for name, g := range map[string]Guidance{
		"guidance-tools.txt": pullTools,
		"guidance-plain.txt": {},
		"guidance-team.txt":  {Self: "maya/codex", HasTools: true, HasRoomFiles: true, TeamLead: true},
	} {
		golden(t, name, SessionGuidance(g))
	}
	plain, tools := SessionGuidance(Guidance{}), SessionGuidance(pullTools)
	for _, s := range []string{".room/", "[The bnbloc-room tools]", "read_artifact", "app_data", "apps.md"} {
		if strings.Contains(plain, s) {
			t.Errorf("guidance without tools or .room/ mentions %q:\n%s", s, plain)
		}
		if !strings.Contains(tools, s) {
			t.Errorf("guidance with tools and .room/ lacks %q:\n%s", s, tools)
		}
	}
	// A session with the tools but no .room/ files (a core that takes no
	// workspace files) is never pointed at a file it does not have.
	if noFiles := SessionGuidance(Guidance{HasTools: true}); strings.Contains(noFiles, ".room/") || strings.Contains(noFiles, "artifacts.md") || strings.Contains(noFiles, "apps.md") {
		t.Errorf("guidance without .room/ files mentions them:\n%s", noFiles)
	}
	if want := fmt.Sprintf("at most %d tasks per request you are working on, %d hops deep", wire.MaxDispatchFanOut, wire.MaxDispatchDepth); !strings.Contains(tools, want) {
		t.Errorf("the dispatch limits are not the wire constants (%q):\n%s", want, tools)
	}
}

// .room/README.md is the session's guidance, kept beside the records.
func TestReadmeGolden(t *testing.T) {
	f := ReadmeFile(pullTools)
	if f.Name != ".room/README.md" || !strings.HasSuffix(f.Content, SessionGuidance(pullTools)) {
		t.Fatalf("README = %s:\n%s", f.Name, f.Content)
	}
	golden(t, "README.md", f.Content)
}

// The README is static per session: a fresh session's first pass writes it,
// a pass whose workspace already holds its current text does not, and a
// changed wording (a stale sum) writes it again.
func TestReadmeUpdateOnlyWhenChanged(t *testing.T) {
	f, sum, changed := ReadmeUpdate(CtxState{}, pullTools)
	if !changed || f.Name != ".room/README.md" || f.Content != ReadmeFile(pullTools).Content || sum == "" {
		t.Fatalf("fresh session: changed=%v name=%s sum=%q", changed, f.Name, sum)
	}
	if _, again, changed := ReadmeUpdate(CtxState{ReadmeSum: sum}, pullTools); changed || again != sum {
		t.Fatalf("unchanged README reported changed (sum %q -> %q)", sum, again)
	}
	if _, _, changed := ReadmeUpdate(CtxState{ReadmeSum: "sum-of-an-older-wording"}, pullTools); !changed {
		t.Fatal("a README whose wording changed is not rewritten")
	}
}

// The per-turn pull prompt is the message alone (its attachments, the
// [now] marker naming its sender, its text), plus one line pointing at the
// records where the guidance rode the first prompt: the goldens pin every
// byte (it rides every turn), and no static guidance rides with it.
func TestComposePointerGolden(t *testing.T) {
	now := Now{From: "maya", Text: "MSG"}
	golden(t, "pointer.txt", ComposePointer(now, false))
	golden(t, "pointer-hint.txt", ComposePointer(now, true))
	for _, p := range []string{ComposePointer(now, false), ComposePointer(now, true)} {
		for _, line := range strings.Split(strings.TrimSpace(SessionGuidance(pullTools)), "\n") {
			if line != "" && strings.Contains(p, line) {
				t.Fatalf("the per-turn prompt carries session guidance %q:\n%s", line, p)
			}
		}
	}
}

// Who the agent is, what the records are and how to answer the [now]
// message are session guidance: the push model's first prompt carries the
// records and the message, nothing else.
func TestComposeCarriesNoSessionGuidance(t *testing.T) {
	p := Compose([]Entry{{Seq: 1, Speaker: "jack", Text: "hello"}}, Now{From: "maya", Text: "MSG"}, Options{})
	if want := RecordsHeader("Room conversation") + "jack: hello\n\n[now — from maya]\nMSG"; p != want {
		t.Fatalf("the push prompt = %q, want %q", p, want)
	}
	g := SessionGuidance(Guidance{Self: "maya/codex"})
	for _, want := range []string{
		`You are the agent "maya/codex" in a bnbloc room`,
		"[now — from <sender>]",
		"respond to that message alone",
		"Anything headed " + recordsMark + " is background",
		"Text fenced as " + fenceMark + " was written by someone else",
		"Attachments listed before it are part of the message",
		"Answer in the language the person wrote in.",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("session guidance lacks %q:\n%s", want, g)
		}
	}
}

// .room/artifacts.md names each deliverable's producer (owner/provider and
// core, or the person), its latest version and id, and where its read-only
// copy is in this workspace or why there is none; the reader's own
// deliverable says it is its own.
func TestArtifactsGoldenNamesProducers(t *testing.T) {
	ff := roomFetch()
	list, err := ReadDeliverables(context.Background(), ff.fetch, "r1", &ProducerCache{})
	if err != nil {
		t.Fatal(err)
	}
	copies := map[string]Copy{
		"artf_a": {Dir: ".room/deliverables/artf_a/", Version: 2, Files: 1},
		"art_b":  {Version: 1, TooLarge: MaxDeliverableCopyBytes + 1},
		"artf_c": {Dir: ".room/deliverables/artf_c/", Version: 2, Files: 1, Pending: true},
	}
	f := DeliverablesFile(list, nil, Producer{Owner: "maya", Provider: "codex", Core: "nimbus"}, copies)
	if f.Name != ".room/artifacts.md" {
		t.Fatalf("name = %s", f.Name)
	}
	golden(t, "artifacts.md", f.Content)
	for _, want := range []string{
		"drafts/weekly-report-assistant/component.json (code) v2, id artf_a: from maya/opencode on core acme, whose source is in that agent's workspace; copy: .room/deliverables/artf_a/",
		"brief.md (markdown) v1, id art_b: uploaded by maya (a person); no copy: 8388609 bytes, over the 8388608-byte copy bound",
		"notes.md (markdown) v3, id artf_c: from you (maya/codex on core nimbus); copy of v2 (not yet the latest): .room/deliverables/artf_c/",
		"to change\none, copy it into your own working tree first",
	} {
		if !strings.Contains(f.Content, want) {
			t.Errorf("artifacts.md lacks %q:\n%s", want, f.Content)
		}
	}
	// Without copies (a core that takes no binary files) every line says so.
	if bare := DeliverablesFile(list, nil, Producer{}, nil).Content; strings.Count(bare, "no copy in this workspace this turn") != 3 {
		t.Fatalf("artifacts.md without copies:\n%s", bare)
	}
}

// A resolved version's producer is cached: the next turn reads the listing
// only. A listing that fails says so rather than "none".
func TestDeliverablesCacheAndFailure(t *testing.T) {
	ff := roomFetch()
	cache := &ProducerCache{}
	if _, err := ReadDeliverables(context.Background(), ff.fetch, "r1", cache); err != nil {
		t.Fatal(err)
	}
	ff.calls = nil
	list, err := ReadDeliverables(context.Background(), ff.fetch, "r1", cache)
	if err != nil || len(list) != 3 || list[0].Producer.Core != "acme" {
		t.Fatalf("cached read = %+v, %v", list, err)
	}
	if len(ff.calls) != 1 {
		t.Fatalf("a cached turn fetched %v, want the listing only", ff.calls)
	}
	// An unreadable run record leaves the job id and is retried next time.
	delete(ff.bodies, "/v1/rooms/r1/jobs/job-a")
	list, _ = ReadDeliverables(context.Background(), ff.fetch, "r1", &ProducerCache{})
	if got := list[0].Producer.String(); got != "an agent (job job-a)" {
		t.Fatalf("unresolved producer = %q", got)
	}
	if got := DeliverablesFile(nil, errors.New("down"), Producer{}, nil).Content; !strings.Contains(got, "could not be read this turn") || strings.Contains(got, "(none yet)") {
		t.Fatalf("failed listing renders:\n%s", got)
	}
}

// .room/library.md lists the Library newest first: name, kind, version,
// scope and updated time, bounded; MCP tools are the platform's.
func TestLibraryGolden(t *testing.T) {
	ff := &fakeFetch{bodies: map[string]string{
		"/v1/components?job=job-1&room=r1": `{"components":[
			{"kind":"mcp","name":"search","version":"2.0.0","rollout":"auto","installed":true,"managed":true,"publishedAtMs":1760200000000}],
			"contentComponents":[
			{"id":"cc_1","kind":"expert","name":"weekly-report-assistant","displayName":"Weekly report assistant","owner":"maya","scope":"personal","status":"active","latestVersion":"1.0.1","version":"1.0.1","permissions":[],"requires":{},"mine":true,"acked":true,"uses":0,"runs":0,"members":0,"createdAtMs":1760250000000,"updatedAtMs":1760260000000},
			{"id":"cc_2","kind":"prompt","name":"team-prompts","owner":"jack","scope":"org","status":"active","latestVersion":"3.0.0","version":"3.0.0","permissions":[],"requires":{},"mine":false,"acked":true,"uses":0,"runs":0,"members":0,"createdAtMs":1,"updatedAtMs":1760100000000}]}`,
	}}
	items, err := ReadLibrary(context.Background(), ff.fetch, "r1", "job-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Name != "weekly-report-assistant" || items[1].Kind != LibraryKindMCP || items[2].Name != "team-prompts" {
		t.Fatalf("items = %+v, want newest first", items)
	}
	golden(t, "library.md", LibraryFile(items, nil).Content)

	many := make([]LibraryItem, MaxLibraryItems+5)
	for i := range many {
		many[i] = LibraryItem{Kind: "skill", Name: "s", Version: "1.0.0", Scope: "personal"}
	}
	if got := LibraryFile(many, nil).Content; strings.Count(got, "\n- ") != MaxLibraryItems || !strings.Contains(got, "(5 older items not listed)") {
		t.Fatalf("library.md is not bounded:\n%s", got)
	}
	if got := LibraryFile(nil, errors.New("down")).Content; !strings.Contains(got, "could not be read this turn") {
		t.Fatalf("failed read renders:\n%s", got)
	}
}

// Every .room/ history file is pinned: INDEX.md, recent.md and a chunk,
// their record lines attributed (Line).
func TestHistoryFilesGolden(t *testing.T) {
	files, _ := Materialize(attributedEntries, true, CtxState{})
	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
	}
	golden(t, "recent.md", byName[".room/recent.md"])
	golden(t, "chunk.md", byName[".room/chunk-0000000010-0000000012.md"])
	golden(t, "INDEX.md", byName[".room/INDEX.md"])
}

// The push model's prompts are pinned: a session's first (records, then the
// message with its attachment and the one that did not reach the
// workspace) and a follow-up's delta.
func TestComposeGolden(t *testing.T) {
	now := Now{From: Sender(attributedEntries[1]), Text: "go", Attachments: []Attachment{{Path: ".room/attachments/att_1.png", Filename: "shot.png", ContentType: "image/png"}},
		Undelivered: []UndeliveredAttachment{{Filename: "brand.png", ContentType: "image/png", Size: 1708534, Reason: "too large for this agent's runtime (it accepts files up to 1.5 MB)"}}}
	older := []Entry{{Seq: 1, Speaker: "jack", Text: "earlier plan"}, {Seq: 2, Speaker: "jack/codex", Kind: KindAgent, Text: "drafted it"}}
	golden(t, "compose-fresh.txt", Compose(append(older, attributedEntries...), now, Options{WindowChars: 120, Summarizer: StructuredSummarizer{}}))
	golden(t, "compose-delta.txt", ComposeDelta(attributedEntries, now, Options{}))
}

// appsFetch is a room with three sites and a document, as the listing's
// isApp (the server's one rule) marks them: artf_poll is an app holding
// data (maya/codex built it), artf_form an app nobody used yet (it loads
// the SDK; jack published it), artf_page a plain site, and the document is
// no app.
func appsFetch() *fakeFetch {
	return &fakeFetch{bodies: map[string]string{
		"/v1/rooms/r1/artifacts?limit=30": `{"artifacts":[
			{"artifactId":"artf_poll","title":"Friday poll","kind":"site","currentVersion":3,"createdBy":"maya","updatedAtMs":4,"isApp":true},
			{"artifactId":"artf_form","title":"Sign-up form","kind":"site","currentVersion":1,"createdBy":"jack","updatedAtMs":3,"isApp":true},
			{"artifactId":"artf_page","title":"Landing page","kind":"site","currentVersion":1,"createdBy":"maya","updatedAtMs":2,"isApp":false},
			{"artifactId":"art_doc","title":"notes.md","kind":"markdown","currentVersion":1,"createdBy":"maya","updatedAtMs":1,"isApp":false}]}`,
		"/v1/rooms/r1/artifacts/artf_poll/versions/3": `{"version":{"artifactId":"artf_poll","version":3,"sourceJobId":"job-a","authorUserId":"maya"}}`,
		"/v1/rooms/r1/artifacts/artf_form/versions/1": `{"version":{"artifactId":"artf_form","version":1,"authorUserId":"jack"}}`,
		"/v1/rooms/r1/jobs/job-a":                     `{"jobId":"job-a","agent":{"owner":"maya","core":"acme","provider":"codex"}}`,
		"/v1/rooms/r1/apps/artf_poll/stats?job=job-1": `{"documents":12,"bytes":4096,"collections":{"votes":11,"options":1}}`,
		"/v1/rooms/r1/apps/artf_form/stats?job=job-1": `{"documents":0,"bytes":0,"collections":{}}`,
	}}
}

// .room/apps.md lists the room's interactive apps: the deliverables the
// listing marks isApp (read with the job's grant), each with its producer
// and its collections' counts, never a document's content. It is written
// only once the room has an app, and from then on every turn, so it never
// goes stale; a failed read says so.
func TestAppsGolden(t *testing.T) {
	ff := appsFetch()
	apps, err := ReadApps(context.Background(), ff.fetch, "r1", "job-1", &ProducerCache{})
	if err != nil || len(apps) != 2 || apps[0].ArtifactID != "artf_poll" || apps[1].ArtifactID != "artf_form" {
		t.Fatalf("apps = %+v, %v", apps, err)
	}
	for _, c := range ff.calls {
		if strings.Contains(c, "art_doc") || strings.Contains(c, "artf_page") {
			t.Errorf("read %s: only an app's usage and producer are read", c)
		}
	}
	f, write, listed := AppsUpdate(false, apps, nil)
	if !write || !listed || f.Name != ".room/apps.md" {
		t.Fatalf("a room with an app: write=%v listed=%v name=%s", write, listed, f.Name)
	}
	golden(t, "apps.md", f.Content)
	if !strings.Contains(f.Content, "- Friday poll v3, id artf_poll: from maya/codex on core acme; 12 documents: options 1, votes 11\n") {
		t.Errorf("apps.md lacks the app's line:\n%s", f.Content)
	}

	if _, write, listed := AppsUpdate(false, nil, nil); write || listed {
		t.Fatal("a room without apps gets an apps.md")
	}
	if _, write, _ := AppsUpdate(false, nil, errors.New("down")); write {
		t.Fatal("an unreadable room that never had apps gets an apps.md")
	}
	if f, write, listed := AppsUpdate(true, nil, nil); !write || !listed || !strings.Contains(f.Content, "(no apps now)") {
		t.Fatalf("apps.md after the data went is not rewritten: write=%v\n%s", write, f.Content)
	}
	if f, _, _ := AppsUpdate(true, nil, errors.New("down")); !strings.Contains(f.Content, "could not be read this turn") {
		t.Fatalf("a failed read renders:\n%s", f.Content)
	}
	if _, err := ReadApps(context.Background(), ff.fetch, "r1", "", nil); err == nil {
		t.Fatal("apps read without a job's grant")
	}
	if got := AppPath("r1", "artf_poll", "c/votes", "job-1", url.Values{"limit": {"5"}}); got != "/v1/rooms/r1/apps/artf_poll/c/votes?job=job-1&limit=5" {
		t.Fatalf("AppPath = %s", got)
	}
}
