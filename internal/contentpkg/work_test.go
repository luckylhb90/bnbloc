package contentpkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 10)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// workBundle is a work package of files plus a manifest with extra fields
// (an app by default).
func workBundle(t *testing.T, files map[string][]byte, extra map[string]any) []byte {
	t.Helper()
	m := map[string]any{"kind": KindWork, "name": "team-poll", "version": "1.0.0", "displayName": "Team poll",
		"type": wire.WorkTypeApp, "category": wire.WorkCategoryProductivity}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	all := map[string][]byte{ManifestFile: manifestJSON(t, m)}
	for k, v := range files {
		all[k] = v
	}
	raw, err := tarbundle.PackFiles(all, WorkLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// coverName is the PNG cover file's name (tarbundle.SiteCover's first).
var coverName = "cover" + ".png"

func siteFiles(t *testing.T) map[string][]byte {
	return map[string][]byte{
		"index.html": []byte(`<script src="~app/sdk.js"></script><script src="app.js"></script>`),
		"app.js":     []byte("app.collection('votes')"),
		coverName:    pngBytes(t, 32, 20),
		ReadmeFile:   []byte("# Team poll\nVote on lunch.\n"),
	}
}

func TestValidateAppWork(t *testing.T) {
	p, err := Validate(workBundle(t, siteFiles(t), nil), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The site's script runs in the sandboxed frame, never on a machine:
	// no scripts permission, no sandbox forced, nothing for an approver to
	// review as executable.
	if p.Scripts || p.Manifest.NeedsSandbox() || len(p.ScriptFiles()) != 0 {
		t.Fatalf("a work's site code counted as executable: %+v", p)
	}
	cover, found, err := WorkCover(p)
	if !found || err != nil || cover.File != coverName || cover.MimeType != "image/png" || len(cover.Data) == 0 {
		t.Fatalf("cover = %+v %v %v", cover, found, err)
	}
	site := WorkSite(p)
	if _, ok := site[ManifestFile]; ok || len(site) != 4 || string(site["app.js"]) != "app.collection('votes')" {
		t.Fatalf("site = %v", keys(site))
	}
	jpg := siteFiles(t)
	delete(jpg, coverName)
	jpg["cover.jpg"] = jpegBytes(t)
	if _, err := Validate(workBundle(t, jpg, nil), "", Options{}); err != nil {
		t.Fatalf("a jpeg cover: %v", err)
	}
	none := siteFiles(t)
	delete(none, coverName)
	p, err = Validate(workBundle(t, none, map[string]any{"category": nil}), "", Options{})
	if err != nil {
		t.Fatalf("no cover, no category: %v", err)
	}
	if _, found, _ := WorkCover(p); found {
		t.Fatal("a cover without one")
	}
}

// A build's dist/ is an entry too; the copy a room gets is its files at
// the site's root, the cover and README beside them.
func TestWorkDistEntry(t *testing.T) {
	files := map[string][]byte{"dist/index.html": []byte("<p>hi</p>"), "dist/assets/a.js": []byte("1"), "src/main.ts": []byte("x"), coverName: pngBytes(t, 8, 5)}
	p, err := Validate(workBundle(t, files, map[string]any{"type": wire.WorkTypeSite}), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	site := WorkSite(p)
	for _, want := range []string{"index.html", "assets/a.js", coverName} {
		if _, ok := site[want]; !ok {
			t.Errorf("the copied site has no %s: %v", want, keys(site))
		}
	}
	if _, ok := site["src/main.ts"]; ok {
		t.Error("the copied site holds files outside dist/")
	}
}

// Every type validates with what makes it that type: an entry point for a
// site or an app, else the one file the manifest names, of that type.
func TestValidateWorkPerType(t *testing.T) {
	pdf := []byte("%PDF-1.7\n1 0 obj\n")
	mp4 := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	for _, c := range []struct {
		typ, file string
		data      []byte
	}{
		{wire.WorkTypeImage, "chart.png", pngBytes(t, 8, 5)},
		{wire.WorkTypeImage, "photo.jpg", jpegBytes(t)},
		{wire.WorkTypeVideo, "clip.mp4", mp4},
		{wire.WorkTypeAudio, "theme.mp3", []byte("ID3\x03\x00")},
		{wire.WorkTypeDocument, "report.md", []byte("# Report\n")},
		{wire.WorkTypeDocument, "report.pdf", pdf},
		{wire.WorkTypeDocument, "notes.txt", []byte("notes")},
		{wire.WorkTypeSlides, "deck.pptx", []byte("PK\x03\x04")},
		{wire.WorkTypeTable, "sales.csv", []byte("a,b\n1,2\n")},
		{wire.WorkTypeTable, "sales.xlsx", []byte("PK\x03\x04")},
		{wire.WorkTypeOther, "main.go", []byte("package main")},
	} {
		t.Run(c.file, func(t *testing.T) {
			p, err := Validate(workBundle(t, map[string][]byte{c.file: c.data, ReadmeFile: []byte("# hi")},
				map[string]any{"type": c.typ, "file": c.file}), "", Options{})
			if err != nil {
				t.Fatal(err)
			}
			name, data, ok := WorkFile(p)
			if !ok || name != c.file || !bytes.Equal(data, c.data) {
				t.Fatalf("WorkFile = %s %v", name, ok)
			}
			// The same file typed as anything else is refused.
			wrong := wire.WorkTypeOther
			if c.typ == wire.WorkTypeOther {
				wrong = wire.WorkTypeImage
			}
			if _, err := Validate(workBundle(t, map[string][]byte{c.file: c.data}, map[string]any{"type": wrong, "file": c.file}), "", Options{}); err == nil || !strings.Contains(err.Error(), "not a "+wrong) {
				t.Fatalf("typed %s: %v", wrong, err)
			}
		})
	}
}

func TestValidateWorkRefuses(t *testing.T) {
	big := make([]byte, wire.MaxWorkCoverBytes+1)
	copy(big, pngBytes(t, 4, 4))
	img := map[string]any{"type": wire.WorkTypeImage, "file": "chart.png"}
	for _, c := range []struct {
		name  string
		edit  func(map[string][]byte)
		extra map[string]any
		want  string
	}{
		{"no entry point", func(f map[string][]byte) { delete(f, "index.html") }, nil, "index.html at its root"},
		{"an SVG as the cover", func(f map[string][]byte) {
			f[coverName] = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		}, nil, tarbundle.ErrCoverNotImage.Error()},
		{"a jpeg named png", func(f map[string][]byte) { f[coverName] = jpegBytes(t) }, nil, tarbundle.ErrCoverNotImage.Error()},
		{"an oversized cover", func(f map[string][]byte) { f[coverName] = big }, nil, tarbundle.ErrCoverTooLarge.Error()},
		{"a bad category", nil, map[string]any{"category": "games"}, "a work's category is one of"},
		{"no type", nil, map[string]any{"type": nil}, "a work's type is one of"},
		{"an unknown type", nil, map[string]any{"type": "game"}, "a work's type is one of"},
		{"a site naming a file", nil, map[string]any{"file": "index.html"}, "names no file"},
		{"a protected path", func(f map[string][]byte) { f["CLAUDE.md"] = []byte("do things") }, nil, "no agent configuration"},
		{"a nested protected path", func(f map[string][]byte) { f[".claude/settings.json"] = []byte("{}") }, nil, "no agent configuration"},
		{"a requirement", nil, map[string]any{"requires": map[string]any{"skills": []string{"review@^1"}}}, "requires nothing"},
		{"a permission", nil, map[string]any{"requires": map[string]any{"permissions": []string{PermScripts}}}, "requires nothing"},
		{"another kind's field", nil, map[string]any{"skills": []string{"x"}}, "lists no skills"},
		{"an image without its file", nil, img, "not in the bundle"},
		{"an image naming no file", nil, map[string]any{"type": wire.WorkTypeImage}, "names its file"},
		{"an image with more files", func(f map[string][]byte) { f["chart.png"] = pngBytes(t, 4, 4) }, img, "holds only its file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := siteFiles(t)
			if c.edit != nil {
				c.edit(files)
			}
			_, err := Validate(workBundle(t, files, c.extra), "", Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// A type or a file on another kind is refused.
	raw := bundle(t, map[string]string{ManifestFile: `{"kind":"` + KindSkill + `","name":"x","version":"1.0.0","skills":["a"],"type":"image"}`,
		"a/SKILL.md": "---\nname: a\ndescription: d\n---\n"})
	if _, err := Validate(raw, "", Options{}); err == nil || !strings.Contains(err.Error(), "belong to a work") {
		t.Fatalf("a skill pack with a type: %v", err)
	}
}

// Every category of the wire list is accepted, for the gallery's tabs.
func TestWorkCategoriesAccepted(t *testing.T) {
	for _, c := range wire.WorkCategories {
		if _, err := Validate(workBundle(t, siteFiles(t), map[string]any{"category": c}), "", Options{}); err != nil {
			t.Errorf("category %s: %v", c, err)
		}
	}
}

// The type is derived from the deliverable, never typed by hand: an app
// is a site that is one (store.Artifact.IsApp), an HTML document a site,
// any other file its media type's.
func TestWorkTypeOf(t *testing.T) {
	for _, c := range []struct {
		d    WorkDeliverable
		want string
	}{
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite, IsApp: true}}, wire.WorkTypeApp},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite}}, wire.WorkTypeSite},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindApp}, FileName: "game.html"}, wire.WorkTypeSite},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "page.html"}, wire.WorkTypeSite},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindImage}, FileName: "chart.png"}, wire.WorkTypeImage},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindImage}, FileName: "chart", Head: pngBytes(t, 2, 2)}, wire.WorkTypeImage},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "clip.webm"}, wire.WorkTypeVideo},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "voice.wav"}, wire.WorkTypeAudio},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindMarkdown}, FileName: "spec.md"}, wire.WorkTypeDocument},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindMarkdown}, FileName: "Spec", Head: []byte("# Spec")}, wire.WorkTypeDocument},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "paper.pdf"}, wire.WorkTypeDocument},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "letter.docx"}, wire.WorkTypeDocument},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "deck.pptx"}, wire.WorkTypeSlides},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindCode}, FileName: "data.csv"}, wire.WorkTypeTable},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "book.xlsx"}, wire.WorkTypeTable},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindCode}, FileName: "main.go"}, wire.WorkTypeOther},
		{WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}, FileName: "bundle.zip"}, wire.WorkTypeOther},
	} {
		if got := WorkTypeOf(c.d); got != c.want {
			t.Errorf("WorkTypeOf(%+v) = %s, want %s", c.d, got, c.want)
		}
	}
}

