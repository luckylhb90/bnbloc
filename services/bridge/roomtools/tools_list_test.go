package roomtools

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/tools_list.json")

// ToolsListJSON is the tools/list answer as the agent reads it (indented
// for review). It rides every request of a session as a cached prefix.
func toolsListJSON(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"tools": DefaultRegistry().Descriptors()}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The full tools/list is pinned (testdata/tools_list.json): every
// descriptor, title and parameter description a session carries.
func TestToolsListGolden(t *testing.T) {
	got := toolsListJSON(t)
	path := filepath.Join("testdata", "tools_list.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("tools/list differs from %s (%v; run with -update after reviewing):\n%s", path, err, got)
	}
}
