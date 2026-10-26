package pkgmeta

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func validPreviewerPackage() map[string]any {
	return map[string]any{
		"kind":      KindPreviewerPackageV1,
		"contract":  PreviewerContractV1,
		"runtime":   "bundle",
		"name":      "sprite-sheet",
		"version":   "1.0.0",
		"publisher": "Example Co",
		"artifacts": map[string]any{"bundle": map[string]string{"url": "https://gw.example.com/v1/artifacts/sha256/" + strings.Repeat("a", 64), "sha256": strings.Repeat("a", 64)}},
		"previewer": map[string]any{
			"entry":        "dist/index.html",
			"match":        map[string]any{"mimeTypes": []string{"image/png", "image/*"}, "extensions": []string{".png", ".atlas.json"}},
			"rank":         50,
			"capabilities": []string{"thumbnail", "liveState", "toolbar"},
			"toolbar": []map[string]string{
				{"id": "zoom", "icon": "zoom-in", "label": "Zoom", "kind": "zoom"},
				{"id": "grid", "icon": "grid", "label": "Pixel grid", "kind": "toggle"},
				{"id": "fit", "icon": "fit", "label": "Fit", "kind": "button"},
			},
		},
	}
}

func parsePreviewer(t *testing.T, mutate func(pkg, previewer map[string]any)) error {
	t.Helper()
	pkg := validPreviewerPackage()
	if mutate != nil {
		mutate(pkg, pkg["previewer"].(map[string]any))
	}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParsePackage(raw)
	return err
}

func TestPreviewerPackage_acceptsAValidPackage(t *testing.T) {
	if err := parsePreviewer(t, nil); err != nil {
		t.Fatalf("valid previewer rejected: %v", err)
	}
	// Capability "edit" (the frame saves its scene as a version) is a known capability.
	if err := parsePreviewer(t, func(_, p map[string]any) {
		p["capabilities"] = []string{"edit"}
		delete(p, "toolbar")
	}); err != nil {
		t.Fatalf("edit capability rejected: %v", err)
	}
	// Capability "draft" with its draft block (the assist task the host runs) is valid.
	if err := parsePreviewer(t, func(_, p map[string]any) {
		p["capabilities"] = []string{"draft"}
		p["draft"] = map[string]string{"task": "draw-diagram"}
		delete(p, "toolbar")
	}); err != nil {
		t.Fatalf("draft capability rejected: %v", err)
	}
	// An upload placeholder parses (draft before upload) ...
	err := parsePreviewer(t, func(pkg, _ map[string]any) {
		pkg["artifacts"] = map[string]any{"bundle": map[string]string{"url": "", "sha256": ""}}
	})
	if err != nil {
		t.Fatalf("draft previewer rejected: %v", err)
	}
	// ... but is refused at publish.
	pkg, _ := ParsePackage(mustJSON(t, map[string]any{
		"kind": KindPreviewerPackageV1, "contract": PreviewerContractV1, "runtime": "bundle", "name": "p", "version": "1.0.0", "publisher": "x",
		"artifacts": map[string]any{"bundle": map[string]string{"url": "", "sha256": ""}},
		"previewer": map[string]any{"entry": "index.html", "match": map[string]any{"extensions": []string{".png"}}, "rank": 1},
	}))
	if err := ValidateCompleteArtifacts(pkg); err == nil {
		t.Fatal("publish of a placeholder bundle must fail")
	}
}

