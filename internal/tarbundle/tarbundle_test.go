package tarbundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type entry struct {
	name     string
	typeflag byte
	body     string
	link     string
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: e.typeflag, Linkname: e.link}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestPackParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"index.html": "<html>", "assets/main.js": "js", ".DS_Store": "junk", ".git/config": "x"} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := Pack(dir, PreviewerLimits)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Pack(dir, PreviewerLimits)
	if !bytes.Equal(raw, again) {
		t.Fatal("Pack is not deterministic")
	}
	b, err := Parse(raw, digest(raw), PreviewerLimits)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.Paths(), ","); got != "assets/main.js,index.html" {
		t.Fatalf("paths = %s (hidden files must be skipped)", got)
	}
	if data, ok := b.File("assets/main.js"); !ok || string(data) != "js" {
		t.Fatalf("file = %q %v", data, ok)
	}
	if err := b.RequireEntry("index.html"); err != nil {
		t.Fatal(err)
	}
	if err := b.RequireEntry("missing.html"); err == nil {
		t.Fatal("missing entry accepted")
	}
}

func TestParse_refusesDigestMismatch(t *testing.T) {
	raw := archive(t, entry{name: "index.html", typeflag: tar.TypeReg, body: "x"})
	if _, err := Parse(raw, strings.Repeat("0", 64), PreviewerLimits); !errors.Is(err, ErrDigest) {
		t.Fatalf("err = %v; want ErrDigest", err)
	}
}

func TestParse_refusesUnsafeArchives(t *testing.T) {
	tests := map[string][]byte{
		"traversal":     archive(t, entry{name: "../evil.html", typeflag: tar.TypeReg, body: "x"}),
		"absolute":      archive(t, entry{name: "/etc/passwd", typeflag: tar.TypeReg, body: "x"}),
		"nested dots":   archive(t, entry{name: "a/../../b.js", typeflag: tar.TypeReg, body: "x"}),
		"symlink":       archive(t, entry{name: "index.html", typeflag: tar.TypeSymlink, link: "/etc/passwd"}),
		"hardlink":      archive(t, entry{name: "a.html", typeflag: tar.TypeReg, body: "x"}, entry{name: "b.html", typeflag: tar.TypeLink, link: "a.html"}),
		"device":        archive(t, entry{name: "dev", typeflag: tar.TypeChar}),
		"duplicate":     archive(t, entry{name: "a.js", typeflag: tar.TypeReg, body: "x"}, entry{name: "./a.js", typeflag: tar.TypeReg, body: "y"}),
		"space in name": archive(t, entry{name: "a b.js", typeflag: tar.TypeReg, body: "x"}),
		"empty":         archive(t),
		"not a tar":     []byte("<html>definitely not a tar archive, just text that is long enough</html>"),
	}
	for name, raw := range tests {
		if _, err := Parse(raw, "", PreviewerLimits); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// Truncated: header claims more bytes than present.
	full := archive(t, entry{name: "index.html", typeflag: tar.TypeReg, body: strings.Repeat("x", 2048)})
	if _, err := Parse(full[:1024], "", PreviewerLimits); err == nil {
		t.Fatal("truncated archive accepted")
	}
}

func TestParse_enforcesCaps(t *testing.T) {
	big := archive(t, entry{name: "big.bin", typeflag: tar.TypeReg, body: strings.Repeat("x", PreviewerLimits.MaxBytes+1)})
	if _, err := Parse(big, "", PreviewerLimits); err == nil {
		t.Fatal("oversized bundle accepted")
	}
	var many []entry
	for i := 0; i <= PreviewerLimits.MaxFiles; i++ {
		many = append(many, entry{name: "f" + strings.Repeat("x", i%50) + "-" + itoa(i) + ".js", typeflag: tar.TypeReg, body: "x"})
	}
	if _, err := Parse(archive(t, many...), "", PreviewerLimits); err == nil {
		t.Fatal("bundle with too many files accepted")
	}
}

func TestContentType(t *testing.T) {
	for p, want := range map[string]string{
		"index.html": "text/html; charset=utf-8", "a/main.JS": "text/javascript; charset=utf-8", "s.css": "text/css; charset=utf-8",
		"f.woff2": "font/woff2", "i.svg": "image/svg+xml", "x.bin": "application/octet-stream", "noext": "application/octet-stream",
	} {
		if got := ContentType(p); got != want {
			t.Fatalf("ContentType(%q) = %q; want %q", p, got, want)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