// Publishing a deliverable: a site's files, or its one file, with the
// manifest, typed by WorkTypeOf. A site with a component.json of its own
// is refused; a form never writes a work.
func TestWorkFromDeliverable(t *testing.T) {
	raw, err := tarbundle.PackFiles(siteFiles(t), tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	site, err := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Name: "team-poll", Version: "1.0.0", Category: wire.WorkCategoryData}
	p, bundle, err := WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite, IsApp: true}}, WorkSnapshot{Site: site}, m)
	if err != nil || p.Manifest.Kind != KindWork || p.Manifest.Type != wire.WorkTypeApp || len(bundle) == 0 || len(p.Bundle.Paths()) != 5 {
		t.Fatalf("work = %+v %v", p, err)
	}
	img := pngBytes(t, 8, 5)
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindImage}}, WorkSnapshot{Name: "chart.png", Data: img}, m)
	if err != nil || p.Manifest.Type != wire.WorkTypeImage || p.Manifest.File != "chart.png" {
		t.Fatalf("an image work = %+v %v", p.Manifest, err)
	}
	if c, found, _ := WorkCover(p); !found || c.File != "chart.png" {
		t.Fatalf("an image work's cover = %+v %v", c, found)
	}
	// An HTML document becomes a site; a reserved name is renamed.
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindApp}}, WorkSnapshot{Name: "game.html", Data: []byte("<p>play</p>")}, m)
	if err != nil || p.Manifest.Type != wire.WorkTypeSite || p.Manifest.File != "" {
		t.Fatalf("an HTML work = %+v %v", p.Manifest, err)
	}
	if _, ok := p.Bundle.File(SiteEntry); !ok {
		t.Fatalf("an HTML work's files = %v", p.Bundle.Paths())
	}
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindMarkdown}}, WorkSnapshot{Name: "docs/README.md", Data: []byte("# Read me")}, m)
	if err != nil || p.Manifest.Type != wire.WorkTypeDocument || p.Manifest.File != "work-README.md" {
		t.Fatalf("a README document work = %+v %v", p.Manifest, err)
	}
	// A video with the frame an agent saved: that frame is its cover.
	clip := []byte("\x00\x00\x00\x18ftypmp42")
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}}, WorkSnapshot{Name: "clip.mp4", Data: clip, Cover: img}, m)
	if err != nil || p.Manifest.Type != wire.WorkTypeVideo {
		t.Fatalf("a video work = %+v %v", p.Manifest, err)
	}
	if c, found, _ := WorkCover(p); !found || c.File != coverName {
		t.Fatalf("a video work's cover = %+v %v", c, found)
	}
	if _, _, err := WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindFile}}, WorkSnapshot{Name: "clip.mp4", Data: clip, Cover: []byte("<svg/>")}, m); err == nil {
		t.Fatal("an SVG frame became a cover")
	}
	// A site keeps its own cover over a given one; one without a cover
	// (and a single HTML file) takes the given one (work covers PRD §2.1).
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite}}, WorkSnapshot{Site: site, Cover: img}, m)
	if c, found, _ := WorkCover(p); err != nil || !found || !bytes.Equal(c.Data, siteFiles(t)[coverName]) {
		t.Fatalf("a site's own cover = %+v %v", c, err)
	}
	bare := siteFiles(t)
	delete(bare, coverName)
	raw, _ = tarbundle.PackFiles(bare, tarbundle.SiteLimits)
	bareSite, _ := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite}}, WorkSnapshot{Site: bareSite, Cover: img}, m)
	if c, found, _ := WorkCover(p); err != nil || !found || !bytes.Equal(c.Data, img) {
		t.Fatalf("a site without a cover took %+v %v", c, err)
	}
	p, _, err = WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindHTML}}, WorkSnapshot{Name: "game.html", Data: []byte("<!doctype html><title>g</title>"), Cover: img}, m)
	if c, found, _ := WorkCover(p); err != nil || !found || !bytes.Equal(c.Data, img) {
		t.Fatalf("a single HTML file's cover = %+v %v", c, err)
	}
	withManifest := siteFiles(t)
	withManifest[ManifestFile] = []byte("{}")
	raw, _ = tarbundle.PackFiles(withManifest, tarbundle.SiteLimits)
	site, _ = tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if _, _, err := WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite}}, WorkSnapshot{Site: site}, Manifest{Name: "x", Version: "1.0.0"}); err == nil || !strings.Contains(err.Error(), "of its own") {
		t.Fatalf("a site with its own manifest: %v", err)
	}
	if _, _, err := Build(Form{Kind: "work", Name: "x"}, "1.0.0", Options{}, nil); err == nil {
		t.Fatal("a form wrote a work")
	}
}

