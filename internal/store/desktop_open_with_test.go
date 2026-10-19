package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The desktop app is offered in the OS's "Open With" only for formats the
// room knows (docs/prd/2026-10-14-ai-document-editing.md §4: the formats a
// viewer claims): every extension of its bundle's fileAssociations types
// as a room file by name (ArtifactMIME), never as an unknown binary.
func TestDesktopOpenWithClaimsOnlyRoomFormats(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "apps", "desktop", "src-tauri", "tauri.conf.json"))
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Bundle struct {
			FileAssociations []struct {
				Ext          []string `json:"ext"`
				Rank         string   `json:"rank"`
				ContentTypes []string `json:"contentTypes"`
			} `json:"fileAssociations"`
		} `json:"bundle"`
	}
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range conf.Bundle.FileAssociations {
		// Offered, never made the default app.
		if a.Rank != "Alternate" {
			t.Errorf("%v: rank %q, want Alternate", a.Ext, a.Rank)
		}
		for _, ext := range a.Ext {
			if seen[ext] {
				t.Errorf("%s is claimed twice", ext)
			}
			seen[ext] = true
			if m := ArtifactMIME("file."+ext, nil); m == "application/octet-stream" {
				t.Errorf("Open With claims .%s, which the room does not know", ext)
			}
		}
	}
	if len(seen) < 60 {
		t.Fatalf("only %d extensions claimed", len(seen))
	}
}
