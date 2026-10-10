package sync

import (
	"path"
	"path/filepath"
	"strings"
)

// safeRel validates a peer-supplied relative path before it is joined to the
// project directory. It rejects absolute paths, Windows volume/drive-absolute
// paths, and any parent traversal ("..") in either separator style, and returns
// the cleaned slash-separated relative path. This is the trust boundary for
// incoming file/delete/request frames: a malicious or buggy peer must not be
// able to write outside the mirrored directory.
func safeRel(rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	// Reject Windows drive/volume-absolute paths ("C:\\...", "\\\\host\\share")
	// even when the bridge runs on POSIX, so a Windows peer cannot escape.
	// filepath.VolumeName is OS-specific (returns "" on POSIX), so detect a
	// drive-letter prefix explicitly in addition.
	if filepath.VolumeName(rel) != "" || hasDriveLetter(rel) {
		return "", false
	}
	unified := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(unified, "/") {
		return "", false
	}
	for _, seg := range strings.Split(unified, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := path.Clean(unified)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// hasDriveLetter reports whether s begins with a Windows drive designator like
// "C:" so peer paths are rejected uniformly on any host OS.
func hasDriveLetter(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
