package roomlog

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// copyRoom is a room whose deliverables the copy pass reads: a site (its
// bundle), an inline document, a blob file, and a blob over the copy bound.
type copyRoom struct {
	*fakeFetch
	site []byte
}

func newCopyRoom(t *testing.T) *copyRoom {
	t.Helper()
	site := sitePack(t, map[string][]byte{"index.html": []byte("<script src=app.js></script>"), "app.js": []byte("render()")})
	r := &copyRoom{fakeFetch: &fakeFetch{bodies: map[string]string{}}, site: site}
	r.setSite(t, 1, site)
	r.bodies["/v1/rooms/r1/artifacts/art_doc/versions/2"] = `{"version":{"artifactId":"art_doc","version":2,"storage":"inline","content":"# Plan","authorUserId":"maya"}}`
	r.bodies["/v1/rooms/r1/artifacts/art_png/versions/1"] = `{"version":{"artifactId":"art_png","version":1,"storage":"blob","sizeBytes":4,"authorUserId":"maya"}}`
	r.bodies["/v1/rooms/r1/artifacts/art_png/versions/1/content"] = "\x89PNG"
	r.bodies["/v1/rooms/r1/artifacts/art_big/versions/1"] = fmt.Sprintf(`{"version":{"artifactId":"art_big","version":1,"storage":"blob","sizeBytes":%d,"authorUserId":"maya"}}`, MaxDeliverableCopyBytes+1)
	return r
}

func (r *copyRoom) setSite(t *testing.T, v int, bundle []byte) {
	t.Helper()
	r.bodies[fmt.Sprintf("/v1/rooms/r1/artifacts/art_site/versions/%d", v)] = fmt.Sprintf(`{"version":{"artifactId":"art_site","version":%d,"storage":"blob","sizeBytes":%d,"contentHash":%q,"authorUserId":"maya"}}`, v, len(bundle), digest.Of(bundle))
	r.bodies[fmt.Sprintf("/v1/rooms/r1/artifacts/art_site/versions/%d/content", v)] = string(bundle)
}

