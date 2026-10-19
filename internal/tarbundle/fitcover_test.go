package tarbundle

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// noisyPNG is a w×h PNG that compresses badly (an image model's poster
// does too), optionally half transparent.
func noisyPNG(t *testing.T, w, h int, alpha uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(rnd.Intn(256))
		if i%4 == 3 {
			img.Pix[i] = alpha
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// An image over the cover cap is scaled into a JPEG cover within it; one
// already a cover is kept as it is; what is no image is refused.
func TestFitCover(t *testing.T) {
	big := noisyPNG(t, 1536, 1024, 0xff)
	if len(big) <= wire.MaxWorkCoverBytes {
		t.Fatalf("the test image is only %d bytes", len(big))
	}
	got, err := FitCover(big)
	if err != nil {
		t.Fatal(err)
	}
	if name, err := CoverFile(got); err != nil || name != "cover.jpg" {
		t.Fatalf("cover file = %q, %v (%d bytes)", name, err, len(got))
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil || cfg.Width > 1600 || cfg.Width*1024 != cfg.Height*1536 && cfg.Width*1024/1536 != cfg.Height {
		t.Fatalf("cover %dx%d, %v", cfg.Width, cfg.Height, err)
	}
	small := noisyPNG(t, 64, 64, 0xff)
	if got, err := FitCover(small); err != nil || !bytes.Equal(got, small) {
		t.Fatalf("a cover-sized image was changed: %v", err)
	}
	if _, err := FitCover([]byte("<svg></svg>")); err != ErrCoverNotImage {
		t.Fatalf("an SVG = %v", err)
	}
}

// Transparency lies on white, not black (a JPEG has no alpha).
func TestFitCoverTransparentOnWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4000, 10))
	var b bytes.Buffer
	_ = png.Encode(&b, img) // all transparent black
	data := append(b.Bytes(), make([]byte, wire.MaxWorkCoverBytes)...)
	got, err := FitCover(data)
	if err != nil {
		t.Fatal(err)
	}
	m, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, bl, _ := m.At(10, 3).RGBA(); r < 0xf000 || g < 0xf000 || bl < 0xf000 {
		t.Fatalf("transparent pixel = %v", color.RGBA64{uint16(r), uint16(g), uint16(bl), 0xffff})
	}
}
