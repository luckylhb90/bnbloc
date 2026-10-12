package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The built-in Expert builder is in every member's Library through the
// ordinary routes: listed, readable, its bundle downloadable, pinnable in a
// room; it is never anyone's to change or delete.
func TestBuiltinBuilderInLibrary(t *testing.T) {
	g := newContentGateway(t)
	g.svc.ContentComponents = contentbuiltin.Overlay(g.svc.ContentComponents)
	builder, _ := contentbuiltin.Get(contentbuiltin.BuilderID)

	for _, user := range []string{"maya", "bob", "vic"} {
		code, raw := g.do(user, http.MethodGet, "/v1/content-components?kind=expert", "", nil)
		var list struct {
			Components []wire.ContentComponent `json:"components"`
		}
		_ = json.Unmarshal(raw, &list)
		var found *wire.ContentComponent
		for i := range list.Components {
			if list.Components[i].ID == contentbuiltin.BuilderID {
				found = &list.Components[i]
			}
		}
		if code != http.StatusOK || found == nil || found.Scope != "builtin" || found.Mine || !found.Acked || found.Kind != "expert" {
			t.Fatalf("%s library = %d %s", user, code, raw)
		}
	}
	if _, raw := g.do("maya", http.MethodGet, "/v1/content-components?kind=skill", "", nil); bytes.Contains(raw, []byte(contentbuiltin.BuilderID)) {
		t.Fatalf("skill listing shows the builder: %s", raw)
	}

	code, raw := g.do("bob", http.MethodGet, "/v1/content-components/"+contentbuiltin.BuilderID, "", nil)
	d := decodeDetail(t, raw)
	if code != http.StatusOK || len(d.Versions) != 1 || d.Versions[0].BundleKey != builder.Version.BundleBlobKey || d.Versions[0].CreatedBy != contentbuiltin.Author {
		t.Fatalf("detail = %d %s", code, raw)
	}
	code, raw = g.do("bob", http.MethodGet, "/v1/content-components/"+contentbuiltin.BuilderID+"/versions/"+builder.Version.Version, "", nil)
	var v wire.ContentVersionDetail
	_ = json.Unmarshal(raw, &v)
	if code != http.StatusOK || !strings.Contains(v.Instructions, "save_component") {
		t.Fatalf("version = %d %.200s", code, raw)
	}
	// The bundle is served from the binary (the blob store never holds it).
	code, raw = g.do("bob", http.MethodGet, d.Versions[0].BundleURL, "", nil)
	if code != http.StatusOK || !bytes.Equal(raw, builder.Bundle) {
		t.Fatalf("bundle download = %d (%d bytes)", code, len(raw))
	}

	// Read-only for everyone.
	if code, _ := g.do("maya", http.MethodDelete, "/v1/content-components/"+contentbuiltin.BuilderID, "", nil); code != http.StatusNotFound {
		t.Fatalf("delete builtin = %d", code)
	}
	if code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+contentbuiltin.BuilderID+"/versions", expertForm("mine now")); code != http.StatusNotFound {
		t.Fatalf("new version of builtin = %d", code)
	}

	// The room owner pins it (the workshop room does); members see the pin.
	if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", map[string]any{"scope": "room", "roomId": "room-1", "componentId": contentbuiltin.BuilderID}); code != http.StatusNoContent {
		t.Fatalf("pin builtin = %d %s", code, raw)
	}
	code, raw = g.do("jack", http.MethodGet, "/v1/content-uses?room=room-1", "", nil)
	var uses wire.ContentUsesReply
	_ = json.Unmarshal(raw, &uses)
	if code != http.StatusOK || len(uses.Room) != 1 || uses.Room[0].Component == nil || uses.Room[0].Component.Scope != "builtin" {
		t.Fatalf("room uses = %d %s", code, raw)
	}
}
