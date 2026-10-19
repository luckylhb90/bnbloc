package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A work version's stored cover (docs/prd/2026-10-19-work-covers.md §2.3).

// putCover sends body as subject's cover of the work's version.
func (e *appGW) putCover(subject string, c wire.ContentComponent, source, contentType, body string) resp {
	e.t.Helper()
	return e.call(http.MethodPut, c.CoverURL+"?source="+source, body, e.as(subject), "Content-Type: "+contentType)
}

// stored reads a cover write's answer (200 expected).
func (e *appGW) stored(r resp) bool {
	e.t.Helper()
	var out wire.WorkCoverStored
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil {
		e.t.Fatalf("cover write = %d %s", r.status, r.body)
	}
	return out.Stored
}

// drawCover asks for subject's AI cover of the work's version.
func (e *appGW) drawCover(subject string, c wire.ContentComponent) resp {
	e.t.Helper()
	return e.call(http.MethodPost, c.CoverURL+"/ai", nil, e.as(subject))
}

// Any reader's render is stored once, for a version with no cover: a PNG,
// WebP or JPEG of the type its Content-Type says, within the cap. The
// organization's row and the author's own share it (the same bundle).
func TestRenderedCoverStoredOnceByAnyReader(t *testing.T) {
	e := galleryGW(t)
	doc := e.realFile(e.roomID, store.ArtifactKindMarkdown, "brief.md", "text/markdown", []byte("# Brief\n"), 0)
	w := e.publishApp("maya", doc, wire.WorkPublishRequest{Name: "brief"})
	if status, _ := e.coverOf("maya", w); status != http.StatusNotFound {
		t.Fatalf("no cover yet = %d", status)
	}
	png := coverPNG(t)
	// Someone who cannot read the work cannot give it a cover.
	if r := e.putCover("jack", w, store.ContentCoverRender, "image/png", png); r.status != http.StatusNotFound {
		t.Fatalf("a cover of a work jack cannot read = %d %s", r.status, r.body)
	}
	org := *e.publishToOrg(w)
	for _, c := range []struct {
		name, source, contentType, body string
		want                            int
	}{
		{"an AI source", store.ContentCoverAI, "image/png", png, http.StatusBadRequest},
		{"no source", "", "image/png", png, http.StatusBadRequest},
		{"an SVG", store.ContentCoverRender, "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`, http.StatusUnsupportedMediaType},
		{"text", store.ContentCoverRender, "image/png", "not an image", http.StatusUnsupportedMediaType},
		{"a PNG sent as a JPEG", store.ContentCoverRender, "image/jpeg", png, http.StatusUnsupportedMediaType},
		{"over the cap", store.ContentCoverRender, "image/png", string(posterPNG(t)), http.StatusRequestEntityTooLarge},
	} {
		if r := e.putCover("jack", org, c.source, c.contentType, c.body); r.status != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, r.status, r.body, c.want)
		}
	}
	if !e.stored(e.putCover("jack", org, store.ContentCoverRender, "image/png", png)) {
		t.Fatal("jack's render was not stored")
	}
	for _, who := range []string{"jack", "maya"} {
		for _, c := range []wire.ContentComponent{org, w} {
			if who == "jack" && c.ID == w.ID {
				continue
			}
			r := e.call(http.MethodGet, c.CoverURL, nil, e.as(who))
			if r.status != http.StatusOK || r.header.Get("Content-Type") != "image/png" || r.header.Get("X-Content-Type-Options") != "nosniff" || string(r.body) != png {
				t.Fatalf("%s's cover of %s = %d %s", who, c.ID, r.status, r.header)
			}
		}
	}
	// A second render, the author's own included, is not kept.
	if e.stored(e.putCover("nora", org, store.ContentCoverRender, "image/png", png)) || e.stored(e.putCover("maya", w, store.ContentCoverRender, "image/png", png)) {
		t.Fatal("a second render replaced the first")
	}
	// A version with a cover of its own keeps it.
	site := e.publishApp("maya", e.pollSite(), wire.WorkPublishRequest{Name: "lunch-poll"})
	if e.stored(e.putCover("maya", site, store.ContentCoverRender, "image/png", png)) {
		t.Fatal("a render over the bundle's own cover was stored")
	}
	if _, found, err := e.b.ContentComponents.GetContentVersionCover(e.ctx(), site.ID, site.Version); found || err != nil {
		t.Fatalf("the site's stored cover = %v %v", found, err)
	}
}

