package store

import "testing"

func TestArtifactFileTyping(t *testing.T) {
	cases := []struct {
		name, mime, kind string
		head             []byte
	}{
		{"docs/spec.md", "text/markdown", ArtifactKindMarkdown, nil},
		{"index.html", "text/html", ArtifactKindHTML, nil},
		{"main.go", "text/x-go", ArtifactKindCode, nil},
		{"Makefile", "text/plain", ArtifactKindCode, nil},
		{"chart.png", "image/png", ArtifactKindImage, nil},
		{"logo.svg", "image/svg+xml", ArtifactKindImage, nil},
		{"report.pdf", "application/pdf", ArtifactKindFile, nil},
		{"blob", "image/png", ArtifactKindImage, []byte("\x89PNG\r\n\x1a\n\x00\x00")},
		{"blob2", "application/octet-stream", ArtifactKindFile, []byte{0, 1, 2, 3}},
	}
	for _, c := range cases {
		m := ArtifactMIME(c.name, c.head)
		if m != c.mime {
			t.Errorf("ArtifactMIME(%q) = %q, want %q", c.name, m, c.mime)
		}
		if k := ArtifactKindForFile(c.name, m); k != c.kind {
			t.Errorf("ArtifactKindForFile(%q) = %q, want %q", c.name, k, c.kind)
		}
	}
	if !IsInlineSafeImage("image/png; x=1") || IsInlineSafeImage("image/svg+xml") {
		t.Fatal("inline-safe image set wrong")
	}
}
