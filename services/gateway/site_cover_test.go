package gateway

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func coverImage(t *testing.T, enc func(*bytes.Buffer, image.Image) error) string {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, image.NewRGBA(image.Rect(0, 0, 16, 10))); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// An app's cover is served as the image its name says, and only as one:
// nosniff, a sandbox CSP and the size cap; an SVG or a mislabeled file is
// never a cover, and a site without one answers 404 (the card draws the
// generated cover). A non-member reads none.
func TestSiteCoverServedOnlyAsAnImage(t *testing.T) {
	e := newAppGW(t)
	pngCover := coverImage(t, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })
	jpgCover := coverImage(t, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
	maya := "Authorization: Bearer " + e.session("maya")
	cover := func(site string) resp {
		t.Helper()
		return e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/versions/1/cover", nil, maya)
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		code  int
		ctype string
	}{
		{"png", map[string]string{"index.html": "app", "cover.png": pngCover}, http.StatusOK, "image/png"},
		{"jpeg", map[string]string{"index.html": "app", "cover.jpg": jpgCover}, http.StatusOK, "image/jpeg"},
		{"png before jpeg", map[string]string{"index.html": "app", "cover.jpg": jpgCover, "cover.png": pngCover}, http.StatusOK, "image/png"},
		{"none", map[string]string{"index.html": "app"}, http.StatusNotFound, ""},
		{"svg is no cover", map[string]string{"index.html": "app", "cover.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`}, http.StatusNotFound, ""},
		{"svg named png", map[string]string{"index.html": "app", "cover.png": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`}, http.StatusUnsupportedMediaType, ""},
		{"html named webp", map[string]string{"index.html": "app", "cover.webp": `<!doctype html><script>alert(1)</script>`}, http.StatusUnsupportedMediaType, ""},
		{"jpeg named png", map[string]string{"index.html": "app", "cover.png": jpgCover}, http.StatusUnsupportedMediaType, ""},
		{"over the cap", map[string]string{"index.html": "app", "cover.png": pngCover + strings.Repeat("\x00", wire.MaxWorkCoverBytes)}, http.StatusRequestEntityTooLarge, ""},
	} {
		site := e.realSite(e.roomID, c.files)
		r := cover(site)
		if r.status != c.code {
			t.Fatalf("%s: %d (%d bytes), want %d", c.name, r.status, len(r.body), c.code)
		}
		if c.code != http.StatusOK {
			if strings.Contains(string(r.body), "<script>") {
				t.Fatalf("%s: a refusal echoes the file: %s", c.name, r.body)
			}
			continue
		}
		h := r.header
		if h.Get("Content-Type") != c.ctype || h.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || len(r.body) > wire.MaxWorkCoverBytes {
			t.Fatalf("%s: headers %v (%d bytes)", c.name, h, len(r.body))
		}
	}
	site := e.realSite(e.roomID, map[string]string{"index.html": "app", "cover.png": pngCover})
	bob := "Authorization: Bearer " + e.session("bob")
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/versions/1/cover", nil, bob); r.status != http.StatusForbidden {
		t.Fatalf("a non-member's cover = %d", r.status)
	}
	other := e.newRoom("jack")
	if r := e.call(http.MethodGet, "/v1/rooms/"+other+"/artifacts/"+site+"/versions/1/cover", nil, "Authorization: Bearer "+e.session("jack")); r.status != http.StatusNotFound {
		t.Fatalf("a site read through another room = %d", r.status)
	}
}

// A member's apps route to the room service, readable by any member (a
// viewer included) and spending no cost budget.
func TestMyAppsRoute(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	p := mustParseAPIPath(t, http.MethodGet, "/v1/my/apps")
	if got := s.route(p); got != "room" {
		t.Fatalf("/v1/my/apps routed to %q, want room", got)
	}
	if perm := requiredPermission(http.MethodGet, p); perm != rbac.PermRead {
		t.Fatalf("/v1/my/apps needs %s, want read", perm)
	}
	if rateLimited(http.MethodGet, p) {
		t.Fatal("/v1/my/apps is rate limited")
	}
	if got := s.route(mustParseAPIPath(t, http.MethodGet, "/v1/my/apps/x")); got != "" {
		t.Fatalf("/v1/my/apps/x routed to %q", got)
	}
}
