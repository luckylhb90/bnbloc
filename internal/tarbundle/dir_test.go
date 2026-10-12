package tarbundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// The one directory packer drops junk, hidden files and protected
// workspace paths, and nothing else.
func TestPackerExclusions(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":            {Data: []byte("<html>")},
		"assets/main.js":        {Data: []byte("js")},
		"_partials/head.html":   {Data: []byte("h")},
		".DS_Store":             {Data: []byte("junk")},
		"assets/._main.js":      {Data: []byte("junk")},
		"__MACOSX/x":            {Data: []byte("junk")},
		"node_modules/a/i.js":   {Data: []byte("junk")},
		"Thumbs.db":             {Data: []byte("junk")},
		".env":                  {Data: []byte("SECRET=1")},
		".git/config":           {Data: []byte("x")},
		".room/INDEX.md":        {Data: []byte("history")},
		".claude/settings.json": {Data: []byte("{}")},
		"CLAUDE.md":             {Data: []byte("agent config")},
		"AGENTS.md":             {Data: []byte("agent config")},
		"opencode.json":         {Data: []byte("{}")},
	}
	raw, err := PackFS(fsys, ".", PreviewerLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(raw, "", PreviewerLimits)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.Paths(), ","); got != "_partials/head.html,assets/main.js,index.html" {
		t.Fatalf("packed %s", got)
	}
	// Under a sub-directory the protection is judged on the workspace path:
	// a draft's own CLAUDE.md is content, the workspace root's is not.
	entries := []Entry{
		{Rel: "component.json", Path: "drafts/x/component.json", Size: 2, Read: func() ([]byte, error) { return []byte("{}"), nil }},
		{Rel: "CLAUDE.md", Path: "drafts/x/CLAUDE.md", Size: 1, Read: func() ([]byte, error) { return []byte("c"), nil }},
		{Rel: "CLAUDE.md", Path: "CLAUDE.md", Size: 1, Read: func() ([]byte, error) { panic("a protected path was read") }},
		{Rel: ".DS_Store", Path: "drafts/x/.DS_Store", Size: 1, Read: func() ([]byte, error) { panic("junk was read") }},
	}
	if _, n, err := PackEntries(entries, ContentLimits); err != nil || n != 2 {
		t.Fatalf("PackEntries = %d, %v", n, err)
	}
	if _, _, err := PackEntries(entries[2:], ContentLimits); err == nil {
		t.Fatal("nothing packable must fail the pack")
	}
}

func tarOf(t *testing.T, format tar.Format, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, Format: format}
		if format == tar.FormatPAX {
			hdr.PAXRecords = map[string]string{"comment": "made by a desktop tool"}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An upload is read leniently about its format (PAX headers, "./" names,
// junk) but never rewrites an unsafe name.
func TestReadArchive(t *testing.T) {
	files, err := ReadArchive(tarOf(t, tar.FormatPAX, map[string]string{"./a/SKILL.md": "s", "a/.DS_Store": "j", "__MACOSX/a/._SKILL.md": "j"}), ContentLimits)
	if err != nil || len(files) != 1 || string(files["a/SKILL.md"]) != "s" {
		t.Fatalf("ReadArchive = %v, %v", files, err)
	}
	for _, bad := range []string{"../escape.md", "/etc/passwd", "a/../../x", "a//b"} {
		if _, err := ReadArchive(tarOf(t, tar.FormatUSTAR, map[string]string{bad: "x"}), ContentLimits); err == nil {
			t.Errorf("tar entry %q accepted", bad)
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(bad)
		_, _ = w.Write([]byte("x"))
		zw.Close()
		if _, err := ReadArchive(buf.Bytes(), ContentLimits); err == nil {
			t.Errorf("zip entry %q accepted", bad)
		}
	}
}

// Only tarbundle reads or writes archives: a stored bundle through
// Cache.LoadPinned, an upload through ReadArchive, a pack through
// PackEntries. A second extractor drifts from the bundle rules (the
// content import rewrote "../x" and accepted what Parse refuses).
func TestOnlyTarbundleReadsArchives(t *testing.T) {
	allowed := map[string]string{
		// A room handoff package is a streamed zip of a room's history and
		// attachments, read entry by entry with its own manifest; not a
		// file bundle.
		"services/room/handoff_export.go": "room handoff package",
		"services/room/handoff_import.go": "room handoff package",
		// The Sandbox Protocol conformance suite exercises the protocol's
		// own archive endpoints (a machine's tar stream).
		"sandbox/conformance/conformance.go": "sandbox protocol archive endpoints",
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "target", ".git", "web", "apps":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/tarbundle/") || allowed[rel] != "" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "archive/tar" || p == "archive/zip" {
				t.Errorf("%s imports %s: read archives through internal/tarbundle", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
