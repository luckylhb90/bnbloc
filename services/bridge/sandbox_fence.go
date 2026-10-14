package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// The sandbox turn fence: the shared workdir's protected paths between
// sandboxed turns.
//
// A sandboxed agent's CLI runs on the host under bnbloc-vmexec's
// confinement, which write-denies the protected workspace paths
// (core.ProtectedWorkspacePath: the config bnbloc writes for the agent and
// the project config a harness executes on the host). The commands the CLI
// runs execute in the room's space of the sandbox VM, though, and the VM
// shares the workdir directly: a guest process writes those paths with no
// host confinement in the way, and the host-side harness acts on them at
// its next start (hooks in .claude/settings*.json, MCP servers in
// .mcp.json, opencode plugins, .codex/config.toml all run on the host).
//
// The fence closes that at the turn boundaries, under the drive lock:
//
//  1. Quiesce. After every sandboxed turn, whatever its outcome, the bridge
//     deletes the space without purge: the Sandbox Protocol kills every
//     process of a deleted space and revokes its exec tokens, and keeps its
//     home and guest-local disks for the next ensure (prepare re-ensures
//     the space every turn). Between turns no guest process can touch the
//     workdir, and one can start again only when the next turn's harness,
//     already running, executes a command with the new turn's token. A
//     space the bridge has not quiesced itself (its first use after a
//     bridge start, a failed quiesce) is quiesced before it is ensured; if
//     that fails the drive fails, it never proceeds.
//  2. Verify. The protected paths' fingerprint (protectedFingerprint) is the
//     session's baseline, taken while every write there is bnbloc's own:
//     right before a follow-up turn's input reaches the core (the space is
//     quiesced and only re-ensured since), and right after a fresh
//     session's create returns. After the quiesce at the end of a turn, and
//     again before a session is reused, the fingerprint must match it.
//  3. Restore. A mismatch means a guest process wrote what the host would
//     read: the session binding is dropped and every protected path is
//     removed (resetProtectedPaths), so the next turn starts a fresh
//     session whose config bnbloc writes from scratch.
//
// Why not mount those paths read-only in the guest instead: runtimes refuse
// overlapping space mounts, and on a case-insensitive host file system a
// read-only bind of one file is bypassed through another spelling of its
// name (".MCP.json" reaches the same inode through the writable parent
// mount). The fence needs nothing of the runtime beyond the protocol's
// space deletion, and it judges paths by the case-folding policy the host
// harnesses are exposed to.
//
// Residual: a fresh session's baseline is taken when its create returns, and
// the harness it started could in principle run a guest command before that
// (it needs a model round trip first). A harness that re-reads its config
// in the middle of a turn sees a guest write of that same turn; the known
// harnesses load hooks and MCP servers at start.

// Fence bounds: a protected tree beyond them is not trusted (reset).
const (
	maxFenceEntries = 20000
	maxFenceBytes   = 512 << 20
	quiesceTimeout  = 30 * time.Second
)

var errFenceTooLarge = errors.New("the protected workspace paths exceed the fence bounds")

