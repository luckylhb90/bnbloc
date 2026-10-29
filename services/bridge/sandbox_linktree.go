package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// A per-space mount (component.Mount.PerSpace): a guest path belongs to one
// space of the machine, so a folder every room needs gets one tree per space.
// The tree is made of hard links to the component's files: it costs no disk,
// works on any file system the share lives on, and the files stay in place for a
// running process when a newer version replaces them.

const (
	// spacesDir holds the per-space trees inside the component's folder:
	// <component folder>/.spaces/<space>/<slot>-<source>, one slot per guest path
	// a component mounts and one folder per source version. The source is in the
	// name, so a new version is a new host path: the sandbox converges the space's
	// mount to it instead of keeping a bind mount of a folder that was replaced.
	spacesDir = ".spaces"
	// maxLinkTreeEntries bounds the work and the inodes of one tree.
	maxLinkTreeEntries = 200_000
	// spaceTreeIdleAge is how long a space's trees may sit unused before a sweep
	// that finds no such space removes them (a space being created has its trees
	// before the sandbox lists it).
	spaceTreeIdleAge = time.Hour
	// spaceTreeSweepEvery bounds how often the bridge asks the sandbox for its
	// spaces to sweep.
	spaceTreeSweepEvery = 30 * time.Minute
)

// isPerSpaceTree reports a host path that is one of the per-space trees.
func isPerSpaceTree(hostPath string) bool {
	return strings.Contains(hostPath, string(filepath.Separator)+spacesDir+string(filepath.Separator))
}

// perSpaceTree makes (or keeps) the space's hard-linked tree of src for one
// slot and returns its path. src is a folder under hostRoot that a publisher
// does not change in place (a new version is a new folder), so a tree made of src
// is current and is reused untouched. But a folder can be replaced under the same
// path (the same version removed and installed again with other files), and a
// tree made of the folder before would then serve the old files for good: the
// source's identity (see sourceStamp) is part of the tree's name, so a replaced
// folder gets a new tree. A tree of another source of the same slot (the
// version before) is removed once the new one is in place: a space has no
// process left from before when it is prepared (the turn fence), so nothing runs
// from it.
func perSpaceTree(hostRoot, space, slot, src string) (string, error) {
	if rel, err := filepath.Rel(hostRoot, src); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is not inside %s", src, hostRoot)
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	slotSum, srcSum := sha256.Sum256([]byte(slot)), sha256.Sum256([]byte(sourceStamp(src, st)))
	prefix := hex.EncodeToString(slotSum[:6]) + "-"
	dir := filepath.Join(hostRoot, spacesDir, space)
	dst := filepath.Join(dir, prefix+hex.EncodeToString(srcSum[:6])+"-"+filepath.Base(slot))
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		_ = os.Chtimes(dst, time.Now(), time.Now()) // in use: a sweep leaves it
		_ = os.Chtimes(dir, time.Now(), time.Now())
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	next := dst + ".new"
	_ = os.RemoveAll(next)
	if err := linkTree(src, next); err != nil {
		_ = os.RemoveAll(next)
		return "", err
	}
	if err := os.Rename(next, dst); err != nil {
		_ = os.RemoveAll(next)
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if full := filepath.Join(dir, e.Name()); full != dst && strings.HasPrefix(e.Name(), prefix) {
			_ = os.RemoveAll(full)
		}
	}
	_ = os.Chtimes(dir, time.Now(), time.Now())
	return dst, nil
}

// sourceStamp names a source folder for a tree: its path and the moment its
// own entries last changed. A folder installed again is a new folder with new
// entries, so its stamp differs even at the same path; one nobody touches keeps
// its stamp, so its trees are reused.
func sourceStamp(src string, st fs.FileInfo) string {
	return fmt.Sprintf("%s\x00%d", src, st.ModTime().UnixNano())
}

// sweepSpaceTrees removes the trees of spaces that no longer exist: under every
// component's folder, <component>/.spaces/<space> for a space not in live that
// nothing has touched for idle. A tree costs no disk of its own, but it holds the
// inodes of the versions it linked, so a deleted room would otherwise keep an
// old agent's files for good.
func sweepSpaceTrees(mountsRoot string, live map[string]bool, idle time.Duration, now time.Time) (removed int) {
	comps, _ := os.ReadDir(mountsRoot)
	for _, c := range comps {
		base := filepath.Join(mountsRoot, c.Name(), spacesDir)
		spaces, _ := os.ReadDir(base)
		for _, sp := range spaces {
			if live[sp.Name()] {
				continue
			}
			info, err := sp.Info()
			if err != nil || now.Sub(info.ModTime()) < idle {
				continue
			}
			if os.RemoveAll(filepath.Join(base, sp.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

// linkTree copies the folder src to dst as hard links: folders are made, files
// are linked, links are recreated as links (never followed), and anything else
// (a device, a socket) is refused.
func linkTree(src, dst string) error {
	n := 0
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n++; n > maxLinkTreeEntries {
			return fmt.Errorf("more than %d entries", maxLinkTreeEntries)
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		switch typ := d.Type(); {
		case typ.IsDir():
			return os.MkdirAll(to, 0o755)
		case typ.IsRegular():
			return os.Link(p, to)
		case typ&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		default:
			return fmt.Errorf("%s is not a file, a folder or a link", p)
		}
	})
}

// sweepTrees removes the per-space trees of spaces the sandbox no longer has,
// at most every spaceTreeSweepEvery. It lists the spaces of every machine the
// bridge uses and does nothing unless every list was read: a tree is never
// removed on a guess.
func (s *sandboxRuntime) sweepTrees(ctx context.Context) {
	if s.cfg.MountsDir == "" || s.cfg.WorkspacesRoot == "" {
		return
	}
	s.mu.Lock()
	due := s.now().Sub(s.treesSweptAt) >= spaceTreeSweepEvery
	if due {
		s.treesSweptAt = s.now()
	}
	s.mu.Unlock()
	if !due {
		return
	}
	live := map[string]bool{}
	for _, machine := range []string{s.cfg.Machine, s.cfg.OwnMachine} {
		if machine == "" {
			continue
		}
		spaces, err := s.client.ListSpaces(ctx, s.endpoint(), machine, sandbox.Labels{})
		if err != nil {
			if sandbox.ErrorCodeOf(err) == sandbox.ErrorMachineNotFound {
				continue // a machine that does not exist holds no space
			}
			return
		}
		for _, sp := range spaces {
			live[sp.Name] = true
		}
	}
	if n := sweepSpaceTrees(s.cfg.mountsRoot(), live, spaceTreeIdleAge, s.now()); n > 0 {
		slog.Info("bnbloc bridge sandbox trees of deleted spaces removed", "event", "bnbloc.bridge.sandbox_trees_swept", "count", n)
	}
}
