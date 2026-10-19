package tarbundle

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A work's cover order (works PRD §2): its cover file, else an image
// work's image within the cap, else none (the generated cover). A video
// work has a cover only when an agent saved a frame as its cover file.
func TestWorkCoverOrder(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 5))); err != nil {
		t.Fatal(err)
	}
	img := buf.Bytes()
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
	coverName := "cover" + ".png"
	// 1. The cover file wins over the image.
	if c, found, err := WorkCover(parse(map[string][]byte{"chart.png": img, coverName: img}), "chart.png"); !found || err != nil || c.File != coverName {
		t.Fatalf("cover file first = %+v %v %v", c, found, err)
	}
	// 2. An image work without one: the image itself.
	if c, found, err := WorkCover(parse(map[string][]byte{"chart.png": img}), "chart.png"); !found || err != nil || c.File != "chart.png" || c.MimeType != "image/png" {
		t.Fatalf("the image = %+v %v %v", c, found, err)
	}
	// ... within the cap, else the generated cover (never an error).
	big := append(append([]byte(nil), img...), make([]byte, wire.MaxWorkCoverBytes)...)
	if _, found, err := WorkCover(parse(map[string][]byte{"huge.png": big}), "huge.png"); found || err != nil {
		t.Fatalf("an oversized image = %v %v", found, err)
	}
	// ... and never an SVG.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`)
	if _, found, err := WorkCover(parse(map[string][]byte{"logo.svg": svg}), "logo.svg"); found || err != nil {
		t.Fatalf("an SVG image work's cover = %v %v", found, err)
	}
	// 3. A video work: its agent-made cover file, else none.
	clip := []byte("\x00\x00\x00\x18ftypmp42")
	if c, found, err := WorkCover(parse(map[string][]byte{"clip.mp4": clip, coverName: img}), ""); !found || err != nil || c.File != coverName {
		t.Fatalf("a video's frame = %+v %v %v", c, found, err)
	}
	if _, found, err := WorkCover(parse(map[string][]byte{"clip.mp4": clip}), ""); found || err != nil {
		t.Fatalf("a video without a frame = %v %v", found, err)
	}
	// The frame an agent saved becomes the cover file of its type.
	if !IsCoverFile("frames/COVER.PNG") || IsCoverFile("cover.svg") || IsCoverFile("covers.png") {
		t.Fatal("IsCoverFile")
	}
	if name, err := CoverFile(img); err != nil || name != coverName {
		t.Fatalf("a PNG frame's cover file = %q %v", name, err)
	}
	if c, err := CoverImage(img); err != nil || c.File != coverName || c.MimeType != "image/png" || len(c.Data) != len(img) {
		t.Fatalf("a PNG as a cover = %+v %v", c, err)
	}
	if _, err := CoverFile(svg); !errors.Is(err, ErrCoverNotImage) {
		t.Fatalf("an SVG frame = %v", err)
	}
	if _, err := CoverFile(big); !errors.Is(err, ErrCoverTooLarge) {
		t.Fatalf("an oversized frame = %v", err)
	}
	// A refused cover file is refused for a work too.
	if _, _, err := WorkCover(parse(map[string][]byte{"chart.png": img, coverName: []byte("<p>not an image</p>")}), "chart.png"); !errors.Is(err, ErrCoverNotImage) {
		t.Fatalf("a refused cover file = %v", err)
	}
}
