package tarbundle

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // Decode reads a PNG image

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// coverWidths are the widths FitCover tries, widest first, and
// coverQualities the JPEG qualities at each.
var (
	coverWidths    = []int{1600, 1200, 800}
	coverQualities = []int{85, 70}
)

// FitCover makes an image a cover (docs/prd/2026-10-19-work-covers.md
// §2): one that already is a cover file (CoverFile) as it is; a PNG or
// JPEG over the cover cap scaled to at most 1600 pixels wide and saved as
// a JPEG within wire.MaxWorkCoverBytes (QA 2026-10-19: a 1536×1024 poster
// from the image model was over the cap, and its work fell back to the
// generated cover). Transparency becomes white. ErrCoverNotImage for
// anything it cannot read, ErrCoverTooLarge when no size fits.
func FitCover(data []byte) ([]byte, error) {
	if _, err := CoverFile(data); err == nil {
		return data, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrCoverNotImage
	}
	for _, w := range coverWidths {
		scaled := scaleOnWhite(src, w)
		for _, q := range coverQualities {
			var out bytes.Buffer
			if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: q}); err != nil {
				return nil, err
			}
			if out.Len() <= wire.MaxWorkCoverBytes {
				return out.Bytes(), nil
			}
		}
	}
	return nil, ErrCoverTooLarge
}

// scaleOnWhite is src at most width wide (its aspect kept; a narrower one
// keeps its size), each pixel the average of the pixels it covers, laid on
// white.
func scaleOnWhite(src image.Image, width int) *image.RGBA {
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	if w > width {
		w, h = width, max(1, h*width/sb.Dx())
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := sb.Min.Y+y*sb.Dy()/h, sb.Min.Y+(y+1)*sb.Dy()/h
		y1 = max(y1, y0+1)
		for x := 0; x < w; x++ {
			x0, x1 := sb.Min.X+x*sb.Dx()/w, sb.Min.X+(x+1)*sb.Dx()/w
			x1 = max(x1, x0+1)
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, b, a, n = r+uint64(cr), g+uint64(cg), b+uint64(cb), a+uint64(ca), n+1
				}
			}
			// Premultiplied colour over white: c + (1 - alpha) * white.
			white := n*0xffff - a
			dst.SetRGBA(x, y, color.RGBA{uint8((r + white) / n >> 8), uint8((g + white) / n >> 8), uint8((b + white) / n >> 8), 0xff})
		}
	}
	return dst
}
