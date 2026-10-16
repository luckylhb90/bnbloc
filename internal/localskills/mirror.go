package localskills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Mirror is a folder holding read-only-to-be copies of skills, one folder per
// skill name. It is brought up to date by comparing each file's size and
// modification time (to the nanosecond): an unchanged skill costs a stat per
// file, an edited file is copied again.
type Mirror struct{ Root string }

// Dir is where a skill's copy lives.
func (m Mirror) Dir(name string) string { return filepath.Join(m.Root, name) }

func copyFile(src File, dst string) error {
	in, err := os.Open(src.Abs)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	mode := fs.FileMode(0o644)
	if src.Mode&0o111 != 0 {
		mode = 0o755
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chtimes(tmp.Name(), src.ModTime, src.ModTime); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Sync makes the skill's copy equal to files: new and changed files copied,
// files that are no longer in the skill removed. name must be a valid skill
// name (a single path part); the caller checked it.
func (m Mirror) Sync(name string, files []File) error {
	dest := m.Dir(name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("mirror %s: %w", name, err)
	}
	want := map[string]bool{}
	for _, f := range files {
		want[f.Rel] = true
		dst := filepath.Join(dest, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mirror %s: %w", name, err)
		}
		if st, err := os.Stat(dst); err == nil && st.Size() == f.Size && st.ModTime().UnixNano() == f.ModTime.UnixNano() {
			continue
		}
		if err := copyFile(f, dst); err != nil {
			return fmt.Errorf("mirror %s/%s: %w", name, f.Rel, err)
		}
	}
	var stale []string
	_ = filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dest {
			return nil
		}
		rel := filepath.ToSlash(relOf(dest, p))
		if d.IsDir() {
			stale = append(stale, p)
			return nil
		}
		if !want[rel] {
			os.Remove(p)
		}
		return nil
	})
	// Directories no file needs any more, deepest first.
	sort.Slice(stale, func(i, j int) bool { return len(stale[i]) > len(stale[j]) })
	for _, d := range stale {
		_ = os.Remove(d) // fails (and stays) when it still holds files
	}
	return nil
}

// Prune removes the copies of every skill not in keep.
func (m Mirror) Prune(keep []string) error {
	list, err := os.ReadDir(m.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for _, e := range list {
		if !kept[e.Name()] {
			if err := os.RemoveAll(filepath.Join(m.Root, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
