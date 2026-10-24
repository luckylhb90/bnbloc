package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfineD(t *testing.T, home, name, body string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(home, ".bnbloc", machineDenyDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMachineDenyReadReadsOnlyWhatItTrusts(t *testing.T) {
	home := t.TempDir()
	writeConfineD(t, home, "b.json", `{"denyRead":["/z/two"]}`, 0o600)
	writeConfineD(t, home, "a.json", `{"denyRead":["/z/one","/z/uno"]}`, 0o644)
	writeConfineD(t, home, "notes.txt", `{"denyRead":["/z/not-json-by-name"]}`, 0o600)
	var note bytes.Buffer
	if got := strings.Join(machineDenyRead(home, &note), ","); got != "/z/one,/z/uno,/z/two" {
		t.Fatalf("paths = %q (file name order, .json only)", got)
	}
	if note.Len() != 0 {
		t.Fatalf("a note for good files: %q", note.String())
	}

	bad := map[string]struct {
		body string
		mode os.FileMode
		want string
	}{
		"broken.json":   {`{"denyRead":`, 0o600, "unexpected end"},
		"relative.json": {`{"denyRead":["rel/dir"]}`, 0o600, "not absolute"},
		"loose.json":    {`{"denyRead":["/z/loose"]}`, 0o666, "writable by others"},
		"group.json":    {`{"denyRead":["/z/group"]}`, 0o620, "writable by others"},
	}
	home = t.TempDir()
	for name, c := range bad {
		writeConfineD(t, home, name, c.body, c.mode)
	}
	os.Symlink("/etc/hosts", filepath.Join(home, ".bnbloc", machineDenyDir, "link.json"))
	note.Reset()
	if got := machineDenyRead(home, &note); len(got) != 0 {
		t.Fatalf("paths from files that must be skipped: %v", got)
	}
	for name, c := range bad {
		if !strings.Contains(note.String(), name) || !strings.Contains(note.String(), c.want) {
			t.Errorf("no note naming %s and %q in %q", name, c.want, note.String())
		}
	}
	if !strings.Contains(note.String(), "link.json") {
		t.Errorf("a symlink was followed silently: %q", note.String())
	}
	if got := machineDenyRead(t.TempDir(), &note); got != nil {
		t.Fatalf("no directory: %v", got)
	}
}

// The profile `confine` builds names the machine's denial, unless the core that
// confines the tool itself turned it off.
func TestConfineAppliesTheMachineDenyDropIns(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no host sandbox")
	}
	home := t.TempDir()
	login := filepath.Join(home, ".somelogin")
	if err := os.MkdirAll(login, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfineD(t, home, "tool.json", `{"denyRead":["`+login+`"]}`, 0o600)
	t.Setenv("HOME", home)
	ws := t.TempDir()
	profile := func() string {
		var out, errb bytes.Buffer
		if code := confine([]string{"--workspace", ws, "--print-profile", "--", "sh", "-c", "true"}, &out, &errb); code != 0 {
			t.Fatalf("confine exit %d: %s", code, errb.String())
		}
		return out.String()
	}
	canon, _ := filepath.EvalSymlinks(login)
	if p := profile(); !strings.Contains(p, login) && !strings.Contains(p, canon) {
		t.Fatalf("the machine's denial is not in the profile:\n%s", p)
	}
	t.Setenv(machineDenyOffEnv, "off")
	if p := profile(); strings.Contains(p, login) || strings.Contains(p, canon) {
		t.Fatalf("the denial applies though the tool's own core turned it off:\n%s", p)
	}
}
