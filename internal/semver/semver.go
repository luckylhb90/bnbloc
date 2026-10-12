// Package semver is the one semantic-version parser and comparer: desktop
// release ordering and update policy (gateway) and content component
// versions and requirement ranges (contentpkg) share it.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version (build metadata dropped).
type Version struct {
	Major int
	Minor int
	Patch int
	Pre   []string
}

// Parse parses major.minor.patch[-prerelease][+build] (a leading "v" is
// accepted).
func Parse(raw string) (Version, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return Version{}, fmt.Errorf("semver required")
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core := s
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		pre = strings.Split(s[i+1:], ".")
		for _, p := range pre {
			if p == "" || !isIdentifier(p) {
				return Version{}, fmt.Errorf("invalid semver prerelease")
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("semver must be major.minor.patch")
	}
	major, err := parseNumber(parts[0])
	if err != nil {
		return Version{}, err
	}
	minor, err := parseNumber(parts[1])
	if err != nil {
		return Version{}, err
	}
	patch, err := parseNumber(parts[2])
	if err != nil {
		return Version{}, err
	}
	return Version{Major: major, Minor: minor, Patch: patch, Pre: pre}, nil
}

// Valid reports whether s is a version exactly as a package or component
// declares it: major.minor.patch[-prerelease][+build], with no leading "v"
// and no surrounding space (Parse is lenient about both). Package and
// content component versions share it.
func Valid(s string) bool {
	if s == "" || s != strings.TrimSpace(s) || strings.HasPrefix(s, "v") {
		return false
	}
	if _, build, ok := strings.Cut(s, "+"); ok {
		for _, id := range strings.Split(build, ".") {
			if id == "" || !isIdentifier(id) {
				return false
			}
		}
	}
	_, err := Parse(s)
	return err == nil
}

func parseNumber(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("semver number required")
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("semver number has leading zero")
	}
	for _, r := range s {
		if !('0' <= r && r <= '9') {
			return 0, fmt.Errorf("semver number must be numeric")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("semver number too large: %w", err)
	}
	return n, nil
}

func isIdentifier(s string) bool {
	for _, r := range s {
		if r == '-' || ('0' <= r && r <= '9') || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			continue
		}
		return false
	}
	return true
}

// Compare orders two versions (semver precedence): -1, 0 or 1.
func Compare(a, b string) (int, error) {
	av, err := Parse(a)
	if err != nil {
		return 0, err
	}
	bv, err := Parse(b)
	if err != nil {
		return 0, err
	}
	if av.Major != bv.Major {
		return cmpInt(av.Major, bv.Major), nil
	}
	if av.Minor != bv.Minor {
		return cmpInt(av.Minor, bv.Minor), nil
	}
	if av.Patch != bv.Patch {
		return cmpInt(av.Patch, bv.Patch), nil
	}
	return comparePrerelease(av.Pre, bv.Pre), nil
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		ai, aNum := identifierNumber(a[i])
		bi, bNum := identifierNumber(b[i])
		switch {
		case aNum && bNum && ai != bi:
			return cmpInt(ai, bi)
		case aNum && !bNum:
			return -1
		case !aNum && bNum:
			return 1
		case !aNum && !bNum && a[i] != b[i]:
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return cmpInt(len(a), len(b))
}

func identifierNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9') {
			return 0, false
		}
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