// A stored version is read against its row's manifest: a row a migration
// moved to the work kind reads although its bundle's component.json still
// names the old kind.
func TestValidateStoredReadsTheRowManifest(t *testing.T) {
	files := siteFiles(t)
	files[ManifestFile] = manifestJSON(t, map[string]any{"kind": "bnbloc.app.package/v1", "name": "team-poll", "version": "1.0.0"})
	raw, err := tarbundle.PackFiles(files, WorkLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tarbundle.Parse(raw, "", BundleLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBundle(b, Options{}); err == nil {
		t.Fatal("the old kind validated as itself")
	}
	row, _ := json.Marshal(map[string]any{"kind": KindWork, "name": "team-poll", "version": "1.0.0", "type": wire.WorkTypeApp})
	p, err := ValidateStored(b, row, Options{})
	if err != nil || p.Manifest.Kind != KindWork || p.Manifest.Type != wire.WorkTypeApp {
		t.Fatalf("the moved row = %+v %v", p, err)
	}
}

// A work is published from a deliverable: an upload of another format
// imports as none.
func TestImportWorkRefused(t *testing.T) {
	files := map[string][]byte{}
	for k, v := range siteFiles(t) {
		files["poll/"+k] = v
	}
	raw, err := tarbundle.PackFiles(files, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Import(raw, "work", "team-poll", "1.0.0", Options{}); err == nil || !strings.Contains(err.Error(), "does not create work") {
		t.Fatalf("an imported site folder = %v", err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A recipe rides the manifest; its reference files lie under references/,
// each under a name of its own; a site's copy leaves them out.
func TestWorkRecipe(t *testing.T) {
	recipe := &wire.WorkRecipe{
		Prompt: "Make a lunch poll for Friday",
		Expert: &wire.WorkRecipePart{ID: "cc_1", Name: "poll-maker", DisplayName: "Poll maker", Version: "1.2.0"},
		Skills: []wire.WorkRecipePart{{ID: "cc_2", Name: "interactive-apps", Version: "1.3.0"}},
		Agent:  &wire.WorkRecipeAgent{Core: "acme", Provider: "codex"},
	}
	refs := []WorkReferenceFile{
		{Ref: wire.WorkReference{AttachmentID: "att_1", Name: "brief.md", ContentType: "text/markdown"}, Data: []byte("# Brief")},
		{Ref: wire.WorkReference{AttachmentID: "att_2", Name: "../brief.md", ContentType: "text/markdown"}, Data: []byte("# Brief 2")},
	}
	raw, _ := tarbundle.PackFiles(map[string][]byte{"index.html": []byte("<p>poll</p>"), "references/notes.txt": []byte("the site's own")}, tarbundle.SiteLimits)
	site, _ := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	m := Manifest{Name: "lunch-poll", Version: "1.0.0", Recipe: recipe}
	p, bundle, err := WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindSite}}, WorkSnapshot{Site: site, References: refs}, m)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Manifest.Recipe
	if got == nil || got.Prompt != recipe.Prompt || got.Expert.Name != "poll-maker" || len(got.References) != 2 ||
		got.References[0].File != "references/brief.md" || got.References[1].File != "references/2-brief.md" ||
		got.References[0].AttachmentID != "" || got.References[1].SizeBytes != 9 {
		t.Fatalf("recipe = %+v", got)
	}
	if recipe.References != nil {
		t.Fatal("the caller's recipe was changed")
	}
	// It survives a stored read.
	if p, err = Validate(bundle, "", Options{}); err != nil || len(WorkReferences(p)) != 2 || string(WorkReferences(p)[1].Data) != "# Brief 2" {
		t.Fatalf("stored = %v", err)
	}
	copied := WorkSite(p)
	if _, ok := copied["references/brief.md"]; ok {
		t.Fatal("a site's copy holds the recipe's references")
	}
	if string(copied["references/notes.txt"]) != "the site's own" {
		t.Fatal("a site's copy lost its own references/ folder")
	}
	// An image work with references.
	img, _, err := WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindImage}}, WorkSnapshot{Name: "chart.png", Data: pngBytes(t, 4, 4), References: refs[:1]}, m)
	if err != nil || len(img.Manifest.Recipe.References) != 1 {
		t.Fatalf("an image work with a reference = %v", err)
	}
	// No recipe: the references go nowhere.
	none, _, err := WorkFromDeliverable(WorkDeliverable{Artifact: wire.Artifact{Kind: store.ArtifactKindImage}}, WorkSnapshot{Name: "chart.png", Data: pngBytes(t, 4, 4), References: refs}, Manifest{Name: "x", Version: "1.0.0"})
	if err != nil || none.Manifest.Recipe != nil || len(none.Bundle.Paths()) != 2 {
		t.Fatalf("no recipe = %v %v", err, none.Bundle.Paths())
	}
}

