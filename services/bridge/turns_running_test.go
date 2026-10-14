package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readTurns(t *testing.T, dir string) RunningTurns {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, TurnsFile))
	if err != nil {
		t.Fatal(err)
	}
	var v RunningTurns
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The bridge records its running turns per core for the desktop, which
// applies a core update only when none runs.
func TestRunningTurnsFile(t *testing.T) {
	dir := t.TempDir()
	b := &Bridge{cfg: Config{StateDir: dir}}

	doneA := b.turnRunning("acp")
	doneB := b.turnRunning("acp")
	doneC := b.turnRunning("nimbus")
	got := readTurns(t, dir)
	if got.PID != os.Getpid() || got.Running["acp"] != 2 || got.Running["nimbus"] != 1 {
		t.Fatalf("while running = %+v", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, TurnsFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("turns file mode = %v (%v), want 0600", fi.Mode().Perm(), err)
	}

	doneA()
	doneA() // done is idempotent
	doneC()
	if got := readTurns(t, dir); got.Running["acp"] != 1 || got.Running["nimbus"] != 0 || len(got.Running) != 1 {
		t.Fatalf("after two ended = %+v", got)
	}
	doneB()
	if got := readTurns(t, dir); len(got.Running) != 0 {
		t.Fatalf("idle = %+v", got)
	}

	// Without a state directory the count is kept, no file is written.
	(&Bridge{}).turnRunning("acp")()
}
