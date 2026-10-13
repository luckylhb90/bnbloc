package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// makeSimilar asks for a draft of work as subject (201 expected).
func (e *appGW) makeSimilar(subject, work string, in wire.MakeSimilarRequest) wire.MakeSimilarDraft {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/content-components/"+work+"/make-similar", in, e.as(subject))
	var d wire.MakeSimilarDraft
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &d) != nil {
		e.t.Fatalf("make-similar as %s = %d %s", subject, r.status, r.body)
	}
	return d
}

// publishToOrg publishes maya's work to her organization, which publishes
// without approval: jack's gallery entry of it.
func (e *appGW) publishToOrg(work wire.ContentComponent) *wire.ContentComponent {
	e.t.Helper()
	none := wire.PublishApprovalNone
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{PublishApproval: &none}); err != nil {
		e.t.Fatal(err)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+work.ID+"/publish", wire.ContentPublishInput{Scope: "org"}, e.as("maya")); r.status != http.StatusCreated {
		e.t.Fatalf("publish = %d %s", r.status, r.body)
	}
	org := e.libraryApp("jack", work.Name)
	if org == nil || org.Scope != "org" {
		e.t.Fatalf("jack's gallery has no %s: %+v", work.Name, org)
	}
	return org
}

// attachmentBytes downloads an attachment of room as subject.
func (e *appGW) attachmentBytes(subject, room, id string) string {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/rooms/"+room+"/attachments/"+id, nil, e.as(subject))
	if r.status != http.StatusOK {
		e.t.Fatalf("attachment %s = %d %s", id, r.status, r.body)
	}
	return string(r.body)
}

// "Make similar": the recipe's references become the member's attachments
// in the room, the packs they may see are turned on there, the expert they
// may use is picked, the rest is reported; it counts as a use. A work the
// member cannot see, or a room they do not own, is refused.
func TestMakeSimilar(t *testing.T) {
	e := galleryGW(t)
	chart, _, _ := e.recipeRoom()
	work := e.publishApp("maya", chart, wire.WorkPublishRequest{Name: "q3-chart"})
	if r := e.call(http.MethodPost, "/v1/content-components/"+work.ID+"/make-similar", wire.MakeSimilarRequest{NewRoom: &wire.WorkNewRoom{}}, e.as("jack")); r.status != http.StatusNotFound {
		t.Fatalf("someone else's personal work = %d %s", r.status, r.body)
	}
	org := e.publishToOrg(work)
	if r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/make-similar", wire.MakeSimilarRequest{NewRoom: &wire.WorkNewRoom{}}, e.as("olaf")); r.status != http.StatusNotFound {
		t.Fatalf("another organization's work = %d %s", r.status, r.body)
	}
	visited := e.newRoom("maya")
	if err := e.b.Room.AddMember(e.ctx(), store.Membership{RoomID: visited, UserID: "jack", Role: store.RoleVisitor, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{visited, e.newRoom("maya")} {
		if r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/make-similar", wire.MakeSimilarRequest{RoomID: room}, e.as("jack")); r.status != http.StatusForbidden {
			t.Fatalf("a room jack does not own = %d %s", r.status, r.body)
		}
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/make-similar", wire.MakeSimilarRequest{}, e.as("jack")); r.status != http.StatusBadRequest {
		t.Fatalf("no room = %d %s", r.status, r.body)
	}

	// A new room of jack's: the draft.
	d := e.makeSimilar("jack", org.ID, wire.MakeSimilarRequest{NewRoom: &wire.WorkNewRoom{Name: "My Q4 chart"}})
	if !d.CreatedRoom || d.RoomID == "" || d.RoomID == e.roomID || d.Text != "Draw the Q3 sales as a bar chart" ||
		d.Expert == nil || d.Expert.Name != "chart-maker" || d.Expert.Version != "1.0.0" {
		t.Fatalf("draft = %+v", d)
	}
	if room, err := e.b.Room.GetRoom(e.ctx(), d.RoomID); err != nil || room.Name != "My Q4 chart" {
		t.Fatalf("the new room = %+v, %v", room, err)
	}
	// The references are jack's attachments in the new room.
	if len(d.AttachmentIDs) != 2 || len(d.Attachments) != 2 || d.Attachments[0].ID != d.AttachmentIDs[0] ||
		d.Attachments[0].Filename != "brief.md" || d.Attachments[1].Filename != "sales.csv" || d.Attachments[1].ContentType != "text/csv" {
		t.Fatalf("draft attachments = %+v", d.Attachments)
	}
	for i, id := range d.AttachmentIDs {
		att, ok, err := e.b.Attachments.GetAttachment(e.ctx(), d.RoomID, id)
		if err != nil || !ok || att.Uploader != "jack" {
			t.Fatalf("attachment %s = %+v %v %v (want jack's, in the new room)", id, att, ok, err)
		}
		if got, want := e.attachmentBytes("jack", d.RoomID, id), []string{"# Brief\nBars, not pies.\n", "q,sales\n3,10\n"}[i]; got != want {
			t.Fatalf("attachment %s = %q, want %q", id, got, want)
		}
	}
	// Only what jack sees is on: the organization's skill pack; maya's own
	// skill pack and prompt pack are reported left out.
	uses, err := e.b.ContentComponents.ListContentUses(e.ctx(), store.ContentUseRoom, d.RoomID)
	if err != nil || len(uses) != 1 {
		t.Fatalf("the new room's uses = %+v, %v", uses, err)
	}
	if c, _ := e.b.ContentComponents.GetContentComponent(e.ctx(), uses[0].ComponentID); c.Name != "palettes" {
		t.Fatalf("turned on %s, want palettes", c.Name)
	}
	if !slices.Equal(d.Enabled, []string{"skill pack palettes"}) || !slices.Contains(d.Skipped, "skill pack charts") || !slices.Contains(d.Skipped, "prompt pack Chart-prompts") {
		t.Fatalf("enabled %v, skipped %v", d.Enabled, d.Skipped)
	}

	// A room jack may post in but does not run: the files come, the packs
	// are his to ask for.
	in := e.makeSimilar("jack", org.ID, wire.MakeSimilarRequest{RoomID: e.roomID})
	if in.CreatedRoom || in.RoomID != e.roomID || len(in.AttachmentIDs) != 2 || len(in.Enabled) != 0 ||
		!slices.ContainsFunc(in.Skipped, func(s string) bool { return strings.Contains(s, "palettes (only the room's owner") }) {
		t.Fatalf("draft in maya's room = %+v", in)
	}
	// Each draft is a use of the work.
	if got := e.libraryApp("jack", "q3-chart"); got == nil || got.Uses != 2 {
		t.Fatalf("uses = %+v", got)
	}
}

