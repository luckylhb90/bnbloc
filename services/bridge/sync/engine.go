// Package sync defines the pluggable file-sync seam used by the bridge: a
// SyncEngine mirrors a local project directory with one peer over a transport
// (a bnbloc-relay raw stream). The Go engine (gosync) is the first implementation;
// others (e.g. a mutagen wrapper) can register under a different name and be
// selected per room without changing the bridge.
package sync

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
)

// ConflictEvent is surfaced when concurrent edits to one path cannot be merged
// automatically. It is never swallowed: the room shows it and both versions are
// preserved on disk.
type ConflictEvent struct {
	Path         string // relative path in the project dir
	PeerID       string // the remote peer whose version conflicted
	ConflictCopy string // relative path of the preserved remote copy (.CONFLICT-...)
	Reason       string // "merge_conflict" | "binary" | "no_ancestor"
}

// SyncStatus is a point-in-time snapshot of an engine's activity.
type SyncStatus struct {
	Engine     string
	Running    bool
	FilesTotal int
	LastError  string
}

// SyncConfig parameterizes one engine run against one peer.
type SyncConfig struct {
	Dir        string              // absolute project directory to mirror
	Conn       net.Conn            // relay raw stream to the peer (reliable, ordered)
	SelfID     string              // this peer's stable id (for vector clocks)
	PeerID     string              // remote peer's stable id
	Ignore     []string            // ignore globs (matched against relative paths)
	AuthToken  string              // room-scoped shared secret; a peer whose HELLO token differs is rejected
	OnConflict func(ConflictEvent) // optional; surfaced to the room
}

// SyncEngine mirrors Dir with one peer over Conn until ctx is cancelled or Stop
// is called. Implementations must be safe to Stop concurrently with Start.
type SyncEngine interface {
	Start(ctx context.Context, cfg SyncConfig) error
	Stop() error
	Status() SyncStatus
	Name() string
}

// Factory constructs a fresh engine instance.
type Factory func() SyncEngine

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes an engine available by name. It panics on a duplicate name so
// wiring mistakes fail loudly at startup.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("sync: duplicate engine registration: " + name)
	}
	registry[name] = f
}

// New constructs the engine registered under name.
func New(name string) (SyncEngine, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sync: no engine registered as %q", name)
	}
	return f(), nil
}

// Names lists registered engine names (sorted) for diagnostics.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
