package sync

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func init() { Register("gosync", func() SyncEngine { return &goSync{} }) }

const (
	sidecarDir      = ".bnbloc-sync"
	sidecarFile     = "state.json"
	protocolVersion = 1
	scanInterval    = 1500 * time.Millisecond
	// maxFileBytes bounds a single synced file; larger files are skipped rather
	// than merged, protecting both peers from memory blowups over the stream.
	maxFileBytes = 32 << 20 // 32 MiB
	// maxFrameBytes bounds one decoded protocol frame on the wire (a file frame
	// base64-encodes its content, so allow headroom over maxFileBytes).
	maxFrameBytes = 64 << 20 // 64 MiB
)

// fileState is the durable per-path record: its vector clock, the digest of the
// content that produced that clock, and the last content both peers agreed on
// (the merge ancestor). Deleted files keep a tombstone (Deleted=true).
type fileState struct {
	Clock    VectorClock `json:"clock"`
	Digest   string      `json:"digest"`
	Ancestor []byte      `json:"ancestor,omitempty"`
	Deleted  bool        `json:"deleted,omitempty"`
}

// frame is one length-prefixed protocol message over the relay stream.
type frame struct {
	Type    string      `json:"type"`              // hello|manifest|update|delete|request|file|conflict
	Version int         `json:"version,omitempty"` // hello
	Peer    string      `json:"peer,omitempty"`    // hello
	Token   string      `json:"token,omitempty"`   // hello: room-scoped shared secret
	Entries []manifest  `json:"entries,omitempty"` // manifest
	Path    string      `json:"path,omitempty"`
	Clock   VectorClock `json:"clock,omitempty"`
	Mode    uint32      `json:"mode,omitempty"`
	Content []byte      `json:"content,omitempty"` // file (JSON base64-encodes []byte)
	Digest  string      `json:"digest,omitempty"`
	Reason  string      `json:"reason,omitempty"` // conflict
}

type manifest struct {
	Path    string      `json:"path"`
	Clock   VectorClock `json:"clock"`
	Digest  string      `json:"digest"`
	Deleted bool        `json:"deleted,omitempty"`
}

type goSync struct {
	mu       sync.Mutex
	cfg      SyncConfig
	ignore   *ignoreMatcher
	index    map[string]*fileState
	running  bool
	lastErr  string
	nFiles   int
	enc      *json.Encoder
	out      chan frame
	done     chan struct{}
	authed   chan struct{} // closed once the peer's HELLO token verifies
	authOnce sync.Once
	peerID   string // learned from HELLO; authority election uses it
	cancel   context.CancelFunc
}

func (g *goSync) Name() string { return "gosync" }

// authedOnce marks the peer authenticated exactly once, unblocking frame
// handling and our manifest send.
func (g *goSync) authedOnce() { g.authOnce.Do(func() { close(g.authed) }) }

// isAuthority reports whether this peer performs merges (the lower id merges and
// announces; the peer applies the result). Peer id is learned from HELLO.
func (g *goSync) isAuthority() bool {
	g.mu.Lock()
	peer := g.peerID
	g.mu.Unlock()
	if peer == "" {
		peer = g.cfg.PeerID
	}
	return g.cfg.SelfID < peer
}

// acceptPath is the trust gate for every peer-supplied path: it rejects
// traversal/absolute paths and ignored paths, returning the cleaned relative
// path when safe. Incoming frames that fail this are dropped.
func (g *goSync) acceptPath(rel string) (string, bool) {
	clean, ok := safeRel(rel)
	if !ok {
		return "", false
	}
	if g.ignore.match(clean) {
		return "", false
	}
	return clean, true
}

func (g *goSync) Status() SyncStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return SyncStatus{Engine: "gosync", Running: g.running, FilesTotal: g.nFiles, LastError: g.lastErr}
}

