package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A per-space mount (component.Mount.PerSpace): a guest path belongs to one
// space of the machine, so a folder every room needs gets one tree per space.
// The tree is made of hard links to the component's files: it costs no disk,
// works on any file system the share lives on, and the files stay in place for a
// running process when a newer version replaces them.

const (
	// spacesDir holds the per-space trees inside the component's folder:
	// <component folder>/.spaces/<space>/<slot>, one slot per guest path a
	// component mounts, so a new version replaces the old tree of its slot.
	spacesDir = ".spaces"
	// stampSuffix names the file beside a tree that says which source it links.
	stampSuffix = ".linked-from"
	// maxLinkTreeEntries bounds the work and the inodes of one tree.
	maxLinkTreeEntries = 200_000
)

// isPerSpaceTree reports a host path that is one of the per-space trees.
func isPerSpaceTree(hostPath string) bool {
	return strings.Contains(hostPath, string(filepath.Separator)+spacesDir+string(filepath.Separator))
}

// perSpaceTree makes (or keeps) the space's hard-linked tree of src for one
// slot and returns its path. src is a folder under hostRoot, immutable once
// published (a new version is a new folder), so a tree that records src as its
// source is current and is reused untouched; one that records another source is
// replaced. A space has no process left from before when it is prepared (the
// turn fence), so nothing runs from the tree that is replaced.
// The replacement is built beside it and swapped in whole, so a failed build
// leaves the previous tree, and the process that ran from it, as it was.
func perSpaceTree(hostRoot, space, slot, src string) (string, error) {
	if rel, err := filepath.Rel(hostRoot, src); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is not inside %s", src, hostRoot)
	}
	sum := sha256.Sum256([]byte(slot))
	dst := filepath.Join(hostRoot, spacesDir, space, hex.EncodeToString(sum[:6])+"-"+filepath.Base(slot))
	stamp := dst + stampSuffix
	if got, err := os.ReadFile(stamp); err == nil && string(got) == src {
		if st, err := os.Stat(dst); err == nil && st.IsDir() {
			return dst, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	next := dst + ".new"
	_ = os.RemoveAll(next)
	if err := linkTree(src, next); err != nil {
		_ = os.RemoveAll(next)
		return "", err
	}
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.RemoveAll(next)
		return "", err
	}
	if err := os.Rename(next, dst); err != nil {
		_ = os.Rename(old, dst)
		return "", err
	}
	_ = os.RemoveAll(old)
	if err := os.WriteFile(stamp, []byte(src), 0o644); err != nil {
		return "", err
	}
	return dst, nil
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
