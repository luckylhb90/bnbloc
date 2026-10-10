package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

const (
	siteToken = "bsh_shsite_c2l0ZXNlY3JldA"
	docToken  = "bsh_shdoc_ZG9jc2VjcmV0"
	pdfToken  = "bsh_shpdf_cGRmc2VjcmV0"
)

func shareRes(shareVersion int, kind, title, fileName string, v store.ArtifactVersion) map[string]any {
	return map[string]any{
		"tenantId": "default", "roomId": "room-1",
		"share": map[string]any{"shareId": "sh1", "artifactId": v.ArtifactID, "version": shareVersion, "createdBy": "alice",
			"createdAtMs": 1, "expiresAtMs": 1900000000000, "viewCount": 0, "active": true},
		"artifact": map[string]any{"artifactId": v.ArtifactID, "kind": kind, "title": title, "fileName": fileName},
		"version":  v,
		"sharedBy": map[string]any{"userId": "alice", "displayName": "Alice A."},
	}
}

func newShareEnv(t *testing.T) *siteEnv {
	t.Helper()
	e := newSiteEnv(t)
	site := e.putSite("room-1", "art_site", testSiteFiles)
	key, size, sha, err := e.blobs.Put(context.Background(), bytes.NewReader([]byte("%PDF-1.7 fake")))
	if err != nil {
		t.Fatal(err)
	}
	pdf := store.ArtifactVersion{ArtifactID: "art_pdf", Version: 2, Storage: store.ArtifactStorageBlob, BlobKey: key, ContentHash: sha, SizeBytes: size, MimeType: "application/pdf", CreatedAtMs: 5}
	doc := store.ArtifactVersion{ArtifactID: "art_doc", Version: 3, Storage: store.ArtifactStorageInline, Content: "# Plan\n", SizeBytes: 7, MimeType: "text/markdown", Summary: "first cut", CreatedAtMs: 7}
	e.shares[siteToken] = shareRes(0, store.ArtifactKindSite, "Demo", "dist", site)
	e.shares[pdfToken] = shareRes(2, store.ArtifactKindFile, "Q3", "Q3 report.pdf", pdf)
	e.shares[docToken] = shareRes(3, store.ArtifactKindMarkdown, "Plan", "plan.md", doc)
	return e
}

func TestPublicShare_metadata_reveals_only_the_shared_version(t *testing.T) {
	e := newShareEnv(t)

	resp, body := e.do(http.MethodGet, "/v1/public/shares/"+docToken, "")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d cache %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := `{"artifact":{"fileName":"plan.md","kind":"markdown","title":"Plan"},"expiresAtMs":1900000000000,"followsLatest":false,"sharedBy":{"displayName":"Alice A."},"version":{"createdAtMs":7,"mimeType":"text/markdown","sizeBytes":7,"summary":"first cut","version":3}}`
	if canon, _ := json.Marshal(got); string(canon) != want {
		t.Fatalf("metadata\n got %s\nwant %s", canon, want)
	}
	if e.views[docToken] != 1 || e.resolveUser != svcauth.ServiceSubject("gateway") {
		t.Fatalf("views %d resolver user %q", e.views[docToken], e.resolveUser)
	}

	resp, body = e.do(http.MethodGet, "/v1/public/shares/"+siteToken, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"siteUrl":"/v1/public/shares/`+siteToken+`/site/"`) || !strings.Contains(body, `"followsLatest":true`) {
		t.Fatalf("site metadata %d %s", resp.StatusCode, body)
	}
}

func TestPublicShare_content_is_sandboxed_and_does_not_count_views(t *testing.T) {
	e := newShareEnv(t)

	resp, body := e.do(http.MethodGet, "/v1/public/shares/"+docToken+"/content", "")
	if resp.StatusCode != http.StatusOK || body != "# Plan\n" {
		t.Fatalf("inline content %d %q", resp.StatusCode, body)
	}
	for k, v := range map[string]string{
		"Content-Type":            "text/markdown; charset=utf-8",
		"Content-Disposition":     `inline; filename=plan.md`,
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox",
		"Content-Length":          "7",
	} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	resp, body = e.do(http.MethodGet, "/v1/public/shares/"+pdfToken+"/content?download=1", "")
	if resp.StatusCode != http.StatusOK || body != "%PDF-1.7 fake" {
		t.Fatalf("blob download (direct off) %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename="Q3 report.pdf"` {
		t.Fatalf("disposition %q", got)
	}
	if e.views[pdfToken]+e.views[docToken] != 0 {
		t.Fatal("content reads must not count views")
	}
}

func TestPublicShare_download_redirects_to_a_presigned_url_when_direct(t *testing.T) {
	e := newShareEnv(t)
	e.rt.Blobs = presigningDisk{e.blobs}
	e.rt.DirectDownloads = true

	resp, _ := e.do(http.MethodGet, "/v1/public/shares/"+pdfToken+"/content?download=1", "")
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "https://s3.example.com/bucket/sha256/") ||
		!strings.Contains(loc, "Q3+report.pdf") || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("status %d location %q", resp.StatusCode, loc)
	}
	// A viewer's plain fetch (no ?download=1) never gets a cross-origin redirect.
	if resp, _ = e.do(http.MethodGet, "/v1/public/shares/"+pdfToken+"/content", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer fetch %d", resp.StatusCode)
	}
}

func TestPublicShare_site_serves_files_under_the_share_base(t *testing.T) {
	e := newShareEnv(t)
	base := "/v1/public/shares/" + siteToken + "/site/"

	resp, body := e.do(http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `src="`+base+`assets/index-AbC123.js"`) {
		t.Fatalf("site index %d %s", resp.StatusCode, body)
	}
	assertSiteHeaders(t, resp, "text/html; charset=utf-8")
	if resp, _ = e.do(http.MethodGet, strings.TrimSuffix(base, "/"), ""); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != base {
		t.Fatalf("base redirect %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ = e.do(http.MethodGet, "/v1/public/shares/"+docToken+"/site/", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-site share site route %d", resp.StatusCode)
	}
}

func TestPublicShare_misses_spend_the_guess_budget(t *testing.T) {
	e := newShareEnv(t)
	for i := 0; i < int(defaultGuessBurst); i++ {
		if resp, _ := e.do(http.MethodGet, "/v1/public/shares/bsh_nope_x"+string(rune('a'+i)), ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("miss %d status %d", i, resp.StatusCode)
		}
	}
	// The IP's guess budget is spent: even a valid link is refused for now.
	if resp, _ := e.do(http.MethodGet, "/v1/public/shares/"+docToken, ""); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after misses status %d", resp.StatusCode)
	}
}

func TestPublicShare_reads_are_rate_limited_per_ip(t *testing.T) {
	e := newShareEnv(t)
	limited := false
	for i := 0; i < int(defaultPublicBurst)+5; i++ {
		resp, _ := e.do(http.MethodGet, "/v1/public/shares/"+docToken+"/content", "")
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("public reads were never rate limited")
	}
	// Hits never spend the guess budget.
	if e.svc.guessBlocked("share-ip:127.0.0.1") {
		t.Fatal("valid reads spent the guess budget")
	}
}

func TestPublicShare_garbage_tokens_never_reach_the_room_service(t *testing.T) {
	e := newShareEnv(t)
	for _, tok := range []string{"nope", "bsh_" + strings.Repeat("a", 300), "bsh_a%2Fb"} {
		if resp, _ := e.do(http.MethodGet, "/v1/public/shares/"+tok, ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%q status %d", tok, resp.StatusCode)
		}
	}
	if e.resolveUser != "" {
		t.Fatal("an implausible token was forwarded for resolution")
	}
}
