package bridge

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// filesCore is fsCore that also lists and reads its workspaces' files
// exactly (sessionFiles, sessionFileBytes), so the bridge publishes what
// the agent wrote.
type filesCore struct{ *fsCore }

func (c filesCore) Supports(_ context.Context, _ corepkg.Endpoint, capability corepkg.Capability) bool {
	switch capability {
	case corepkg.CapabilityAgentProfile, corepkg.CapabilityWriteFile, corepkg.CapabilitySessionFiles, corepkg.CapabilitySessionFileBytes:
		return true
	}
	return false
}

func (c filesCore) wsOf(sess string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		if s.id == sess {
			return s.workspace
		}
	}
	return ""
}

func (c filesCore) ListSessionFiles(_ context.Context, _ corepkg.Endpoint, ws, _ string) ([]corepkg.SessionFile, error) {
	c.mu.Lock()
	rels := c.listLocked(ws)
	c.mu.Unlock()
	var out []corepkg.SessionFile
	for _, rel := range rels {
		fi, err := os.Stat(filepath.Join(c.dir(ws), filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		out = append(out, corepkg.SessionFile{Path: rel, Bytes: fi.Size(), MtimeMs: fi.ModTime().UnixMilli()})
	}
	return out, nil
}

func (c filesCore) ReadSessionFile(_ context.Context, _ corepkg.Endpoint, ws, _, rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(c.dir(ws), filepath.FromSlash(rel)))
}

func editBridge(t *testing.T) (*Bridge, *pullGateway, filesCore) {
	t.Helper()
	g := newPullGateway(t)
	g.artifacts = `{"artifacts":[],"maxBytes":1048576}`
	g.details = map[string]string{
		"art_deck": `{"artifact":{"artifactId":"art_deck","roomId":"r1","kind":"file","title":"deck.pptx","fileName":"deck.pptx","currentVersion":2},"versions":[]}`,
	}
	g.contents["art_deck/2"] = "deck v2"
	c := filesCore{newFSCore(t)}
	b := New(Config{
		GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya",
		Cores: []CoreConfig{{Name: "core-a", Provider: c, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	ts, _ := c.ListTargets(context.Background(), corepkg.Endpoint{})
	b.cacheTargets("core-a", ts)
	return b, g, c
}

func editDrive(t *testing.T, b *Bridge, id, text string, ref *wire.FileRef) {
	t.Helper()
	d := testDrive(b, jobRef{ID: id, RequesterUserID: "maya", MessageSeq: 9}, "r1", "maya", "core-a", "agent-a", "t", text, "general")
	d.job.FileRef = ref
	if _, err := b.driveClaim(context.Background(), d, func(rs *roomSession) { d.run.bind(d.core, rs) }); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	// What settle does before reporting the outcome: the turn's publish.
	b.publishTurn(context.Background(), d)
}

func fileNotices(g *pullGateway) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, r := range g.reports {
		if strings.Contains(r, `"`+wire.NoticeFileEdit+`"`) {
			out = append(out, r)
		}
	}
	return out
}

// docs/prd/2026-10-14-ai-document-editing.md rules 3–4: the file a request
// names is placed in the workspace (another's under edits/), the turn input
// names it in one line, and the agent's rewrite publishes as the next
// version of THAT file, recording the version the member was looking at.
// A turn that leaves it as it was says so in the room.
func TestEditRequestPublishesTheNextVersionOfTheFile(t *testing.T) {
	b, g, c := editBridge(t)
	ref := &wire.FileRef{ArtifactID: "art_deck", Version: 2, Name: "deck.pptx"}
	c.agentWrites(map[string]string{"edits/deck.pptx": "deck v3"})
	editDrive(t, b, "job-1", "shorten slide 4's title", ref)

	_, sessions := c.snapshot()
	prompt := sessions[0].turns[0]
	if got, err := os.ReadFile(filepath.Join(c.root, "ws1", ".room/edits/art_deck/deck.pptx")); err != nil || string(got) != "deck v2" {
		t.Fatalf("placed version = %q, %v", got, err)
	}
	if !strings.Contains(prompt, "[edit deck.pptx v2: it is at .room/edits/art_deck/deck.pptx; save your version as edits/deck.pptx, which updates the room's file]") {
		t.Fatalf("prompt lacks the edit line:\n%s", prompt)
	}
	g.mu.Lock()
	published, created := append([]testPublish(nil), g.published...), append([]string(nil), g.created...)
	g.mu.Unlock()
	if len(published) != 1 || published[0].ArtifactID != "art_deck" || published[0].Content != "deck v3" ||
		published[0].Fields["editedFrom"] != "2" || published[0].Fields["sourceJobId"] != "job-1" || !strings.Contains(published[0].Fields["summary"], "from v2") {
		t.Fatalf("published = %+v", published)
	}
	if len(created) != 0 {
		t.Fatalf("an edit created artifacts %v: it must publish to the file it edits", created)
	}
	if n := fileNotices(g); len(n) != 0 {
		t.Fatalf("an edited file got a notice: %q", n)
	}

	// Asked again about the new version, the agent leaves the file as it
	// was: no version, one calm notice.
	g.mu.Lock()
	g.contents["art_deck/3"] = "deck v3"
	g.mu.Unlock()
	editDrive(t, b, "job-2", "make it bolder", &wire.FileRef{ArtifactID: "art_deck", Version: 3, Name: "deck.pptx"})
	g.mu.Lock()
	published = append([]testPublish(nil), g.published...)
	g.mu.Unlock()
	if len(published) != 1 {
		t.Fatalf("an unchanged file published again: %+v", published)
	}
	if n := fileNotices(g); len(n) != 1 || !strings.Contains(n[0], `"text":"No changes to deck.pptx."`) ||
		!strings.Contains(n[0], `"agentSessionId":"`+wire.FileEditNoticeSessionID("job-2")+`"`) {
		t.Fatalf("notices = %q", n)
	}

	// A later turn that rewrites the placed copy without being asked about
	// the file still versions that file, never a new edits/deck.pptx.
	c.agentWrites(map[string]string{"edits/deck.pptx": "deck v4"})
	editDrive(t, b, "job-3", "one more pass on the deck", nil)
	g.mu.Lock()
	published, created = append([]testPublish(nil), g.published...), append([]string(nil), g.created...)
	g.mu.Unlock()
	if len(published) != 2 || published[1].ArtifactID != "art_deck" || published[1].Content != "deck v4" || len(created) != 0 {
		t.Fatalf("a later rewrite = %+v, created %v", published, created)
	}
}

// The agent's own file is edited where it lives; a file that cannot be
// fetched is named to the agent and to the room, never silently dropped.
func TestEditRequestOwnFileAndUnreachableFile(t *testing.T) {
	b, g, c := editBridge(t)
	d := testDrive(b, jobRef{ID: "probe", RequesterUserID: "maya"}, "r1", "maya", "core-a", "agent-a", "t", "x", "general")
	own := artifactIDForSeat("r1", d.key(), "core-a", "agent-a", "maya", "work/out/report.md")
	g.mu.Lock()
	g.details[own] = `{"artifact":{"artifactId":"` + own + `","roomId":"r1","kind":"markdown","title":"work/out/report.md","fileName":"report.md","currentVersion":1},"versions":[]}`
	g.contents[own+"/1"] = "# Report\n"
	g.details["art_gone"] = `{"artifact":{"artifactId":"art_gone","roomId":"r1","kind":"file","title":"old.xlsx","fileName":"old.xlsx","currentVersion":1},"versions":[]}`
	g.mu.Unlock()

	c.agentWrites(map[string]string{"work/out/report.md": "# Report\n\nShorter.\n"})
	editDrive(t, b, "job-own", "tighten it", &wire.FileRef{ArtifactID: own, Version: 1, Name: "report.md"})
	_, sessions := c.snapshot()
	if p := sessions[0].turns[0]; !strings.Contains(p, "save your version as work/out/report.md,") {
		t.Fatalf("own file prompt:\n%s", p)
	}
	g.mu.Lock()
	published := append([]testPublish(nil), g.published...)
	g.mu.Unlock()
	if len(published) != 1 || published[0].ArtifactID != own || published[0].Fields["editedFrom"] != "1" {
		t.Fatalf("own file published = %+v", published)
	}

	editDrive(t, b, "job-gone", "fix the totals", &wire.FileRef{ArtifactID: "art_gone", Version: 1, Name: "old.xlsx"})
	_, sessions = c.snapshot()
	turns := sessions[len(sessions)-1].turns
	if p := turns[len(turns)-1]; !strings.Contains(p, "[the file to edit, old.xlsx v1, is NOT in this workspace: it could not be downloaded from the room — tell the sender]") {
		t.Fatalf("unreachable file prompt:\n%s", p)
	}
	if n := fileNotices(g); len(n) != 1 || !strings.Contains(n[0], "Could not give agent-a old.xlsx v1 to edit: it could not be downloaded from the room.") {
		t.Fatalf("notices = %q", n)
	}
	if _, err := os.Stat(filepath.Join(c.root, "ws1", path.Join("edits", "old.xlsx"))); err == nil {
		t.Fatal("an unreachable file was written")
	}
}