func TestWorkRecipeRefused(t *testing.T) {
	img := pngBytes(t, 4, 4)
	base := func() map[string]any {
		return map[string]any{"type": wire.WorkTypeImage, "file": "chart.png"}
	}
	ref := map[string]any{"file": "references/brief.md", "name": "brief.md", "contentType": "text/markdown", "sizeBytes": 7}
	for _, c := range []struct {
		name   string
		recipe map[string]any
		files  map[string][]byte
		want   string
	}{
		{"a long prompt", map[string]any{"prompt": strings.Repeat("x", wire.MaxChatTextBytes+1)}, nil, "recipe prompt"},
		{"a bad skill name", map[string]any{"skills": []any{map[string]any{"id": "cc_1", "name": "Bad Name"}}}, nil, "name"},
		{"a skill without an id", map[string]any{"skills": []any{map[string]any{"name": "review"}}}, nil, "an id"},
		{"a bad version", map[string]any{"expert": map[string]any{"id": "cc_1", "name": "x", "version": "one"}}, nil, "version"},
		{"an agent without a provider", map[string]any{"agent": map[string]any{"core": "acme"}}, nil, "a provider"},
		{"a missing reference", map[string]any{"references": []any{ref}}, nil, "not in the bundle"},
		{"a reference outside references/", map[string]any{"references": []any{map[string]any{"file": "brief.md", "name": "brief.md", "contentType": "text/markdown", "sizeBytes": 7}}},
			map[string][]byte{"brief.md": []byte("# Brief")}, "under references/"},
		{"a reference of another size", map[string]any{"references": []any{map[string]any{"file": "references/brief.md", "name": "brief.md", "contentType": "text/markdown", "sizeBytes": 3}}},
			map[string][]byte{"references/brief.md": []byte("# Brief")}, "not 3"},
		{"a stored attachment", map[string]any{"references": []any{map[string]any{"file": "references/brief.md", "attachmentId": "att_1", "name": "brief.md", "contentType": "text/markdown", "sizeBytes": 7}}},
			map[string][]byte{"references/brief.md": []byte("# Brief")}, "names no attachment"},
		{"an unlisted reference file", map[string]any{}, map[string][]byte{"references/brief.md": []byte("# Brief")}, "holds only its file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			extra := base()
			extra["recipe"] = c.recipe
			files := map[string][]byte{"chart.png": img}
			for k, v := range c.files {
				files[k] = v
			}
			if _, err := Validate(workBundle(t, files, extra), "", Options{}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	too := []any{}
	files := map[string][]byte{"chart.png": img}
	for i := 0; i <= wire.MaxWorkReferences; i++ {
		f := fmt.Sprintf("references/r%d.txt", i)
		files[f] = []byte("x")
		too = append(too, map[string]any{"file": f, "name": "r.txt", "contentType": "text/plain", "sizeBytes": 1})
	}
	extra := base()
	extra["recipe"] = map[string]any{"references": too}
	if _, err := Validate(workBundle(t, files, extra), "", Options{}); err == nil || !strings.Contains(err.Error(), "reference files") {
		t.Fatalf("too many references = %v", err)
	}
	extra = base()
	extra["recipe"] = map[string]any{"prompt": "fine", "references": []any{ref}}
	if _, err := Validate(workBundle(t, map[string][]byte{"chart.png": img, "references/brief.md": []byte("# Brief")}, extra), "", Options{}); err != nil {
		t.Fatalf("a valid recipe: %v", err)
	}
}
