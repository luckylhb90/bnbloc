package tarbundle

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An app's cover (docs/prd/2026-10-13-app-gallery.md §4, §9): the image a
// card shows, cover.png, cover.webp or cover.jpg (.jpeg) at the root of a
// site snapshot or an app component's bundle, the first of these present.
// It is an image only: its bytes must sniff as the type its name says (an
// SVG, HTML or any other content is never a cover), at most
// wire.MaxAppCoverBytes. SiteCover is the one rule: the gateway's site
// cover route and the app component validator (contentpkg) both read it.

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
	// ErrCoverTooLarge: the cover is over wire.MaxAppCoverBytes.
	ErrCoverTooLarge = fmt.Errorf("the cover is larger than %d bytes", wire.MaxAppCoverBytes)
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
		if len(data) > wire.MaxAppCoverBytes {
			return Cover{}, true, fmt.Errorf("%s: %w", f.name, ErrCoverTooLarge)
		}
		if http.DetectContentType(data) != f.mimeType {
			return Cover{}, true, fmt.Errorf("%s: %w", f.name, ErrCoverNotImage)
		}
		return Cover{File: f.name, MimeType: f.mimeType, Data: data}, true, nil
	}
	return Cover{}, false, nil
}