func TestPreviewerPackage_rejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(pkg, p map[string]any)
		want   string
	}{
		{"wrong contract", func(pkg, _ map[string]any) { pkg["contract"] = "bnbloc.previewer.contract/v2" }, "contract"},
		// File suggestions are prompt packs' (prompts with a files scope);
		// the strict schema refuses the old previewer field.
		{"suggestions", func(_, p map[string]any) { p["suggestions"] = []map[string]string{{"label": "x", "prompt": "y"}} }, "suggestions"},
		{"draft block without the capability", func(_, p map[string]any) {
			p["draft"] = map[string]string{"task": "draw-diagram"}
		}, `previewer.draft requires the "draft" capability`},
		{"draft capability without the block", func(_, p map[string]any) {
			p["capabilities"] = []string{"draft"}
			delete(p, "toolbar")
		}, `requires previewer.draft.task`},
		{"draft task not a content name", func(_, p map[string]any) {
			p["capabilities"] = []string{"draft"}
			p["draft"] = map[string]string{"task": "Draw Diagram/../x"}
			delete(p, "toolbar")
		}, "content name"},
		{"draft block with an unknown field", func(_, p map[string]any) {
			p["capabilities"] = []string{"draft"}
			p["draft"] = map[string]any{"task": "draw-diagram", "model": "x"}
			delete(p, "toolbar")
		}, "model"},
		{"wrong runtime", func(pkg, _ map[string]any) { pkg["runtime"] = "sidecar" }, `runtime must be "bundle"`},
		{"no previewer", func(pkg, _ map[string]any) { delete(pkg, "previewer") }, "requires previewer"},
		{"two artifacts", func(pkg, _ map[string]any) {
			pkg["artifacts"].(map[string]any)["extra"] = map[string]string{"url": "", "sha256": ""}
		}, "exactly one artifact"},
		{"wrong artifact key", func(pkg, _ map[string]any) {
			pkg["artifacts"] = map[string]any{"darwin-arm64": map[string]string{"url": "", "sha256": ""}}
		}, "exactly one artifact"},
		{"network permission", func(pkg, _ map[string]any) {
			pkg["permissions"] = map[string]any{"network": []string{"example.com"}}
		}, "network"},
		{"endpoint", func(pkg, _ map[string]any) { pkg["endpoint"] = map[string]string{"baseUrl": "https://x"} }, "endpoint"},
		{"hooks", func(pkg, _ map[string]any) { pkg["hooks"] = []string{"chat_message.v1"} }, "hooks"},
		{"top-level capabilities", func(pkg, _ map[string]any) { pkg["capabilities"] = []string{"thumbnail"} }, "must not declare capabilities"},
		{"absolute entry", func(_, p map[string]any) { p["entry"] = "/index.html" }, "entry"},
		{"traversal entry", func(_, p map[string]any) { p["entry"] = "../index.html" }, "entry"},
		{"dot entry", func(_, p map[string]any) { p["entry"] = "./index.html" }, "entry"},
		{"non-html entry", func(_, p map[string]any) { p["entry"] = "main.js" }, "entry"},
		{"empty match", func(_, p map[string]any) { p["match"] = map[string]any{} }, "at least one"},
		{"any mime", func(_, p map[string]any) { p["match"] = map[string]any{"mimeTypes": []string{"*/*"}} }, "type/subtype"},
		{"upper mime", func(_, p map[string]any) { p["match"] = map[string]any{"mimeTypes": []string{"Image/PNG"}} }, "type/subtype"},
		{"mime without subtype", func(_, p map[string]any) { p["match"] = map[string]any{"mimeTypes": []string{"image"}} }, "type/subtype"},
		{"mime parameters", func(_, p map[string]any) {
			p["match"] = map[string]any{"mimeTypes": []string{"text/plain; charset=utf-8"}}
		}, "type/subtype"},
		{"duplicate mime", func(_, p map[string]any) {
			p["match"] = map[string]any{"mimeTypes": []string{"image/png", "image/png"}}
		}, "duplicate"},
		{"extension without dot", func(_, p map[string]any) { p["match"] = map[string]any{"extensions": []string{"png"}} }, ".ext"},
		{"upper extension", func(_, p map[string]any) { p["match"] = map[string]any{"extensions": []string{".PNG"}} }, ".ext"},
		{"long extension chain", func(_, p map[string]any) { p["match"] = map[string]any{"extensions": []string{".a.b.c.d"}} }, ".ext"},
		{"too many extensions", func(_, p map[string]any) {
			exts := []string{}
			for i := 0; i < 33; i++ {
				exts = append(exts, "."+strings.Repeat("x", i%15+1)+string(rune('a'+i%26)))
			}
			p["match"] = map[string]any{"extensions": exts}
		}, "at most"},
		{"rank zero", func(_, p map[string]any) { p["rank"] = 0 }, "rank"},
		{"rank too high", func(_, p map[string]any) { p["rank"] = 1001 }, "rank"},
		{"unknown capability", func(_, p map[string]any) { p["capabilities"] = []string{"network"} }, "unknown previewer capability"},
		{"toolbar without capability", func(_, p map[string]any) { p["capabilities"] = []string{"thumbnail"} }, "requires the \"toolbar\" capability"},
		{"unknown icon", func(_, p map[string]any) {
			p["toolbar"] = []map[string]string{{"id": "a", "icon": "https://evil/icon.png", "label": "A", "kind": "button"}}
		}, "icon"},
		{"unknown kind", func(_, p map[string]any) {
			p["toolbar"] = []map[string]string{{"id": "a", "icon": "grid", "label": "A", "kind": "slider"}}
		}, "kind"},
		{"bad id", func(_, p map[string]any) {
			p["toolbar"] = []map[string]string{{"id": "Zoom In", "icon": "grid", "label": "A", "kind": "button"}}
		}, "id"},
		{"duplicate id", func(_, p map[string]any) {
			p["toolbar"] = []map[string]string{{"id": "a", "icon": "grid", "label": "A", "kind": "button"}, {"id": "a", "icon": "fit", "label": "B", "kind": "button"}}
		}, "duplicate id"},
		{"empty label", func(_, p map[string]any) {
			p["toolbar"] = []map[string]string{{"id": "a", "icon": "grid", "label": " ", "kind": "button"}}
		}, "label"},
		{"long label", func(_, p map[string]any) {
			p["toolbar"] = []map[string]string{{"id": "a", "icon": "grid", "label": strings.Repeat("x", 41), "kind": "button"}}
		}, "label"},
		{"too many toolbar items", func(_, p map[string]any) {
			items := []map[string]string{}
			for i := 0; i < 21; i++ {
				items = append(items, map[string]string{"id": "b" + strings.Repeat("x", i), "icon": "grid", "label": "B", "kind": "button"})
			}
			p["toolbar"] = items
		}, "at most 20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parsePreviewer(t, tt.mutate)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v; want containing %q", err, tt.want)
			}
		})
	}
}