func (g *goSync) Stop() error {
	g.mu.Lock()
	c := g.cancel
	g.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

// Start runs the sync loop: load index, send HELLO+MANIFEST, then concurrently
// (a) read frames from the peer and apply them, and (b) periodically scan the
// dir for local changes and push updates. Returns when ctx ends or the conn dies.
func (g *goSync) Start(ctx context.Context, cfg SyncConfig) error {
	ctx, cancel := context.WithCancel(ctx)
	g.mu.Lock()
	g.cfg = cfg
	g.ignore = newIgnoreMatcher(cfg.Ignore)
	g.index = g.loadIndex()
	g.running = true
	g.cancel = cancel
	g.enc = json.NewEncoder(cfg.Conn)
	g.out = make(chan frame, 256)
	g.done = make(chan struct{})
	g.authed = make(chan struct{})
	g.mu.Unlock()
	var doneOnce sync.Once
	closeDone := func() { doneOnce.Do(func() { close(g.done) }) }
	defer func() {
		closeDone()
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
		g.saveIndex()
	}()

	// Initial scan to refresh the index against on-disk reality.
	g.scanLocal()

	// One writer goroutine drains the outbound queue; sends never block callers,
	// so sending from the read loop cannot deadlock when both peers send at once.
	go g.writeLoop()

	// Start reading before sending so the initial HELLO/MANIFEST exchange over an
	// unbuffered stream does not deadlock.
	inbox := make(chan frame, 256)
	errc := make(chan error, 1)
	go func() { errc <- g.readLoop(ctx, inbox) }()

	if err := g.send(frame{Type: "hello", Version: protocolVersion, Peer: cfg.SelfID, Token: cfg.AuthToken}); err != nil {
		return err
	}
	// Do not reveal our manifest (file paths + digests) until the peer's HELLO
	// token verifies, so a wrong-room peer learns nothing.
	manifestSent := false
	authedCh := g.authed

	// Single event loop: every state mutation (incoming frames and local rescans)
	// runs here, serially, so the index is never mutated concurrently.
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case <-authedCh:
			authedCh = nil // fire once; a closed channel is always ready
			manifestSent = true
			if err := g.sendManifest(); err != nil {
				return err
			}
		case f := <-inbox:
			g.handle(f)
		case <-ticker.C:
			if manifestSent {
				g.pushLocalChanges()
			}
		}
	}
}

// handle dispatches one decoded frame (called only from the event loop).
func (g *goSync) handle(f frame) {
	switch f.Type {
	case "manifest":
		g.onManifest(f.Entries)
	case "request":
		if rel, ok := g.acceptPath(f.Path); ok {
			g.onRequest(rel)
		}
	case "file":
		rel, ok := g.acceptPath(f.Path)
		if !ok || len(f.Content) > maxFileBytes {
			return
		}
		f.Path = rel
		g.onFile(f)
	case "delete":
		if rel, ok := g.acceptPath(f.Path); ok {
			f.Path = rel
			g.onDelete(f)
		}
	}
}

// --- protocol send ---

// send enqueues a frame for the async writer. It never blocks the caller on the
// wire, so sending from the read loop cannot deadlock when both peers send
// simultaneously over an unbuffered stream.
func (g *goSync) send(f frame) error {
	select {
	case g.out <- f:
		return nil
	case <-g.done:
		return fmt.Errorf("sync: engine stopped")
	}
}

// writeLoop drains the outbound queue to the wire, the single writer.
func (g *goSync) writeLoop() {
	for {
		select {
		case f := <-g.out:
			if err := g.enc.Encode(f); err != nil {
				return
			}
		case <-g.done:
			return
		}
	}
}

func (g *goSync) sendManifest() error {
	g.mu.Lock()
	entries := make([]manifest, 0, len(g.index))
	for p, st := range g.index {
		entries = append(entries, manifest{Path: p, Clock: st.Clock, Digest: st.Digest, Deleted: st.Deleted})
	}
	g.mu.Unlock()
	return g.send(frame{Type: "manifest", Entries: entries})
}

// --- protocol receive ---

func (g *goSync) readLoop(ctx context.Context, inbox chan<- frame) error {
	// Bound each decoded frame: the limited reader is reset per iteration so one
	// oversized frame errors out instead of exhausting memory.
	lr := &io.LimitedReader{R: g.cfg.Conn, N: maxFrameBytes}
	dec := json.NewDecoder(bufio.NewReaderSize(lr, 1<<20))
	for {
		lr.N = maxFrameBytes
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var f frame
		if err := dec.Decode(&f); err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		if f.Type == "hello" {
			if subtle.ConstantTimeCompare([]byte(f.Token), []byte(g.cfg.AuthToken)) != 1 {
				return fmt.Errorf("sync: peer auth token mismatch")
			}
			g.mu.Lock()
			if f.Peer != "" {
				g.peerID = f.Peer
			}
			g.mu.Unlock()
			g.authedOnce()
			continue
		}
		// Drop any non-hello frame that arrives before authentication: a peer
		// must present a valid HELLO token before we act on its data.
		select {
		case <-g.authed:
		default:
			continue
		}
		select {
		case inbox <- f:
		case <-ctx.Done():
			return ctx.Err()
		case <-g.done:
			return nil
		}
	}
}

