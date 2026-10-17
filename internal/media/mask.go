package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg" // DecodeConfig reads a JPEG input's size
	"image/png"
)

// Region is a rectangle of an image in its own pixels (origin top-left).
type Region struct{ X, Y, W, H int }

// ErrRegionOutside: the region is empty or not inside the image.
var ErrRegionOutside = errors.New("the region must be a non-empty rectangle inside the image")

// ImageSize reads an image's width and height from its header (PNG, JPEG
// or WebP).
func ImageSize(b []byte) (int, int, error) {
	if SniffImage(b) == "image/webp" {
		return webpSize(b)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// EditMask is the one rule for an edit's mask: a PNG of the input's size,
// opaque everywhere but the region, which is fully transparent (the area
// the provider may change, the OpenAI images/edits convention).
func EditMask(input []byte, r Region) ([]byte, error) {
	w, h, err := ImageSize(input)
	if err != nil {
		return nil, err
	}
	if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > w || r.Y+r.H > h {
		return nil, ErrRegionOutside
	}
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	opaque := color.NRGBA{A: 0xFF}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
				continue // transparent: may change
			}
			m.SetNRGBA(x, y, opaque)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// webpSize reads a WebP's canvas size from its first chunk: VP8X (the
// extended header), VP8L (lossless) or VP8 (lossy).
func webpSize(b []byte) (int, int, error) {
	bad := errors.New("not a readable WebP header")
	if len(b) < 30 {
		return 0, 0, bad
	}
	switch string(b[12:16]) {
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, nil
	case "VP8L":
		if b[20] != 0x2F {
			return 0, 0, bad
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3FFF) + 1, int((bits>>14)&0x3FFF) + 1, nil
	case "VP8 ":
		if b[23] != 0x9D || b[24] != 0x01 || b[25] != 0x2A {
			return 0, 0, bad
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3FFF), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3FFF), nil
	}
	return 0, 0, bad
}