// walkProtected calls fn for every protected entry of workdir (a protected
// directory, then everything below it unless fn skips it), in lexical
// order. It descends only into directories that are protected or may hold a
// protected path, so an ordinary project tree is not walked. Symlinks are
// visited, never followed.
func walkProtected(workdir string, fn func(rel, path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(workdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workdir {
			return nil
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if corepkg.ProtectedWorkspacePath(rel) {
			return fn(rel, path, d)
		}
		if d.IsDir() && !corepkg.MayHoldProtectedPath(rel) {
			return fs.SkipDir
		}
		return nil
	})
}

// protectedFingerprint digests every protected entry of a workdir: its path
// as it is spelled on disk, its kind and mode, a file's content and a
// symlink's target. Two equal fingerprints mean nothing there changed.
func protectedFingerprint(workdir string) (string, error) {
	h := sha256.New()
	entries, total := 0, int64(0)
	err := walkProtected(workdir, func(rel, path string, d fs.DirEntry) error {
		if entries++; entries > maxFenceEntries {
			return errFenceTooLarge
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "L %q %q\n", rel, target)
		case mode.IsDir():
			fmt.Fprintf(h, "D %q %o\n", rel, mode.Perm())
		case mode.IsRegular():
			if total += info.Size(); total > maxFenceBytes {
				return errFenceTooLarge
			}
			sum, err := fileSHA256(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "F %q %o %s\n", rel, mode.Perm(), sum)
		default:
			fmt.Fprintf(h, "O %q %s\n", rel, mode.Type())
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// resetProtectedPaths removes every protected entry from a sandbox workdir:
// before a fresh session is created in it (the workdir outlives sessions; a
// profile's files and the room context are re-materialized from scratch),
// and when the fence finds a guest wrote there. Entries are matched by the
// case-folding policy, so another spelling of a name goes too.
func resetProtectedPaths(workdir string) {
	if strings.TrimSpace(workdir) == "" {
		return
	}
	err := walkProtected(workdir, func(_, path string, d fs.DirEntry) error {
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("bnbloc bridge protected path reset failed",
				"event", "bnbloc.bridge.protected_reset_failed", "path", path, "error", err)
		}
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("bnbloc bridge protected path reset incomplete",
			"event", "bnbloc.bridge.protected_reset_failed", "workdir", workdir, "error", err)
	}
}

// protectedDenyWrite lists a workdir's protected paths for the host
// confinement's DenyWrite: the agent must not edit the role it wears or the
// room context, nor plant project config a harness would execute on the
// host, nor create any of them. A directory keeps its trailing "/" (the
// DenyWrite contract), so confinement can protect it before it exists.
func protectedDenyWrite(workdir string) []string {
	paths := corepkg.ProtectedWorkspacePaths()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		path := filepath.Join(workdir, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		if strings.HasSuffix(p, "/") {
			path += "/"
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// fenceBaseline records a sandboxed session's protected-path fingerprint:
// the state its next turns must find. An unreadable tree records no
// baseline, so the turn's end resets it.
func (rs *roomSession) fenceBaseline() {
	if rs.workdir == "" {
		return
	}
	sum, err := protectedFingerprint(rs.workdir)
	if err != nil {
		slog.Warn("bnbloc bridge sandbox fence baseline unreadable",
			"event", "bnbloc.bridge.sandbox_fence_unreadable", "workdir", rs.workdir, "error", err)
		sum = ""
	}
	sh := rs.shared()
	sh.mu.Lock()
	sh.fence = sum
	sh.mu.Unlock()
}

// fenceIntact reports whether a sandboxed session's protected paths are
// still exactly its baseline (a host session always is).
func (rs *roomSession) fenceIntact() bool {
	if rs.workdir == "" {
		return true
	}
	sh := rs.shared()
	sh.mu.Lock()
	want := sh.fence
	sh.mu.Unlock()
	sum, err := protectedFingerprint(rs.workdir)
	return err == nil && want != "" && sum == want
}

// endSandboxedTurn is the fence at the end of a sandboxed turn (whatever its
// outcome): quiesce the space, then verify the session's protected paths;
// a changed tree drops the session and is reset. rs is the session the turn
// ran in (nil when none was bound).
func (b *Bridge) endSandboxedTurn(ctx context.Context, key driveKey, sx corepkg.SandboxExecution, rs *roomSession) {
	qctx, cancel := context.WithTimeout(b.detached(ctx), quiesceTimeout)
	defer cancel()
	if err := b.sandbox.quiesce(qctx, sx.Space); err != nil {
		// The next prepare quiesces before it re-ensures the space, or fails
		// the drive; the reuse check below it catches anything written
		// meanwhile.
		slog.Warn("bnbloc bridge sandbox space not quiesced after a turn",
			"event", "bnbloc.bridge.sandbox_quiesce_failed", "space", sx.Space, "error", err)
	}
	if rs == nil || rs.fenceIntact() {
		return
	}
	slog.Warn("bnbloc bridge sandboxed turn changed protected workspace paths; resetting the session",
		"event", "bnbloc.bridge.sandbox_fence_breached", "room", key.room, "core", key.core, "provider", key.provider, "workdir", sx.Workdir)
	// Every channel session of the workspace was started on the tree the
	// guest changed: none is reused.
	b.mu.Lock()
	b.unbindWorkspaceLocked(key)
	b.mu.Unlock()
	resetProtectedPaths(sx.Workdir)
	b.workspaces.roomFilesCleared(key)
}

// quiesce stops every process of a space: deleting it without purge (the
// protocol kills its processes and revokes its exec tokens; its home and
// guest-local disks stay for the next ensure). A space that does not exist
// has no process.
func (s *sandboxRuntime) quiesce(ctx context.Context, space string) error {
	err := s.client.DeleteSpace(ctx, s.endpoint(), s.cfg.Machine, space, false)
	if err != nil && sandbox.ErrorCodeOf(err) != sandbox.ErrorSpaceNotFound {
		return err
	}
	s.mu.Lock()
	s.quiesced[space] = true
	s.mu.Unlock()
	return nil
}

// takeQuiesced reports whether the bridge quiesced the space since its last
// use, and marks it in use.
func (s *sandboxRuntime) takeQuiesced(space string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.quiesced[space]
	delete(s.quiesced, space)
	return q
}

// removeRoomWorkspace removes a deleted room's sandboxed workspace: its
// space (purged: processes, home, local disks) and the workdir the bridge
// created for it under the workspaces root — never a path outside it.
// Best-effort; a core's own record of the workspace is its deleteWorkspace.
func (s *sandboxRuntime) removeRoomWorkspace(ctx context.Context, k driveKey) error {
	if s == nil || !s.cfg.present() {
		return nil
	}
	space := sandboxSpaceName(k)
	if err := s.client.DeleteSpace(ctx, s.endpoint(), s.cfg.Machine, space, true); err != nil && sandbox.ErrorCodeOf(err) != sandbox.ErrorSpaceNotFound {
		return err
	}
	s.mu.Lock()
	delete(s.quiesced, space)
	s.mu.Unlock()
	return removeWorkdirUnder(s.cfg.WorkspacesRoot, space)
}

// removeWorkdirUnder removes root/name when name is one plain path segment
// (core.CleanRelPath, unchanged and without a separator): never root itself
// nor anything outside it.
func removeWorkdirUnder(root, name string) error {
	if root == "" {
		return nil
	}
	clean, ok := corepkg.CleanRelPath(name)
	if !ok || clean != name || clean == "." || strings.Contains(clean, "/") {
		return fmt.Errorf("refusing to remove %q under %q: not one workspace directory", name, root)
	}
	return os.RemoveAll(filepath.Join(root, clean))
}