// An empty recipe still makes a draft: no text, and the work itself as the
// reference (an image as itself, a site's files as a zip).
func TestMakeSimilarEmptyRecipe(t *testing.T) {
	e := galleryGW(t)
	png := coverPNG(t)
	upload := e.publishApp("maya", e.realFile(e.roomID, store.ArtifactKindImage, "logo.png", "image/png", []byte(png), 0), wire.WorkPublishRequest{Name: "logo"})
	if upload.Recipe != nil {
		t.Fatalf("an upload's recipe = %+v", upload.Recipe)
	}
	d := e.makeSimilar("maya", upload.ID, wire.MakeSimilarRequest{RoomID: e.roomID})
	if d.Text != "" || d.Expert != nil || len(d.AttachmentIDs) != 1 || d.Attachments[0].Filename != "logo.png" || d.Attachments[0].ContentType != "image/png" {
		t.Fatalf("an empty recipe's draft = %+v", d)
	}
	if got := e.attachmentBytes("maya", e.roomID, d.AttachmentIDs[0]); got != png {
		t.Fatal("the attached work is not the image")
	}
	site := e.publishApp("maya", e.pollSite(), wire.WorkPublishRequest{Name: "poll"})
	d = e.makeSimilar("maya", site.ID, wire.MakeSimilarRequest{NewRoom: &wire.WorkNewRoom{}})
	if len(d.Attachments) != 1 || d.Attachments[0].Filename != "poll.zip" || d.Attachments[0].ContentType != "application/zip" {
		t.Fatalf("a site's draft = %+v", d)
	}
	r := e.call(http.MethodGet, "/v1/rooms/"+d.RoomID+"/attachments/"+d.AttachmentIDs[0], nil, e.as("maya"))
	if r.status != http.StatusOK || !strings.HasPrefix(string(r.body), "PK") {
		t.Fatalf("the site's zip = %d", r.status)
	}
}
