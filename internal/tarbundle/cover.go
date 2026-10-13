package tarbundle

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A cover (docs/prd/2026-10-13-app-gallery.md §4, §9;
// docs/prd/2026-10-13-works-gallery-and-make-similar.md §2): the image a
// card shows, cover.png, cover.webp or cover.jpg (.jpeg) at the root of a
// site snapshot or a work component's bundle, the first of these present.
// It is an image only: its bytes must sniff as the type its name says (an
// SVG, HTML or any other content is never a cover), at most
// wire.MaxWorkCoverBytes. SiteCover is the one rule of a cover file: the
// gateway's site cover route and the work component validator
// (contentpkg) both read it; WorkCover puts a work's order on top of it.

// coverFiles are the cover file names, in order, with the one media type
// each may hold.
var coverFiles = []struct{ name, mimeType string }{
	{"cover.png", "image/png"},
	{"cover.webp", "image/webp"},
	{"cover.jpg", "image/jpeg"},
	{"cover.jpeg", "image/jpeg"},
}

// Cover is a bundle's cover image.
type Cover struct {
	File     string
	MimeType string
	Data     []byte
}

var (
	// ErrCoverTooLarge: the cover is over wire.MaxWorkCoverBytes.
	ErrCoverTooLarge = fmt.Errorf("the cover is larger than %d bytes", wire.MaxWorkCoverBytes)
	// ErrCoverNotImage: the cover's bytes are not the image its name says.
	ErrCoverNotImage = errors.New("the cover is not the PNG, WebP or JPEG image its name says")
)

// SiteCover returns the bundle's cover: found=false when it has none; err
// (ErrCoverTooLarge, ErrCoverNotImage, wrapped with its file) when its
// cover is refused.
func SiteCover(b *Bundle) (c Cover, found bool, err error) {
	for _, f := range coverFiles {
		data, ok := b.File(f.name)
		if !ok {
			continue
		}
		if len(data) > wire.MaxWorkCoverBytes {
			return Cover{}, true, fmt.Errorf("%s: %w", f.name, ErrCoverTooLarge)
		}
		if http.DetectContentType(data) != f.mimeType {
			return Cover{}, true, fmt.Errorf("%s: %w", f.name, ErrCoverNotImage)
		}
		return Cover{File: f.name, MimeType: f.mimeType, Data: data}, true, nil
	}
	return Cover{}, false, nil
}

// IsCoverFile reports a file named as a cover (its last path segment,
// any case): a frame an agent saved for a work that is not a site.
func IsCoverFile(name string) bool {
	base := strings.ToLower(path.Base(name))
	for _, f := range coverFiles {
		if base == f.name {
			return true
		}
	}
	return false
}

// CoverFile is the cover file an image becomes in a bundle: the cover
// name of the type its bytes sniff as (ErrCoverNotImage for anything but a
// PNG, WebP or JPEG; ErrCoverTooLarge over wire.MaxWorkCoverBytes).
func CoverFile(data []byte) (string, error) {
	if len(data) > wire.MaxWorkCoverBytes {
		return "", ErrCoverTooLarge
	}
	sniffed := http.DetectContentType(data)
	for _, f := range coverFiles {
		if sniffed == f.mimeType {
			return f.name, nil
		}
	}
	return "", ErrCoverNotImage
}

// WorkCover is a work's cover, in the works PRD's order: its cover file
// (SiteCover; a refused one is the error), else, for an image work, the
// image itself (image: its path in the bundle) when its bytes are a PNG,
// WebP or JPEG within wire.MaxWorkCoverBytes, else none (found=false: the
// generated cover). A video work's cover is the frame an agent saved as
// its cover file.
func WorkCover(b *Bundle, image string) (c Cover, found bool, err error) {
	if c, found, err = SiteCover(b); found || image == "" {
		return c, found, err
	}
	data, ok := b.File(image)
	if !ok || len(data) > wire.MaxWorkCoverBytes {
		return Cover{}, false, nil
	}
	sniffed := http.DetectContentType(data)
	for _, f := range coverFiles {
		if sniffed == f.mimeType {
			return Cover{File: image, MimeType: sniffed, Data: data}, true, nil
		}
	}
	return Cover{}, false, nil
}