// onManifest compares the peer's manifest to ours and requests files the peer
// has that we lack or that dominate ours.
func (g *goSync) onManifest(entries []manifest) {
	for _, e := range entries {
		rel, ok := g.acceptPath(e.Path)
		if !ok {
			continue
		}
		g.mu.Lock()
		local := g.index[rel]
		g.mu.Unlock()
		if local == nil {
			if !e.Deleted {
				_ = g.send(frame{Type: "request", Path: rel})
			}
			continue
		}
		if local.Digest == e.Digest && local.Deleted == e.Deleted {
			continue // identical
		}
		switch e.Clock.Compare(local.Clock) {
		case Dominates:
			if e.Deleted {
				g.applyRemoteDelete(rel, e.Clock)
			} else {
				_ = g.send(frame{Type: "request", Path: rel})
			}
		case Concurrent:
			// Need the remote content to merge; request it.
			if !e.Deleted {
				_ = g.send(frame{Type: "request", Path: rel})
			} else {
				// Concurrent delete vs local edit: keep local, surface conflict.
				g.reportConflict(rel, "", "concurrent_delete")
			}
		}
	}
}

func (g *goSync) onRequest(rel string) {
	full := filepath.Join(g.cfg.Dir, rel)
	data, err := os.ReadFile(full)
	if err != nil {
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		return
	}
	g.mu.Lock()
	st := g.index[rel]
	g.mu.Unlock()
	clk := VectorClock{}
	if st != nil {
		clk = st.Clock
	}
	_ = g.send(frame{Type: "file", Path: rel, Clock: clk, Mode: uint32(info.Mode().Perm()), Content: data, Digest: digest(data)})
}

// onFile applies an incoming file using vector-clock resolution and, on true
// concurrency, a three-way merge with a .CONFLICT fallback. No version is lost.
func (g *goSync) onFile(f frame) {
	full := filepath.Join(g.cfg.Dir, f.Path)
	g.mu.Lock()
	local := g.index[f.Path]
	g.mu.Unlock()

	if local == nil || local.Deleted {
		g.writeFile(full, f.Path, f.Content, f.Mode, f.Clock)
		return
	}
	switch f.Clock.Compare(local.Clock) {
	case Dominates:
		g.writeFile(full, f.Path, f.Content, f.Mode, f.Clock)
	case Equal, Dominated:
		// We are current or newer; ignore.
	case Concurrent:
		// Single merge authority avoids symmetric-merge divergence: the lower
		// peer id merges and announces the result; the higher peer keeps its
		// version and waits for the authority's merged file (which will then
		// dominate and apply). Both converge to one merged content + clock.
		if g.isAuthority() {
			g.resolveConcurrent(full, f)
		}
	}
}

func (g *goSync) resolveConcurrent(full string, f frame) {
	localData, _ := os.ReadFile(full)
	g.mu.Lock()
	st := g.index[f.Path]
	var ancestor []byte
	if st != nil {
		ancestor = st.Ancestor
	}
	g.mu.Unlock()

	if ancestor != nil {
		// Reached only by the merge authority (lower peer id). Merge with a fixed
		// operand order for determinism, bump self so the announced clock strictly
		// dominates the peer's, and send it; the peer then applies it via Dominates.
		merged, ok := threeWayMerge(ancestor, localData, f.Content)
		if ok {
			joined := f.Clock.Merge(st.Clock).Bump(g.cfg.SelfID)
			g.writeFile(full, f.Path, merged, f.Mode, joined)
			_ = g.send(frame{Type: "file", Path: f.Path, Clock: joined, Mode: f.Mode, Content: merged, Digest: digest(merged)})
			return
		}
	}
	// Unmergeable (or binary, or no ancestor): preserve both, surface conflict.
	copyRel := f.Path + ".CONFLICT-" + g.cfg.PeerID + "-" + time.Now().Format("20060102T150405")
	g.writeRaw(filepath.Join(g.cfg.Dir, copyRel), f.Content, f.Mode)
	reason := "merge_conflict"
	if ancestor == nil {
		reason = "no_ancestor"
	}
	if !isText(f.Content) || !isText(localData) {
		reason = "binary"
	}
	g.reportConflict(f.Path, copyRel, reason)
}

func (g *goSync) onDelete(f frame) {
	g.mu.Lock()
	local := g.index[f.Path]
	g.mu.Unlock()
	if local == nil {
		g.recordTombstone(f.Path, f.Clock)
		return
	}
	if f.Clock.Compare(local.Clock) == Dominates {
		g.applyRemoteDelete(f.Path, f.Clock)
	} else if f.Clock.Compare(local.Clock) == Concurrent {
		g.reportConflict(f.Path, "", "concurrent_delete")
	}
}

func (g *goSync) applyRemoteDelete(rel string, clk VectorClock) {
	_ = os.Remove(filepath.Join(g.cfg.Dir, rel))
	g.recordTombstone(rel, clk)
}

// --- disk writes + index ---

