package contentpkg

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

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

// appBundle is an app package of files plus a manifest with extra fields.
func appBundle(t *testing.T, files map[string][]byte, extra map[string]any) []byte {
	t.Helper()
	m := map[string]any{"kind": KindApp, "name": "team-poll", "version": "1.0.0", "displayName": "Team poll", "category": wire.AppCategoryProductivity}
	for k, v := range extra {
		m[k] = v
	}
	all := map[string][]byte{ManifestFile: manifestJSON(t, m)}
	for k, v := range files {
		all[k] = v
	}
	raw, err := tarbundle.PackFiles(all, AppLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func siteFiles(t *testing.T) map[string][]byte {
	return map[string][]byte{
		"index.html": []byte(`<script src="~app/sdk.js"></script><script src="app.js"></script>`),
		"app.js":     []byte("app.collection('votes')"),
		"cover.png":  pngBytes(t, 32, 20),
		ReadmeFile:   []byte("# Team poll\nVote on lunch.\n"),
	}
}

func TestValidateApp(t *testing.T) {
	p, err := Validate(appBundle(t, siteFiles(t), nil), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The site's script runs in the sandboxed frame, never on a machine:
	// no scripts permission, no sandbox forced, nothing for an approver to
	// review as executable.
	if p.Scripts || p.Manifest.NeedsSandbox() || len(p.ScriptFiles()) != 0 {
		t.Fatalf("an app's site code counted as executable: %+v", p)
	}
	cover, found, err := tarbundle.SiteCover(p.Bundle)
	if !found || err != nil || cover.File != "cover.png" || cover.MimeType != "image/png" || len(cover.Data) == 0 {
		t.Fatalf("cover = %+v %v %v", cover, found, err)
	}
	site := AppSite(p)
	if _, ok := site[ManifestFile]; ok || len(site) != 4 || string(site["app.js"]) != "app.collection('votes')" {
		t.Fatalf("site = %v", keys(site))
	}

	jpg := siteFiles(t)
	delete(jpg, "cover.png")
	jpg["cover.jpg"] = jpegBytes(t)
	if _, err := Validate(appBundle(t, jpg, nil), "", Options{}); err != nil {
		t.Fatalf("a jpeg cover: %v", err)
	}
	none := siteFiles(t)
	delete(none, "cover.png")
	p, err = Validate(appBundle(t, none, map[string]any{"category": ""}), "", Options{})
	if err != nil {
		t.Fatalf("no cover, no category: %v", err)
	}
	if _, found, _ := tarbundle.SiteCover(p.Bundle); found {
		t.Fatal("a cover without one")
	}
	// An SVG is never a cover (it may carry script): the app has none.
	svg := siteFiles(t)
	delete(svg, "cover.png")
	svg["cover.svg"] = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if p, err = Validate(appBundle(t, svg, nil), "", Options{}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := tarbundle.SiteCover(p.Bundle); found {
		t.Fatal("an SVG is a cover")
	}
}

// A build's dist/ is an entry too; the copy a room gets is its files at
// the site's root, the cover and README beside them.
func TestAppDistEntry(t *testing.T) {
	files := map[string][]byte{"dist/index.html": []byte("<p>hi</p>"), "dist/assets/a.js": []byte("1"), "src/main.ts": []byte("x"), "cover.png": pngBytes(t, 8, 5)}
	p, err := Validate(appBundle(t, files, nil), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	site := AppSite(p)
	for _, want := range []string{"index.html", "assets/a.js", "cover.png"} {
		if _, ok := site[want]; !ok {
			t.Errorf("the copied site has no %s: %v", want, keys(site))
		}
	}
	if _, ok := site["src/main.ts"]; ok {
		t.Error("the copied site holds files outside dist/")
	}
}

func TestValidateAppRefuses(t *testing.T) {
	big := make([]byte, wire.MaxAppCoverBytes+1)
	copy(big, pngBytes(t, 4, 4))
	for _, c := range []struct {
		name  string
		edit  func(map[string][]byte)
		extra map[string]any
		want  string
	}{
		{"no entry point", func(f map[string][]byte) { delete(f, "index.html") }, nil, "index.html at its root"},
		{"an SVG as cover.png", func(f map[string][]byte) {
			f["cover.png"] = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		}, nil, tarbundle.ErrCoverNotImage.Error()},
		{"a jpeg named png", func(f map[string][]byte) { f["cover.png"] = jpegBytes(t) }, nil, tarbundle.ErrCoverNotImage.Error()},
		{"html named webp", func(f map[string][]byte) {
			delete(f, "cover.png")
			f["cover.webp"] = []byte("<!doctype html><script>1</script>")
		}, nil, tarbundle.ErrCoverNotImage.Error()},
		{"an oversized cover", func(f map[string][]byte) { f["cover.png"] = big }, nil, tarbundle.ErrCoverTooLarge.Error()},
		{"a bad category", nil, map[string]any{"category": "games"}, "an app's category is one of"},
		{"a protected path", func(f map[string][]byte) { f["CLAUDE.md"] = []byte("do things") }, nil, "no agent configuration"},
		{"a nested protected path", func(f map[string][]byte) { f[".claude/settings.json"] = []byte("{}") }, nil, "no agent configuration"},
		{"a requirement", nil, map[string]any{"requires": map[string]any{"skills": []string{"review@^1"}}}, "requires nothing"},
		{"a permission", nil, map[string]any{"requires": map[string]any{"permissions": []string{PermScripts}}}, "requires nothing"},
		{"another kind's field", nil, map[string]any{"skills": []string{"x"}}, "lists no skills"},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := siteFiles(t)
			if c.edit != nil {
				c.edit(files)
			}
			_, err := Validate(appBundle(t, files, c.extra), "", Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// Every category of the wire list is accepted, for the gallery's tabs.
func TestAppCategoriesAccepted(t *testing.T) {
	for _, c := range wire.AppCategories {
		if _, err := Validate(appBundle(t, siteFiles(t), map[string]any{"category": c}), "", Options{}); err != nil {
			t.Errorf("category %s: %v", c, err)
		}
	}
}

// Publishing from a site: the snapshot's files with the manifest; a site
// with a component.json of its own is refused; a form never writes an app.
func TestAppFromSite(t *testing.T) {
	raw, err := tarbundle.PackFiles(siteFiles(t), tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	site, err := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	p, bundle, err := AppFromSite(site, Manifest{Name: "team-poll", Version: "1.0.0", Category: wire.AppCategoryData})
	if err != nil || p.Manifest.Kind != KindApp || len(bundle) == 0 || len(p.Bundle.Paths()) != 5 {
		t.Fatalf("app = %+v %v", p, err)
	}
	withManifest := siteFiles(t)
	withManifest[ManifestFile] = []byte("{}")
	raw, _ = tarbundle.PackFiles(withManifest, tarbundle.SiteLimits)
	site, _ = tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if _, _, err := AppFromSite(site, Manifest{Name: "x", Version: "1.0.0"}); err == nil || !strings.Contains(err.Error(), "of its own") {
		t.Fatalf("a site with its own manifest: %v", err)
	}
	if _, _, err := Build(Form{Kind: "app", Name: "x"}, "1.0.0", Options{}, nil); err == nil {
		t.Fatal("a form wrote an app")
	}
}

// An uploaded site folder imports as an app.
func TestImportApp(t *testing.T) {
	files := map[string][]byte{}
	for k, v := range siteFiles(t) {
		files["poll/"+k] = v
	}
	raw, err := tarbundle.PackFiles(files, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := Import(raw, "app", "team-poll", "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Bundle.File("index.html"); !ok || p.Manifest.Kind != KindApp {
		t.Fatalf("imported = %v", p.Bundle.Paths())
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