// The bundle's own cover is served before a stored one (whose blob this
// test never wrote: reading it would fail).
func TestWorkCoverServesTheBundleFirst(t *testing.T) {
	e := galleryGW(t)
	site := e.publishApp("maya", e.pollSite(), wire.WorkPublishRequest{Name: "lunch-poll"})
	if _, _, err := e.b.ContentComponents.SetContentVersionCover(e.ctx(), store.ContentVersionCover{ComponentID: site.ID, Version: site.Version, BlobKey: "sha256/" + strings.Repeat("0", 64), MimeType: "image/jpeg", Rank: store.ContentCoverRank(store.ContentCoverAI), CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	r := e.call(http.MethodGet, site.CoverURL, nil, e.as("maya"))
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "image/png" || string(r.body) != coverPNG(t) {
		t.Fatalf("the cover = %d %s", r.status, r.header)
	}
}

// The author's AI cover is drawn from the work's title, description and
// type and replaces a render; only the author may ask, once; without an
// image provider, or for a work with a cover of its own, there is none to
// draw. The route spends the cost budget.
func TestAuthorsAICoverStored(t *testing.T) {
	fake := &mediaProviderFake{imageB64: base64.StdEncoding.EncodeToString(posterPNG(t))}
	e := mediaGW(t, fake)
	e.gw.ContentComponents = e.b.ContentComponents
	e.gw.TenantSettings = e.b.Tenant.GetTenant
	game := e.realFile(e.roomID, store.ArtifactKindHTML, "merge.html", "text/html", []byte("<!doctype html><title>merge</title>"), 0)
	w := e.publishApp("maya", game, wire.WorkPublishRequest{Name: "crypto-merge", DisplayName: "Crypto Merge", Description: "merge coins into bigger coins"})
	if status, _ := e.coverOf("maya", w); status != http.StatusNotFound {
		t.Fatalf("no cover yet = %d", status)
	}
	if !e.stored(e.putCover("maya", w, store.ContentCoverRender, "image/png", coverPNG(t))) {
		t.Fatal("the render was not stored")
	}
	if r := e.drawCover("jack", w); r.status != http.StatusNotFound {
		t.Fatalf("someone else's AI cover = %d %s", r.status, r.body)
	}
	if paths := len(fake.paths); paths != 0 {
		t.Fatalf("the provider was called %d times for a refused cover", paths)
	}
	if !e.stored(e.drawCover("maya", w)) {
		t.Fatal("the AI cover was not stored")
	}
	if p := fake.last["prompt"]; !strings.Contains(p, `"Crypto Merge"`) || !strings.Contains(p, "merge coins") || !strings.Contains(p, "a web page") {
		t.Fatalf("prompt = %q", p)
	}
	if status, typ := e.coverOf("maya", w); status != http.StatusOK || typ != "image/jpeg" {
		t.Fatalf("the AI cover = %d %s (it replaces the render)", status, typ)
	}
	if r := e.drawCover("maya", w); r.status != http.StatusConflict {
		t.Fatalf("a second AI cover = %d %s", r.status, r.body)
	}
	if e.stored(e.putCover("maya", w, store.ContentCoverRender, "image/png", coverPNG(t))) {
		t.Fatal("a render replaced the AI cover")
	}

	g := galleryGW(t)
	doc := g.realFile(g.roomID, store.ArtifactKindMarkdown, "a.md", "text/markdown", []byte("# a"), 0)
	if r := g.drawCover("maya", g.publishApp("maya", doc, wire.WorkPublishRequest{Name: "a"})); r.status != http.StatusConflict {
		t.Fatalf("no image provider = %d %s", r.status, r.body)
	}
	if r := g.drawCover("maya", g.publishApp("maya", g.pollSite(), wire.WorkPublishRequest{Name: "lunch-poll"})); r.status != http.StatusConflict {
		t.Fatalf("a work with a cover of its own = %d %s", r.status, r.body)
	}

	for _, rt := range e.gw.routes() {
		switch rt.pattern {
		case "POST /v1/content-components/{id}/versions/{version}/cover/ai":
			if rt.budget != costBudget || rt.perm != rbac.PermDriveAgent {
				t.Fatalf("the AI cover route = budget %v, perm %v", rt.budget, rt.perm)
			}
		case "PUT /v1/content-components/{id}/versions/{version}/cover":
			if rt.budget != noBudget {
				t.Fatalf("a render spends budget %v", rt.budget)
			}
		}
	}
}