func (g *goSync) writeFile(full, rel string, data []byte, mode uint32, clk VectorClock) {
	g.writeRaw(full, data, mode)
	g.mu.Lock()
	g.index[rel] = &fileState{Clock: clk.Clone(), Digest: digest(data), Ancestor: append([]byte(nil), data...)}
	g.mu.Unlock()
}

func (g *goSync) writeRaw(full string, data []byte, mode uint32) {
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	perm := os.FileMode(mode).Perm()
	if perm == 0 {
		perm = 0o644
	}
	_ = os.WriteFile(full, data, perm)
}

func (g *goSync) recordTombstone(rel string, clk VectorClock) {
	g.mu.Lock()
	g.index[rel] = &fileState{Clock: clk.Clone(), Deleted: true}
	g.mu.Unlock()
}

func (g *goSync) reportConflict(rel, copyRel, reason string) {
	if g.cfg.OnConflict != nil {
		g.cfg.OnConflict(ConflictEvent{Path: rel, PeerID: g.cfg.PeerID, ConflictCopy: copyRel, Reason: reason})
	}
}

// --- local scanning ---

// scanLocal reconciles the index with on-disk files: new/changed files get a
// bumped clock; disappeared files become tombstones.
func (g *goSync) scanLocal() {
	seen := map[string]bool{}
	_ = filepath.Walk(g.cfg.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(g.cfg.Dir, path)
		if rerr != nil || g.ignore.match(rel) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		seen[rel] = true
		d := digest(data)
		g.mu.Lock()
		st := g.index[rel]
		if st == nil || st.Deleted || st.Digest != d {
			clk := VectorClock{}
			var ancestor []byte
			if st != nil {
				clk = st.Clock.Clone()
				// Preserve the last synced content as the merge ancestor; a local
				// edit must NOT redefine the baseline, or 3-way merge degrades to
				// "only remote changed" and silently drops the local edit.
				ancestor = st.Ancestor
			}
			if ancestor == nil {
				ancestor = append([]byte(nil), data...)
			}
			clk.Bump(g.cfg.SelfID)
			g.index[rel] = &fileState{Clock: clk, Digest: d, Ancestor: ancestor}
		}
		g.mu.Unlock()
		return nil
	})
	// Tombstone files that vanished.
	g.mu.Lock()
	for rel, st := range g.index {
		if !st.Deleted && !seen[rel] {
			st.Clock.Bump(g.cfg.SelfID)
			g.index[rel] = &fileState{Clock: st.Clock, Deleted: true}
		}
	}
	g.nFiles = len(seen)
	g.mu.Unlock()
}

// pushLocalChanges rescans and sends updates/deletes whose clock changed.
func (g *goSync) pushLocalChanges() {
	before := g.snapshotDigests()
	g.scanLocal()
	after := g.snapshotStates()
	for rel, st := range after {
		if b, ok := before[rel]; ok && b == stateSig(st) {
			continue // unchanged
		}
		if st.Deleted {
			_ = g.send(frame{Type: "delete", Path: rel, Clock: st.Clock})
		} else {
			full := filepath.Join(g.cfg.Dir, rel)
			data, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			info, _ := os.Stat(full)
			var mode uint32 = 0o644
			if info != nil {
				mode = uint32(info.Mode().Perm())
			}
			_ = g.send(frame{Type: "file", Path: rel, Clock: st.Clock, Mode: mode, Content: data, Digest: st.Digest})
		}
	}
	g.saveIndex()
}

func (g *goSync) snapshotDigests() map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]string, len(g.index))
	for rel, st := range g.index {
		out[rel] = stateSig(st)
	}
	return out
}

func (g *goSync) snapshotStates() map[string]*fileState {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]*fileState, len(g.index))
	for rel, st := range g.index {
		out[rel] = st
	}
	return out
}

func stateSig(st *fileState) string {
	if st.Deleted {
		return "D:" + clockSig(st.Clock)
	}
	return st.Digest + ":" + clockSig(st.Clock)
}

func clockSig(c VectorClock) string {
	b, _ := json.Marshal(c)
	return string(b)
}

// --- sidecar persistence ---

func (g *goSync) indexPath() string {
	return filepath.Join(g.cfg.Dir, sidecarDir, sidecarFile)
}

func (g *goSync) loadIndex() map[string]*fileState {
	out := map[string]*fileState{}
	data, err := os.ReadFile(g.indexPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func (g *goSync) saveIndex() {
	g.mu.Lock()
	data, _ := json.MarshalIndent(g.index, "", "  ")
	g.mu.Unlock()
	dir := filepath.Join(g.cfg.Dir, sidecarDir)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(g.indexPath(), data, 0o644)
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