func TestPreviewerPayload_onlyOnPreviewerKind(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"kind": KindNotifierPackageV1, "contract": NotifierContractV1, "runtime": "endpoint", "name": "n", "version": "1.0.0", "publisher": "x",
		"endpoint":  map[string]string{"baseUrl": "https://n.example.com"},
		"previewer": map[string]any{"entry": "index.html", "match": map[string]any{"extensions": []string{".png"}}, "rank": 1},
	})
	if _, err := ParsePackage(raw); err == nil || !strings.Contains(err.Error(), "previewer is only valid") {
		t.Fatalf("err = %v", err)
	}
}

// The Go vocabularies must equal the frame protocol's (the TypeScript file is
// the single source of truth both the host and the SDK import).
func TestPreviewerVocabulariesMatchFrameProtocol(t *testing.T) {
	src, err := os.ReadFile("../../web/previewer-sdk/src/protocol.ts")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"PREVIEWER_CAPABILITIES": PreviewerCapabilities,
		"TOOLBAR_KINDS":          PreviewerToolbarKinds,
		"TOOLBAR_ICONS":          PreviewerToolbarIcons,
		"SELECTION_KINDS":        wire.FileSelectionKinds,
	} {
		re := regexp.MustCompile(`(?s)export const ` + name + ` = \[(.*?)\] as const`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in protocol.ts", name)
		}
		got := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(string(m[1]), -1)
		var names []string
		for _, g := range got {
			names = append(names, g[1])
		}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: protocol.ts %v != Go %v", name, names, want)
		}
	}
	for name, want := range map[string]int{
		"TOOLBAR_LIMIT": maxPreviewerToolbar, "TOOLBAR_LABEL_LIMIT": maxPreviewerLabel,

		"SELECTION_LABEL_LIMIT": wire.MaxFileSelectionLabel, "SELECTION_DETAIL_LIMIT": wire.MaxFileSelectionDetail,
		"SELECTION_KEYS_LIMIT": wire.MaxFileSelectionKeys, "SELECTION_KEY_LIMIT": wire.MaxFileSelectionKey,
	} {
		re := regexp.MustCompile(`export const ` + name + ` = (\d+);`)
		m := re.FindSubmatch(src)
		if m == nil || string(m[1]) != strconv.Itoa(want) {
			t.Fatalf("%s: protocol.ts %s != Go %d", name, m, want)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
