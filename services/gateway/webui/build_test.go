package webui

import (
	"io/fs"
	"testing"
)

// Build reads the identity the web build writes beside index.html.
func TestBuildReadsEmbeddedIdentity(t *testing.T) {
	if _, err := fs.Stat(files, "static/build.json"); err != nil {
		t.Skip("web bundle not built into webui/static")
	}
	if b := Build(); b == nil || b.Version == "" {
		t.Fatalf("Build() = %+v", b)
	}
}
