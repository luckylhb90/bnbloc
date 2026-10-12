package tarbundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
)

// junkNames are the OS, VCS and dependency metadata names no bundle holds.
var junkNames = map[string]bool{
	"__MACOSX": true, ".DS_Store": true, "Thumbs.db": true, "desktop.ini": true,
	".git": true, ".gitignore": true, ".gitattributes": true, ".svn": true, ".hg": true,
	"node_modules": true,
}

// Junk reports a path with an OS, VCS or dependency metadata segment
// (__MACOSX, .DS_Store, an AppleDouble "._" file, .git, node_modules, …).
// Reading an upload and packing a directory both drop it, so a stray
// .DS_Store never fails a pack or ends up in a bundle.
func Junk(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if junkNames[seg] || strings.HasPrefix(seg, "._") {
			return true
		}
	}
	return false
}

// Ignored reports a path a directory packer never looks at: Junk, or a
// hidden (dot) segment: editor and tool state, local secrets (.env), the
// agent config trees.
func Ignored(p string) bool {
	if Junk(p) {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// Entry is one file of a directory being packed.
type Entry struct {
	// Rel is the file's bundle path: relative to the packed directory.
	Rel string
	// Path is the file's workspace-relative path, which the workspace path
	// policy (core.ProtectedWorkspacePath) is judged on; Rel when the
	// packed directory is the root.
	Path string
	Size int64
	Read func() ([]byte, error)
}

// Packed reports whether a directory packer packs e: never an Ignored path
// of the packed directory, never a protected workspace path (room
// history, the agent config bnbloc writes, project config a harness
// executes on the host): a bundle is shared with other people.
func (e Entry) Packed() bool {
	return !Ignored(e.Rel) && !core.ProtectedWorkspacePath(e.Path)
}

// PackEntries is the one directory packer (a workspace draft, a local
// previewer directory, an embedded built-in package): it packs every Packed
// entry into a deterministic bundle within l and reports how many files it
// packed. An entry whose path the bundle rules refuse, or the caps, fail
// the pack before the files past the cap are read.
func PackEntries(entries []Entry, l Limits) ([]byte, int, error) {
	files := map[string][]byte{}
	total := int64(0)
	for _, e := range entries {
		if !e.Packed() {
			continue
		}
		if !core.ValidBundlePath(e.Rel) {
			return nil, 0, fmt.Errorf("%s is not a valid bundle path ([A-Za-z0-9._-] segments)", e.Rel)
		}
		if len(files) >= l.MaxFiles {
			return nil, 0, fmt.Errorf("more than %d files", l.MaxFiles)
		}
		if total += e.Size; total > int64(l.MaxBytes) {
			return nil, 0, fmt.Errorf("larger than %d bytes", l.MaxBytes)
		}
		data, err := e.Read()
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", e.Rel, err)
		}
		files[e.Rel] = data
	}
	if len(files) == 0 {
		return nil, 0, errors.New("no files to pack")
	}
	raw, err := PackFiles(files, l)
	if err != nil {
		return nil, 0, err
	}
	return raw, len(files), nil
}

// PackFS packs the tree under root of fsys (PackEntries; paths are judged
// relative to root). A symlink or other non-regular file that would be
// packed fails the pack.
func PackFS(fsys fs.FS, root string, l Limits) ([]byte, error) {
	var entries []Entry
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		if root == "." {
			rel = p
		}
		if rel == "." || rel == "" {
			return nil
		}
		e := Entry{Rel: rel, Path: rel}
		if d.IsDir() {
			if Ignored(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !e.Packed() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		full := p
		e.Size = info.Size()
		e.Read = func() ([]byte, error) { return fs.ReadFile(fsys, full) }
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	raw, _, err := PackEntries(entries, l)
	return raw, err
}

// Pack packs the local directory dir (PackFS).
func Pack(dir string, l Limits) ([]byte, error) {
	raw, err := PackFS(os.DirFS(dir), ".", l)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return raw, nil
}
