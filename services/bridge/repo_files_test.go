package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A working copy's tracked files are not deliverables; what git does not
// track (a report written into the clone, a file beside it) is; a working
// copy git cannot list keeps all its paths out; a workspace that is itself
// a repository keeps only its tracked files out.
func TestTrackedFilesOfWorkingCopies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(wd string, args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=m", "-c", "user.email=m@example.com"}, args...)...)
		cmd.Dir = filepath.Join(dir, wd)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write("proj/main.go", "package main")
	write("proj/docs/design.md", "# design")
	git("proj", "init", "-q")
	git("proj", "add", ".")
	git("proj", "commit", "-q", "-m", "x")
	write("proj/report.md", "# what I found")
	write("notes.md", "# notes")
	write("broken/.git", "gitdir: /nowhere")
	write("broken/a.txt", "a")

	got := trackedFiles(context.Background(), dir, []string{"proj/main.go", "proj/docs/design.md", "proj/report.md", "notes.md", "broken/a.txt", "proj/gone.go"})
	want := map[string]bool{"proj/main.go": true, "proj/docs/design.md": true, "broken/a.txt": true}
	if len(got) != len(want) {
		t.Fatalf("tracked = %v, want %v", got, want)
	}
	for p := range want {
		if !got[p] {
			t.Fatalf("tracked = %v, want %v", got, want)
		}
	}

	// The workspace itself a repository: its tracked files only.
	git(".", "init", "-q")
	git(".", "add", "notes.md")
	git(".", "commit", "-q", "-m", "y")
	write("chart.png", "png")
	got = trackedFiles(context.Background(), dir, []string{"notes.md", "chart.png", "proj/report.md"})
	if !got["notes.md"] || got["chart.png"] || got["proj/report.md"] {
		t.Fatalf("root repository: tracked = %v", got)
	}
}

// The publisher leaves a clone's tracked files out and publishes the
// report the agent wrote into it (docs/prd/2026-10-27-repo-files-and-folders.md).
func TestPublishLeavesAClonesFilesOut(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "proj"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "proj", "main.go"), []byte("package main"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=m", "-c", "user.email=m@example.com", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = filepath.Join(dir, "proj"), append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	fc := newFakeCore()
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: newSessionFilesCore(fc)})
	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s", workdir: dir}
	start := fc.mtime + 30_000
	fc.set("proj/main.go", "package main")
	fc.set("proj/report.md", "# what I found")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	if len(cg.creates) != 1 || cg.creates[0]["title"] != "proj/report.md" {
		t.Fatalf("published %v, want only proj/report.md", cg.creates)
	}
}
