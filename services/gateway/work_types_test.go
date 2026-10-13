package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// realFile is a file deliverable of room (kind, file name, media type)
// whose version 1 holds data in the blob store, published by maya (by the
// turn of request seq, when not 0).
func (e *appGW) realFile(room, kind, fileName, mimeType string, data []byte, seq int64) string {
	e.t.Helper()
	key, size, sha, err := e.blobs.Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		e.t.Fatal(err)
	}
	a, err := e.b.Artifacts.PutArtifact(e.ctx(), store.Artifact{ArtifactID: ids.New("art_"), RoomID: room, Kind: kind, Title: fileName, FileName: fileName, CreatedBy: "maya"})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), a.ArtifactID, store.ArtifactVersion{Storage: store.ArtifactStorageBlob, BlobKey: key,
		ContentHash: sha, MimeType: mimeType, SizeBytes: size, AuthorUserID: "maya", CreatedAtMs: 1, SourceSeq: seq}, 0); err != nil {
		e.t.Fatal(err)
	}
	return a.ArtifactID
}

// versionFiles lists a work version's files as subject reads them.
func (e *appGW) versionFiles(subject, id, version string) []string {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/content-components/"+id+"/versions/"+version, nil, e.as(subject))
	var v wire.ContentVersionDetail
	if r.status != http.StatusOK || json.Unmarshal(r.body, &v) != nil {
		e.t.Fatalf("version = %d %s", r.status, r.body)
	}
	var files []string
	for _, f := range v.Files {
		files = append(files, f.Path)
	}
	sort.Strings(files)
	return files
}

// Every kind of deliverable publishes as a work, its type derived from it:
// an image (its own cover), a document, a video with the frame an agent
// saved as its cover. Use this app and Try demo stay an app's.
func TestPublishWorkEveryType(t *testing.T) {
	e := galleryGW(t)
	png := coverPNG(t)
	chart := e.realFile(e.roomID, store.ArtifactKindImage, "chart.png", "image/png", []byte(png), 0)
	img := e.publishApp("maya", chart, wire.WorkPublishRequest{Name: "sales-chart"})
	if img.Kind != "work" || img.WorkType != wire.WorkTypeImage || img.WorkFile != "chart.png" || img.DisplayName != "chart.png" {
		t.Fatalf("the image work = %+v", img)
	}
	if got := strings.Join(e.versionFiles("maya", img.ID, "1.0.0"), ","); got != "chart.png,component.json" {
		t.Fatalf("the image work's files = %s", got)
	}
	// Its cover is the image itself.
	if r := e.call(http.MethodGet, img.CoverURL, nil, e.as("maya")); r.status != http.StatusOK || r.header.Get("Content-Type") != "image/png" || string(r.body) != png {
		t.Fatalf("the image work's cover = %d %v", r.status, r.header)
	}

	doc := e.realFile(e.roomID, store.ArtifactKindMarkdown, "plan.md", "text/markdown", []byte("# Plan\n"), 0)
	if w := e.publishApp("maya", doc, wire.WorkPublishRequest{Name: "plan", Category: wire.WorkCategoryOffice}); w.WorkType != wire.WorkTypeDocument || w.WorkFile != "plan.md" {
		t.Fatalf("the document work = %+v", w)
	}
	// A document has no cover: the card draws the generated one.
	if r := e.call(http.MethodGet, "/v1/content-components/"+e.libraryApp("maya", "plan").ID+"/versions/1.0.0/cover", nil, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("a document's cover = %d", r.status)
	}

	// A video: the frame its turn saved as a cover file is its cover; a
	// frame of another turn, or an image named otherwise, is not.
	mp4 := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00")
	frameName := "frames/cover" + ".png"
	e.realFile(e.roomID, store.ArtifactKindImage, "other.png", "image/png", []byte(png), 7)
	e.realFile(e.roomID, store.ArtifactKindImage, frameName, "image/png", []byte(png), 8)
	plain := e.publishApp("maya", e.realFile(e.roomID, store.ArtifactKindFile, "plain.mp4", "video/mp4", mp4, 7), wire.WorkPublishRequest{Name: "plain"})
	if r := e.call(http.MethodGet, plain.CoverURL, nil, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("a video without a frame's cover = %d", r.status)
	}
	clip := e.realFile(e.roomID, store.ArtifactKindFile, "clip.mp4", "video/mp4", mp4, 8)
	video := e.publishApp("maya", clip, wire.WorkPublishRequest{Name: "clip"})
	if video.WorkType != wire.WorkTypeVideo || video.WorkFile != "clip.mp4" {
		t.Fatalf("the video work = %+v", video)
	}
	if got := strings.Join(e.versionFiles("maya", video.ID, "1.0.0"), ","); got != "clip.mp4,component.json,"+strings.TrimPrefix(frameName, "frames/") {
		t.Fatalf("the video work's files = %s", got)
	}
	if r := e.call(http.MethodGet, video.CoverURL, nil, e.as("maya")); r.status != http.StatusOK || string(r.body) != png {
		t.Fatalf("the video's frame cover = %d", r.status)
	}

	// Only an app is used as a copy or tried as a demo.
	if r := e.call(http.MethodPost, "/v1/content-components/"+img.ID+"/use-app", wire.AppUseRequest{RoomID: e.roomID}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("use-app of an image = %d %s", r.status, r.body)
	}
	if r := e.demo("maya", img.ID, "1.0.0"); r.status != http.StatusNotFound {
		t.Fatalf("a demo of an image = %d %s", r.status, r.body)
	}
	// The gallery lists every type.
	for _, name := range []string{"sales-chart", "plan", "clip"} {
		if e.libraryApp("maya", name) == nil {
			t.Errorf("maya's gallery has no %s", name)
		}
	}
}
