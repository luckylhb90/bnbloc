package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// attachment stores a file maya attached in room (metadata and blob).
func (e *appGW) attachment(room, name, contentType, data string) string {
	e.t.Helper()
	key, size, sha, err := e.blobs.Put(context.Background(), bytes.NewReader([]byte(data)))
	if err != nil {
		e.t.Fatal(err)
	}
	id := ids.New("att_")
	if err := e.b.Attachments.PutAttachment(e.ctx(), store.Attachment{ID: id, RoomID: room, Uploader: "maya", Filename: name, ContentType: contentType,
		Size: size, SHA256: sha, BlobKey: key, CreatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// content stores a component of maya's (kind, name) at 1.0.0 in scope
// (personal: hers alone; org: her organization's) with a manifest of its
// fields.
func (e *appGW) content(scope store.ContentScope, kind, name string, manifest map[string]any) string {
	e.t.Helper()
	id := ids.New("cc_")
	manifest["kind"], manifest["name"], manifest["version"] = kind, name, "1.0.0"
	raw, _ := json.Marshal(manifest)
	if err := e.b.ContentComponents.CreateContentComponent(e.ctx(),
		store.ContentComponent{ID: id, Kind: kind, Name: name, TenantID: e.tenant, Owner: "maya", Scope: scope, Status: store.ContentStatusActive,
			DisplayName: strings.ToUpper(name[:1]) + name[1:], CreatedAtMs: 1, UpdatedAtMs: 1},
		store.ContentComponentVersion{Version: "1.0.0", ManifestJSON: raw, BundleBlobKey: "sha256/" + strings.Repeat("0", 64), SHA256: strings.Repeat("0", 64), SizeBytes: 1, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// recipeRoom is a room where maya's request (job-r, by jack's codex on
// core acme, wearing her organization's expert) drew a chart from two
// attachments, with two skill packs (hers, charts, and her organization's,
// palettes) and her prompt pack on in the room: the chart deliverable and
// the attachments' ids.
func (e *appGW) recipeRoom() (chart, brief, data string) {
	e.t.Helper()
	expert := e.content(store.ContentScopeOrg, "bnbloc.expert.package/v1", "chart-maker", map[string]any{})
	skill := e.content(store.ContentScopePersonal, "bnbloc.skill.package/v1", "charts", map[string]any{"skills": []string{"charts"}})
	orgSkill := e.content(store.ContentScopeOrg, "bnbloc.skill.package/v1", "palettes", map[string]any{"skills": []string{"palettes"}})
	pack := e.content(store.ContentScopePersonal, "bnbloc.prompt.package/v1", "chart-prompts", map[string]any{"prompts": []any{map[string]any{"id": "q3", "title": "Q3", "body": "Q3"}}})
	for _, id := range []string{skill, orgSkill, pack} {
		c, _ := e.b.ContentComponents.GetContentComponent(e.ctx(), id)
		if err := e.b.ContentComponents.PutContentUse(e.ctx(), store.ContentUse{TenantID: e.tenant, ScopeKind: store.ContentUseRoom, ScopeID: e.roomID, ComponentID: id, Kind: c.Kind, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			e.t.Fatal(err)
		}
	}
	brief = e.attachment(e.roomID, "brief.md", "text/markdown", "# Brief\nBars, not pies.\n")
	data = e.attachment(e.roomID, "sales.csv", "text/csv", "q,sales\n3,10\n")
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-r", RoomID: e.roomID, MessageSeq: 5, RequesterUserID: "maya", OwnerUserID: "jack",
		Core: "acme", Provider: "codex", MessageText: "Draw the Q3 sales as a bar chart", AttachmentIDs: []string{brief, data},
		ExpertID: expert, ExpertVersion: "1.0.0", ExpertName: "chart-maker", Status: store.AgentJobReplied, CreatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
	chart = e.realFile(e.roomID, store.ArtifactKindImage, "q3.png", "image/png", []byte(coverPNG(e.t)), 5)
	v, err := e.b.Artifacts.GetArtifactVersion(e.ctx(), chart, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	v.SourceJobID = "job-r"
	// The version's producing job (the bridge names it when it publishes).
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), chart, store.ArtifactVersion{Storage: v.Storage, BlobKey: v.BlobKey, ContentHash: v.ContentHash,
		MimeType: v.MimeType, SizeBytes: v.SizeBytes, AuthorUserID: "maya", CreatedAtMs: 2, SourceJobID: "job-r", SourceSeq: 5}, 1); err != nil {
		e.t.Fatal(err)
	}
	return chart, brief, data
}

// recipeOf is what publishing a deliverable would capture, as subject.
func (e *appGW) recipeOf(subject, artifact string) wire.WorkRecipeCapture {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+artifact+"/recipe", nil, e.as(subject))
	var c wire.WorkRecipeCapture
	if r.status != http.StatusOK || json.Unmarshal(r.body, &c) != nil {
		e.t.Fatalf("recipe of %s = %d %s", artifact, r.status, r.body)
	}
	return c
}

// The recipe is captured from the job that produced the deliverable
// version: the request's text, its expert, the packs in effect and its
// attachments. An uploaded deliverable has none.
func TestRecipeCapturedFromTheProducingJob(t *testing.T) {
	e := galleryGW(t)
	chart, brief, data := e.recipeRoom()
	c := e.recipeOf("maya", chart)
	r := c.Recipe
	if c.Version != 2 || r == nil {
		t.Fatalf("capture = %+v", c)
	}
	if r.Prompt != "Draw the Q3 sales as a bar chart" || r.Expert == nil || r.Expert.Name != "chart-maker" || r.Expert.Version != "1.0.0" ||
		r.Agent == nil || r.Agent.Core != "acme" || r.Agent.Provider != "codex" {
		t.Fatalf("recipe = %+v", r)
	}
	skills := []string{}
	for _, p := range r.Skills {
		skills = append(skills, p.Name)
	}
	slices.Sort(skills)
	if strings.Join(skills, ",") != "charts,palettes" || len(r.Prompts) != 1 || r.Prompts[0].Name != "chart-prompts" {
		t.Fatalf("recipe packs = %+v %+v", r.Skills, r.Prompts)
	}
	if len(r.References) != 2 || r.References[0].AttachmentID != brief || r.References[0].Name != "brief.md" || r.References[0].ContentType != "text/markdown" ||
		r.References[1].AttachmentID != data || r.References[1].SizeBytes != int64(len("q,sales\n3,10\n")) {
		t.Fatalf("recipe references = %+v", r.References)
	}
	// Someone who may not post in the room reads no recipe.
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+chart+"/recipe", nil, e.as("olaf")); r.status != http.StatusForbidden {
		t.Fatalf("an outsider's capture = %d %s", r.status, r.body)
	}
	// An uploaded file: no producing job, no recipe.
	upload := e.realFile(e.roomID, store.ArtifactKindImage, "upload.png", "image/png", []byte(coverPNG(t)), 0)
	if c := e.recipeOf("maya", upload); c.Recipe != nil {
		t.Fatalf("an upload's recipe = %+v", c.Recipe)
	}
	if w := e.publishApp("maya", upload, wire.WorkPublishRequest{Name: "upload"}); w.Recipe != nil {
		t.Fatalf("an upload's work carries a recipe: %+v", w.Recipe)
	}

	// Published as captured: the references ride in the bundle.
	w := e.publishApp("maya", chart, wire.WorkPublishRequest{Name: "q3-chart"})
	if w.Recipe == nil || w.Recipe.Prompt != r.Prompt || len(w.Recipe.References) != 2 || w.Recipe.References[0].File != "references/brief.md" || w.Recipe.References[0].AttachmentID != "" {
		t.Fatalf("the published recipe = %+v", w.Recipe)
	}
	if got := strings.Join(e.versionFiles("maya", w.ID, "1.0.0"), ","); got != "component.json,q3.png,references/brief.md,references/sales.csv" {
		t.Fatalf("the work's files = %s", got)
	}
	// A related file downloads by its path, named as attached.
	if r := e.call(http.MethodGet, "/v1/content-components/"+w.ID+"/versions/1.0.0/file?path=references/brief.md", nil, e.as("maya")); r.status != http.StatusOK ||
		string(r.body) != "# Brief\nBars, not pies.\n" || !strings.Contains(r.header.Get("Content-Disposition"), "brief.md") {
		t.Fatalf("a reference file = %d %v", r.status, r.header)
	}
}

// The author decides what is published: an edited prompt, references
// unticked, or no recipe at all. A reference the request did not attach
// is refused.
func TestRecipeAuthorEditRespected(t *testing.T) {
	e := galleryGW(t)
	chart, brief, _ := e.recipeRoom()
	captured := e.recipeOf("maya", chart).Recipe
	edit := *captured
	edit.Prompt = "Draw quarterly sales as a bar chart"
	edit.References = []wire.WorkReference{{AttachmentID: brief}}
	w := e.publishApp("maya", chart, wire.WorkPublishRequest{Name: "edited", Recipe: &edit})
	if w.Recipe == nil || w.Recipe.Prompt != edit.Prompt || len(w.Recipe.References) != 1 || w.Recipe.References[0].Name != "brief.md" {
		t.Fatalf("the edited recipe = %+v", w.Recipe)
	}
	if got := strings.Join(e.versionFiles("maya", w.ID, "1.0.0"), ","); got != "component.json,q3.png,references/brief.md" {
		t.Fatalf("the edited work's files = %s", got)
	}
	// A new request text only (the producing message was "publish it"): the
	// rest of the recipe stays as captured.
	w = e.publishApp("maya", chart, wire.WorkPublishRequest{Name: "reworded", RecipePrompt: "  Chart quarterly sales by region  "})
	if w.Recipe == nil || w.Recipe.Prompt != "Chart quarterly sales by region" || len(w.Recipe.References) != len(captured.References) {
		t.Fatalf("the reworded recipe = %+v", w.Recipe)
	}
	off := false
	if w := e.publishApp("maya", chart, wire.WorkPublishRequest{Name: "no-recipe", IncludeRecipe: &off}); w.Recipe != nil {
		t.Fatalf("a dropped recipe = %+v", w.Recipe)
	}
	other := e.attachment(e.roomID, "secret.txt", "text/plain", "not in the request")
	forged := edit
	forged.References = []wire.WorkReference{{AttachmentID: other}}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+chart+"/publish-work", wire.WorkPublishRequest{Name: "forged", Recipe: &forged}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("a reference the request did not attach = %d %s", r.status, r.body)
	}
	long := edit
	long.Prompt = strings.Repeat("x", wire.MaxChatTextBytes+1)
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+chart+"/publish-work", wire.WorkPublishRequest{Name: "long", Recipe: &long}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("an overlong prompt = %d %s", r.status, r.body)
	}
}

// An agent publishing through the room tools includes the recipe and is
// told to say so.
func TestAgentPublishesWorkWithRecipe(t *testing.T) {
	e := galleryGW(t)
	chart, _, _ := e.recipeRoom()
	e.runJob("job-1", e.roomID)
	r := e.roomToolAs(e.roomID, "job-1", "save_component", map[string]any{"artifactId": chart, "name": "q3-chart"})
	if r.Err != "" {
		t.Fatalf("save_component = %+v", r)
	}
	out, _ := r.Value.(map[string]any)
	recipe, _ := out["recipe"].(map[string]any)
	note, _ := out["note"].(string)
	if out["kind"] != "work" || recipe["request"] != true || recipe["expert"] != "chart-maker" || !strings.Contains(note, "recipe") {
		t.Fatalf("save_component = %v", out)
	}
	if refs, _ := recipe["references"].([]string); len(refs) != 2 || refs[0] != "brief.md" {
		t.Fatalf("the recipe's references = %v", recipe["references"])
	}
}
