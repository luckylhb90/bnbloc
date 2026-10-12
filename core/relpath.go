package core

import (
	"path"
	"strings"
)

// CleanRelPath is the one check of a relative path that someone else
// supplies (a sync peer, an agent, an archive entry, a package) before it is
// joined to a directory. "\" is read as "/" (a Windows peer), and the path
// must not be empty, absolute (a leading "/", a Windows drive "C:" or a
// "\\host\share" volume) or hold a ".." segment anywhere, whatever it would
// clean to. It returns the cleaned slash path ("./a//b" is "a/b"); a caller
// that accepts only a path already in that form compares the result with
// its input (TestCleanRelPathSingleOwner forbids a local copy).
func CleanRelPath(rel string) (string, bool) {
	unified := strings.ReplaceAll(rel, `\`, "/")
	if unified == "" || strings.HasPrefix(unified, "/") || hasDriveLetter(unified) {
		return "", false
	}
	for _, seg := range strings.Split(unified, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := path.Clean(unified)
	if clean == "." {
		return "", false
	}
	return clean, true
}

// hasDriveLetter reports a Windows drive designator ("C:"), refused on any
// host OS so a Windows peer cannot escape a POSIX one either.
func hasDriveLetter(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
