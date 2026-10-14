package bridge

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Running turns, for the desktop's "apply an update when idle"
// (docs/prd/2026-10-13-live-turns.md): a turn has no fixed length, so
// restarting a core (or the bridge) for an update must wait until no turn
// runs. The bridge keeps the count of its running turns per core in
// <StateDir>/turns.json, rewritten whenever a turn starts or ends; the
// desktop that supervises it reads the file and trusts it only while its
// pid is the bridge it runs. No network surface: the file lives in the
// member's own state directory.

// TurnsFile is the file's name in the bridge's state directory.
const TurnsFile = "turns.json"

// RunningTurns is TurnsFile's content.
type RunningTurns struct {
	PID         int            `json:"pid"`
	Running     map[string]int `json:"running"`
	UpdatedAtMs int64          `json:"updatedAtMs"`
}

// turnCounter counts the turns the bridge is driving, per core.
type turnCounter struct {
	mu      sync.Mutex
	running map[string]int
}

// turnRunning records a turn of core as running until the returned done is
// called (once).
func (b *Bridge) turnRunning(core string) (done func()) {
	b.turns.add(b.cfg.StateDir, core, 1)
	var once sync.Once
	return func() { once.Do(func() { b.turns.add(b.cfg.StateDir, core, -1) }) }
}

func (c *turnCounter) add(stateDir, core string, delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running = map[string]int{}
	}
	c.running[core] += delta
	if c.running[core] <= 0 {
		delete(c.running, core)
	}
	if stateDir == "" {
		return
	}
	snapshot := RunningTurns{PID: os.Getpid(), Running: map[string]int{}, UpdatedAtMs: time.Now().UnixMilli()}
	for k, v := range c.running {
		snapshot.Running[k] = v
	}
	if err := writeTurnsFile(filepath.Join(stateDir, TurnsFile), snapshot); err != nil {
		slog.Warn("bnbloc bridge could not record its running turns",
			"event", "bnbloc.bridge.turns_file_failed", "error", err)
	}
}

func writeTurnsFile(path string, v RunningTurns) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".turns-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
