package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// pngOf is a w×h opaque PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A painted mask passes when it is a PNG of the input's size with a
// transparent pixel; another size, another format or nothing painted is
// refused.
func TestPaintedMask(t *testing.T) {
	in := pngOf(t, 8, 6)
	painted, err := EditMask(in, Region{X: 1, Y: 1, W: 2, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := PaintedMask(in, painted); err != nil || !bytes.Equal(got, painted) {
		t.Fatalf("painted mask = %v", err)
	}
	for name, mask := range map[string][]byte{
		"another size":    pngOf(t, 4, 6),
		"nothing painted": pngOf(t, 8, 6),
		"not a png":       []byte("GIF89a"),
	} {
		if _, err := PaintedMask(in, mask); !errors.Is(err, ErrMaskShape) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

// The mask is the input's size, transparent exactly inside the region and
// opaque elsewhere; a region outside the image is refused.
func TestEditMask(t *testing.T) {
	in := pngOf(t, 8, 6)
	raw, err := EditMask(in, Region{X: 2, Y: 1, W: 3, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	m, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := m.Bounds(); b.Dx() != 8 || b.Dy() != 6 {
		t.Fatalf("mask size = %v", b)
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			_, _, _, a := m.At(x, y).RGBA()
			inside := x >= 2 && x < 5 && y >= 1 && y < 3
			if inside != (a == 0) {
				t.Fatalf("pixel (%d,%d) alpha %d, inside %v", x, y, a, inside)
			}
		}
	}
	for _, r := range []Region{{X: 6, Y: 0, W: 3, H: 1}, {X: 0, Y: 0, W: 0, H: 1}, {X: -1, Y: 0, W: 2, H: 2}} {
		if _, err := EditMask(in, r); !errors.Is(err, ErrRegionOutside) {
			t.Errorf("region %+v = %v", r, err)
		}
	}
}

// A WebP's size comes from its header: lossless (VP8L) and extended (VP8X).
func TestImageSizeWebP(t *testing.T) {
	// VP8L: 0x2F signature, then 14-bit width-1 and height-1.
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2F"), 0, 0, 0, 0, 0, 0, 0, 0, 0)
	bits := uint32(99) | uint32(49)<<14
	vp8l[21], vp8l[22], vp8l[23], vp8l[24] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
	if w, h, err := ImageSize(vp8l); err != nil || w != 100 || h != 50 {
		t.Fatalf("VP8L = %d×%d %v", w, h, err)
	}
	vp8x := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x00\x00\x00\x00"), 0, 0, 0, 0, 0x1F, 0x03, 0x00, 0xDF, 0x01, 0x00)
	if w, h, err := ImageSize(vp8x); err != nil || w != 800 || h != 480 {
		t.Fatalf("VP8X = %d×%d %v", w, h, err)
	}
}

// An edit with a mask and references posts every image as an "image[]"
// part (the one to change first) and the mask as "mask".
func TestGenerateEditWithMaskAndReferences(t *testing.T) {
	s := settings(t)
	var parts []string
	var mask []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		for _, fh := range r.MultipartForm.File["image[]"] {
			parts = append(parts, fh.Filename)
		}
		if f, _, err := r.FormFile("mask"); err == nil {
			mask, _ = io.ReadAll(f)
			_ = f.Close()
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}]}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapImage, srv.URL+"/v1")
	in := pngOf(t, 4, 4)
	m, err := EditMask(in, Region{X: 0, Y: 0, W: 2, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider(t, s, CapImage).Generate(context.Background(), ImageRequest{
		Prompt: "put the logo here", Input: &ImageInput{Bytes: in, MimeType: "image/png"}, Mask: m,
		References: []ImageInput{{Bytes: tinyPNG, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0] != "image-1" || parts[1] != "image-2" || !bytes.Equal(mask, m) {
		t.Fatalf("parts %v mask %d bytes", parts, len(mask))
	}
}
