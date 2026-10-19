package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// posterPNG is an image model's size of poster that compresses badly: over
// the cover cap.
func posterPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1536, 1024))
	rnd := rand.New(rand.NewSource(2))
	for i := range img.Pix {
		img.Pix[i] = uint8(rnd.Intn(256))
		if i%4 == 3 {
			img.Pix[i] = 0xff
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if b.Len() <= wire.MaxWorkCoverBytes {
		t.Fatalf("the poster is only %d bytes", b.Len())
	}
	return b.Bytes()
}

// coverOf reads a work's cover as subject: its type, or 0 status.
func (e *appGW) coverOf(subject string, c wire.ContentComponent) (int, string) {
	e.t.Helper()
	r := e.call(http.MethodGet, c.CoverURL, nil, e.as(subject))
	return r.status, r.header.Get("Content-Type")
}

// An image over the cover cap is its work's cover, scaled into a JPEG
// (QA 2026-10-19: a poster fell back to the generated cover).
func TestImageWorkOverTheCapGetsAFittedCover(t *testing.T) {
	e := galleryGW(t)
	poster := e.realFile(e.roomID, store.ArtifactKindImage, "poster.png", "image/png", posterPNG(t), 0)
	w := e.publishApp("maya", poster, wire.WorkPublishRequest{Name: "poster"})
	if status, typ := e.coverOf("maya", w); status != http.StatusOK || typ != "image/jpeg" {
		t.Fatalf("the poster's cover = %d %s", status, typ)
	}
	if files := e.versionFiles("maya", w.ID, "1.0.0"); !contains(files, "cover.jpg") {
		t.Fatalf("files = %v", files)
	}
}

// The cover the person chose is the work's (a single HTML file can carry
// one); one that is no cover image is refused.
func TestChosenCoverAndSingleHTMLFile(t *testing.T) {
	e := galleryGW(t)
	game := e.realFile(e.roomID, store.ArtifactKindHTML, "game.html", "text/html", []byte("<!doctype html><title>merge</title><canvas></canvas>"), 0)
	chosen := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(coverPNG(t)))
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+game+"/publish-work", wire.WorkPublishRequest{Name: "merge", Cover: "data:image/svg+xml;base64,PHN2Zy8+"}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("an SVG cover = %d %s", r.status, r.body)
	}
	w := e.publishApp("maya", game, wire.WorkPublishRequest{Name: "merge", Cover: chosen})
	if status, typ := e.coverOf("maya", w); status != http.StatusOK || typ != "image/png" {
		t.Fatalf("the game's cover = %d %s", status, typ)
	}
}

// A document without a cover of its own takes the room's thumbnail of its
// version (a viewer's first page); without one it has none.
func TestDocumentTakesTheRoomThumbnail(t *testing.T) {
	e := galleryGW(t)
	deck := e.realFile(e.roomID, store.ArtifactKindMarkdown, "deck.md", "text/markdown", []byte("# Brand deck\n"), 0)
	plain := e.publishApp("maya", deck, wire.WorkPublishRequest{Name: "deck"})
	if status, _ := e.coverOf("maya", plain); status != http.StatusNotFound {
		t.Fatalf("no thumbnail yet: cover = %d", status)
	}
	key, _, _, err := e.blobs.Put(context.Background(), bytes.NewReader([]byte(coverPNG(t))))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.b.Artifacts.SetArtifactThumbnail(e.ctx(), deck, 1, store.ArtifactThumbnailWrite{BlobKey: key, MimeType: "image/png", Width: 32, Height: 20, Rank: store.ArtifactThumbnailRank(store.ArtifactThumbnailBuiltin)}); err != nil {
		t.Fatal(err)
	}
	again := e.publishApp("maya", deck, wire.WorkPublishRequest{ComponentID: plain.ID})
	if status, typ := e.coverOf("maya", again); status != http.StatusOK || typ != "image/png" {
		t.Fatalf("the deck's cover = %d %s", status, typ)
	}
}

// An AI cover is drawn from the title and type the dialog has, fitted to
// the cover cap, and is the work's cover once published with it. Without
// an image provider there is none to draw.
func TestAICoverIsDrawnFromTheTitle(t *testing.T) {
	fake := &mediaProviderFake{imageB64: base64.StdEncoding.EncodeToString(posterPNG(t))}
	e := mediaGW(t, fake)
	e.gw.ContentComponents = e.b.ContentComponents
	e.gw.TenantSettings = e.b.Tenant.GetTenant
	game := e.realFile(e.roomID, store.ArtifactKindHTML, "merge.html", "text/html", []byte("<!doctype html><title>merge</title>"), 0)
	r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+game+"/work-cover", wire.WorkCoverRequest{DisplayName: "Crypto Merge", Description: "merge coins into bigger coins"}, e.as("maya"))
	var drawn wire.WorkCoverReply
	if r.status != http.StatusOK || json.Unmarshal(r.body, &drawn) != nil || !strings.HasPrefix(drawn.Cover, "data:image/jpeg;base64,") {
		t.Fatalf("work-cover = %d %.200s", r.status, r.body)
	}
	if p := fake.last["prompt"]; !strings.Contains(p, `"Crypto Merge"`) || !strings.Contains(p, "a web page") || !strings.Contains(p, "No text") {
		t.Fatalf("prompt = %q", p)
	}
	w := e.publishApp("maya", game, wire.WorkPublishRequest{Name: "crypto-merge", Cover: drawn.Cover})
	if status, typ := e.coverOf("maya", w); status != http.StatusOK || typ != "image/jpeg" {
		t.Fatalf("the published cover = %d %s", status, typ)
	}

	g := galleryGW(t)
	doc := g.realFile(g.roomID, store.ArtifactKindMarkdown, "a.md", "text/markdown", []byte("# a"), 0)
	if r := g.call(http.MethodPost, "/v1/rooms/"+g.roomID+"/artifacts/"+doc+"/work-cover", wire.WorkCoverRequest{}, g.as("maya")); r.status != http.StatusConflict {
		t.Fatalf("no image provider = %d %s", r.status, r.body)
	}
}
