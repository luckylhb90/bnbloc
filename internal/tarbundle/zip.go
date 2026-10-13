package tarbundle

import (
	"archive/zip"
	"bytes"
	"fmt"
	"sort"

	"github.com/luckylhb90/bnbloc/core"
)

// ZipFiles writes files as a zip archive, sorted by path: a bundle's files
// as a member attaches them (a site work's files, the reference a "Make
// similar" draft attaches when the work has no recipe). Every path must
// be a bundle path.
func ZipFiles(files map[string][]byte) ([]byte, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		if !core.ValidBundlePath(p) {
			return nil, fmt.Errorf("%q is not a bundle path", p)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range paths {
		w, err := zw.Create(p)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[p]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