func sitePack(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	raw, err := tarbundle.PackFiles(files, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func copyList(siteVersion int) []Deliverable {
	return []Deliverable{
		{ArtifactID: "art_site", Title: "dist", Kind: "site", FileName: "dist", Version: siteVersion},
		{ArtifactID: "art_doc", Title: "plan.md", Kind: "markdown", FileName: "plan.md", Version: 2},
		{ArtifactID: "art_png", Title: "shot", Kind: "image", FileName: "shots/shot.png", Version: 1},
		{ArtifactID: "art_big", Title: "video.mp4", Kind: "file", FileName: "video.mp4", Version: 1},
	}
}

// recordingWrite records every file a pass writes.
type recordingWrite struct{ files map[string]string }

func (w *recordingWrite) write(_ context.Context, name string, data []byte) error {
	if !core.WorkspaceFileAllowed(name) {
		return fmt.Errorf("%s is not a bnbloc-owned path", name)
	}
	w.files[name] = string(data)
	return nil
}

func (w *recordingWrite) names() []string {
	out := make([]string, 0, len(w.files))
	for n := range w.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// A pass copies each deliverable's latest version under
// .room/deliverables/<id>/ (a site as its file tree, a document or file as
// one file), skips one over the copy bound with a note, writes nothing for
// a version the workspace already holds, and rewrites only what changed.
func TestCopyDeliverables(t *testing.T) {
	room := newCopyRoom(t)
	w := &recordingWrite{files: map[string]string{}}
	copies, st := CopyDeliverables(context.Background(), room.fetch, "r1", copyList(1), nil, w.write)
	want := []string{
		".room/deliverables/art_doc/plan.md",
		".room/deliverables/art_png/shot.png",
		".room/deliverables/art_site/app.js",
		".room/deliverables/art_site/index.html",
	}
	if got := w.names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("copied %v, want %v", got, want)
	}
	if w.files[".room/deliverables/art_site/app.js"] != "render()" || w.files[".room/deliverables/art_doc/plan.md"] != "# Plan" || w.files[".room/deliverables/art_png/shot.png"] != "\x89PNG" {
		t.Fatalf("copied contents = %q", w.files)
	}
	if c := copies["art_site"]; c.Dir != ".room/deliverables/art_site/" || c.Files != 2 || c.Version != 1 {
		t.Fatalf("site copy = %+v", c)
	}
	if c := copies["art_big"]; c.Dir != "" || c.TooLarge != MaxDeliverableCopyBytes+1 {
		t.Fatalf("oversize copy = %+v", c)
	}
	f := DeliverablesFile(copyList(1), nil, Producer{}, copies).Content
	for _, line := range []string{
		"id art_site: from unknown; copy: .room/deliverables/art_site/",
		fmt.Sprintf("id art_big: from unknown; no copy: %d bytes, over the %d-byte copy bound", MaxDeliverableCopyBytes+1, MaxDeliverableCopyBytes),
	} {
		if !strings.Contains(f, line) {
			t.Errorf("artifacts.md lacks %q:\n%s", line, f)
		}
	}

	// The same versions again: nothing is read or written.
	room.calls, w.files = nil, map[string]string{}
	copies, st = CopyDeliverables(context.Background(), room.fetch, "r1", copyList(1), st, w.write)
	if len(room.calls) != 0 || len(w.files) != 0 {
		t.Fatalf("an unchanged pass read %v and wrote %v", room.calls, w.names())
	}
	if c := copies["art_site"]; c.Dir == "" || c.Files != 2 {
		t.Fatalf("an unchanged copy is not reported: %+v", c)
	}

	// A new site version: only the site is copied again.
	room.setSite(t, 2, sitePack(t, map[string][]byte{"index.html": []byte("<script src=app.js></script>"), "app.js": []byte("render(2)")}))
	room.calls = nil
	_, st = CopyDeliverables(context.Background(), room.fetch, "r1", copyList(2), st, w.write)
	if got := w.names(); strings.Join(got, ",") != ".room/deliverables/art_site/app.js,.room/deliverables/art_site/index.html" || w.files[".room/deliverables/art_site/app.js"] != "render(2)" {
		t.Fatalf("a new site version copied %v", w.files)
	}
	if st["art_site"].Version != 2 || st["art_doc"].Version != 2 || st["art_big"].TooLarge == 0 {
		t.Fatalf("state = %+v", st)
	}
	for _, c := range room.calls {
		if !strings.Contains(c, "art_site") {
			t.Fatalf("a pass read %s, which did not change", c)
		}
	}
	// The state is what a workspace persists.
	if _, err := json.Marshal(st); err != nil {
		t.Fatal(err)
	}
}

// One pass writes at most MaxDeliverableCopyPassBytes: the rest wait for
// the next turn (listed without a copy, absent from the state), and a
// deliverable that cannot be read keeps the copy it had.
func TestCopyDeliverablesPassBoundAndFailures(t *testing.T) {
	room := &fakeFetch{bodies: map[string]string{}}
	var list []Deliverable
	n := MaxDeliverableCopyPassBytes/MaxDeliverableCopyBytes + 1
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("art_%d", i)
		list = append(list, Deliverable{ArtifactID: id, Title: id + ".bin", Kind: "file", FileName: id + ".bin", Version: 1})
		room.bodies["/v1/rooms/r1/artifacts/"+id+"/versions/1"] = fmt.Sprintf(`{"version":{"version":1,"storage":"blob","sizeBytes":%d}}`, MaxDeliverableCopyBytes)
		room.bodies["/v1/rooms/r1/artifacts/"+id+"/versions/1/content"] = "x"
	}
	w := &recordingWrite{files: map[string]string{}}
	copies, st := CopyDeliverables(context.Background(), room.fetch, "r1", list, nil, w.write)
	last := list[n-1].ArtifactID
	if len(w.files) != n-1 || !copies[last].Pending || copies[last].Dir != "" {
		t.Fatalf("pass wrote %d files, last copy %+v", len(w.files), copies[last])
	}
	if _, ok := st[last]; ok {
		t.Fatalf("a deferred deliverable is in the state: %+v", st)
	}
	// Next pass: the deferred one is copied; the rest are not read again.
	w.files = map[string]string{}
	_, st = CopyDeliverables(context.Background(), room.fetch, "r1", list, st, w.write)
	if got := w.names(); len(got) != 1 || !strings.HasSuffix(got[0], last+".bin") || len(st) != n {
		t.Fatalf("second pass wrote %v, state %d entries", got, len(st))
	}

	// A new version that cannot be read: the older copy is still listed.
	list[0].Version = 2
	copies, st = CopyDeliverables(context.Background(), room.fetch, "r1", list, st, w.write)
	if c := copies[list[0].ArtifactID]; !c.Pending || c.Version != 1 || c.Dir == "" || st[list[0].ArtifactID].Version != 1 {
		t.Fatalf("unreadable new version: copy %+v, state %+v", c, st[list[0].ArtifactID])
	}
	f := DeliverablesFile(list, nil, Producer{}, copies).Content
	if !strings.Contains(f, "copy of v1 (not yet the latest): .room/deliverables/"+list[0].ArtifactID+"/") {
		t.Fatalf("artifacts.md:\n%s", f)
	}
}
