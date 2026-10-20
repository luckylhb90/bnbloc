package contentpkg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// site is a parsed site snapshot of files.
func site(t *testing.T, files map[string]string) *tarbundle.Bundle {
	t.Helper()
	all := map[string][]byte{}
	for k, v := range files {
		all[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(all, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// carryForm is a room form with instructions, two apps made in the room
// (one a dist/ build, both titled alike) and two files.
func carryForm(t *testing.T) Form {
	return Form{Kind: "room", Name: "support-desk", Description: "Answers customers.", Instructions: "  You answer support tickets in our tone.\n",
		Captured: &RoomCapture{
			Apps: []CapturedApp{
				{Title: "Ticket board", Site: site(t, map[string]string{"index.html": "<p>board</p>", "app.js": "app.collection('t')"})},
				{Title: "Ticket board", Site: site(t, map[string]string{"dist/index.html": "<p>built</p>", "dist/a.js": "x", "README.md": "# Board\n", "src/main.ts": "y"})},
			},
			Files: []CapturedFile{{Name: "replies/template.md", Data: []byte("Dear …")}, {Name: "template.md", Data: []byte("Hi")}},
		}}
}

// A room form carries the room's instructions as INSTRUCTIONS.md, each
// captured app under its own folder and each file under files/, all listed
// in the manifest with their sizes; a script in an app needs no permission
// (nothing in a room package runs on a machine).
func TestRoomPackageCarriesInstructionsAppsAndFiles(t *testing.T) {
	p, _, err := Build(carryForm(t), "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Instructions != "You answer support tickets in our tone.\n" || p.Scripts {
		t.Fatalf("instructions = %q, scripts %v", p.Instructions, p.Scripts)
	}
	m := p.Manifest
	if len(m.AppSnapshots) != 2 || m.AppSnapshots[0] != (wire.RoomAppSnapshot{Title: "Ticket board", Dir: "apps/ticket-board"}) || m.AppSnapshots[1].Dir != "apps/2-ticket-board" {
		t.Fatalf("apps = %+v", m.AppSnapshots)
	}
	if len(m.Files) != 2 || m.Files[0] != (wire.RoomPackageFile{Name: "template.md", Path: "files/template.md", SizeBytes: int64(len("Dear …"))}) ||
		m.Files[1].Path != "files/2-template.md" || m.Files[1].Name != "template.md" {
		t.Fatalf("files = %+v", m.Files)
	}
	spec := m.RoomSpec()
	if len(spec.AppSnapshots) != 2 || len(spec.Files) != 2 {
		t.Fatalf("spec = %+v", spec)
	}
	first, err := RoomSite(p, m.AppSnapshots[0])
	if err != nil || string(first["index.html"]) != "<p>board</p>" || len(first) != 2 {
		t.Fatalf("the first app's site = %v, %v", keys(first), err)
	}
	// A dist/ build copies as WorkSite copies a work's: its files at the
	// root, the folder's README beside them, the sources left behind.
	built, err := RoomSite(p, m.AppSnapshots[1])
	if err != nil || string(built["index.html"]) != "<p>built</p>" || built["a.js"] == nil || built["README.md"] == nil || built["src/main.ts"] != nil {
		t.Fatalf("the built app's site = %v, %v", keys(built), err)
	}
	if string(RoomFile(p, m.Files[1])) != "Hi" {
		t.Fatalf("file = %q", RoomFile(p, m.Files[1]))
	}

	// A new version keeps what the form keeps of the edited one, by
	// folder and path, and drops the rest; it cannot name what the edited
	// version does not hold.
	next := Form{Kind: "room", Name: "support-desk", Instructions: "Answer briefly.",
		AppSnapshots: []wire.RoomAppSnapshot{{Title: "Board", Dir: "apps/2-ticket-board"}}, Files: []wire.RoomPackageFile{{Name: "reply.md", Path: "files/template.md"}}}
	q, _, err := Build(next, "1.0.1", Options{}, p)
	if err != nil {
		t.Fatal(err)
	}
	if q.Manifest.AppSnapshots[0].Title != "Board" || len(q.Manifest.Files) != 1 || q.Manifest.Files[0].Name != "reply.md" || q.Manifest.Files[0].SizeBytes != int64(len("Dear …")) {
		t.Fatalf("the next version = %+v", q.Manifest)
	}
	for _, p := range q.Bundle.Paths() {
		if strings.HasPrefix(p, "apps/ticket-board/") || p == "files/2-template.md" {
			t.Fatalf("%s was dropped by the form but kept", p)
		}
	}
	for _, bad := range []Form{
		{Kind: "room", Name: "support-desk", AppSnapshots: []wire.RoomAppSnapshot{{Title: "x", Dir: "apps/nope"}}},
		{Kind: "room", Name: "support-desk", Files: []wire.RoomPackageFile{{Name: "x", Path: "files/nope"}}},
	} {
		if _, _, err := Build(bad, "1.0.2", Options{}, p); err == nil || !strings.Contains(err.Error(), "edited version") {
			t.Errorf("keeping what the edited version lacks: %v", err)
		}
		if _, _, err := Build(bad, "1.0.0", Options{}, nil); err == nil || !strings.Contains(err.Error(), "baseVersion") {
			t.Errorf("keeping without a base: %v", err)
		}
	}
}

// The carried parts are checked: an app has a site's entry and no agent
// configuration, a file is one of its own under files/ of its stated size,
// the counts and the bound hold, and nothing else lies in the bundle.
func TestRoomPackageCarryValidation(t *testing.T) {
	app := map[string]any{"title": "Board", "dir": "apps/board"}
	file := map[string]any{"name": "notes.md", "path": "files/notes.md", "sizeBytes": 2}
	ok := func(extra map[string]any) map[string]any {
		out := map[string]any{"requires": map[string]any{"prompts": []string{"review-prompts@^1"}}, "appSnapshots": []map[string]any{app}, "files": []map[string]any{file}}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	files := map[string]string{"apps/board/index.html": "<p>x</p>", "files/notes.md": "hi", InstructionsFile: "Answer briefly."}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range files {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	p, err := Validate(roomBundle(t, ok(nil), files), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Instructions != "Answer briefly." {
		t.Fatalf("instructions = %q", p.Instructions)
	}
	tooMany := []map[string]any{}
	for i := range wire.MaxRoomFiles + 1 {
		tooMany = append(tooMany, map[string]any{"name": "f", "path": fmt.Sprintf("files/f%d", i), "sizeBytes": 0})
	}
	bad := []struct {
		name  string
		extra map[string]any
		files map[string]string
		want  string
	}{
		{"an app without an entry", nil, with(map[string]string{"apps/board/index.html": "", "apps/board/page.html": "x"}), "needs index.html"},
		{"an app folder that is not apps/<name>", map[string]any{"appSnapshots": []map[string]any{{"title": "Board", "dir": "board"}}}, nil, "apps/<name>"},
		{"an app twice", map[string]any{"appSnapshots": []map[string]any{app, app}}, nil, "twice"},
		{"an app without a title", map[string]any{"appSnapshots": []map[string]any{{"title": " ", "dir": "apps/board"}}}, nil, "a title"},
		{"agent configuration in an app", nil, with(map[string]string{"apps/board/CLAUDE.md": "x"}), "agent configuration"},
		{"a file's size misstated", map[string]any{"files": []map[string]any{{"name": "notes.md", "path": "files/notes.md", "sizeBytes": 3}}}, nil, "not 3"},
		{"a file outside files/", map[string]any{"files": []map[string]any{{"name": "notes.md", "path": "apps/board/index.html", "sizeBytes": 8}}}, nil, "under files/"},
		{"a file not in the bundle", map[string]any{"files": []map[string]any{file, {"name": "gone.md", "path": "files/gone.md", "sizeBytes": 0}}}, nil, "not in the bundle"},
		{"a file named as a folder", map[string]any{"files": []map[string]any{{"name": "a/notes.md", "path": "files/notes.md", "sizeBytes": 2}}}, nil, "a name of one file"},
		{"a file named as agent configuration", map[string]any{"files": []map[string]any{{"name": "AGENTS.md", "path": "files/notes.md", "sizeBytes": 2}}}, nil, "agent configuration"},
		{"too many files", map[string]any{"files": tooMany}, nil, "at most"},
		{"too many apps", map[string]any{"apps": names("w", MaxRoomApps)}, nil, "at most"},
		{"a file the manifest does not list", nil, with(map[string]string{"files/extra.md": "x"}), "a room package holds"},
		{"empty instructions", nil, with(map[string]string{InstructionsFile: "  "}), "empty"},
	}
	for _, c := range bad {
		fs := c.files
		if fs == nil {
			fs = files
		}
		if _, err := Validate(roomBundle(t, ok(c.extra), fs), "", Options{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// What a save captures stays within the bound, before anything is packed.
	big := carryForm(t)
	big.Captured.Files = append(big.Captured.Files, CapturedFile{Name: "big.bin", Data: make([]byte, wire.MaxRoomCarryBytes)})
	if _, _, err := Build(big, "1.0.0", Options{}, nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a capture past the bound: %v", err)
	}
}

// The bound of what a room package carries is one site snapshot's.
func TestRoomCarryBoundIsASiteSnapshots(t *testing.T) {
	if wire.MaxRoomCarryBytes != tarbundle.SiteLimits.MaxBytes {
		t.Fatalf("wire.MaxRoomCarryBytes = %d, a site snapshot's cap %d", wire.MaxRoomCarryBytes, tarbundle.SiteLimits.MaxBytes)
	}
}
