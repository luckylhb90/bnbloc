package tarbundle

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/png"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestSiteCover(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 5))); err != nil {
		t.Fatal(err)
	}
	cover := buf.Bytes()
	parse := func(files map[string][]byte) *Bundle {
		t.Helper()
		raw, err := PackFiles(files, SiteLimits)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Parse(raw, "", SiteLimits)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if c, found, err := SiteCover(parse(map[string][]byte{"index.html": nil, "cover.png": cover})); !found || err != nil || c.MimeType != "image/png" || c.File != "cover.png" {
		t.Fatalf("a PNG cover = %+v %v %v", c, found, err)
	}
	if _, found, _ := SiteCover(parse(map[string][]byte{"index.html": nil, "cover.svg": []byte("<svg/>")})); found {
		t.Fatal("an SVG is a cover")
	}
	if _, _, err := SiteCover(parse(map[string][]byte{"cover.webp": []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")})); !errors.Is(err, ErrCoverNotImage) {
		t.Fatalf("an SVG named webp = %v", err)
	}
	if _, _, err := SiteCover(parse(map[string][]byte{"cover.png": append(cover, make([]byte, wire.MaxAppCoverBytes)...)})); !errors.Is(err, ErrCoverTooLarge) {
		t.Fatalf("an oversized cover = %v", err)
	}
}

// Which file is a cover and what it may hold is SiteCover's alone: no
// other Go source names a cover file.
func TestCoverRuleOwned(t *testing.T) {
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if rel == "internal/tarbundle/cover.go" {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(s, "cover.") {
				t.Errorf("%s names the cover file %q: ask tarbundle.SiteCover", fset.Position(lit.Pos()), s)
			}
			return true
		})
	})
}
