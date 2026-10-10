package gateway

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type semverVersion struct {
	major int
	minor int
	patch int
	pre   []string
}

func parseSemver(raw string) (semverVersion, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return semverVersion{}, fmt.Errorf("semver required")
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
			if p == "" || !isSemverIdentifier(p) {
				return semverVersion{}, fmt.Errorf("invalid semver prerelease")
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semverVersion{}, fmt.Errorf("semver must be major.minor.patch")
	}
	major, err := parseSemverNumber(parts[0])
	if err != nil {
		return semverVersion{}, err
	}
	minor, err := parseSemverNumber(parts[1])
	if err != nil {
		return semverVersion{}, err
	}
	patch, err := parseSemverNumber(parts[2])
	if err != nil {
		return semverVersion{}, err
	}
	return semverVersion{major: major, minor: minor, patch: patch, pre: pre}, nil
}

func parseSemverNumber(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("semver number required")
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("semver number has leading zero")
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return 0, fmt.Errorf("semver number must be numeric")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("semver number too large: %w", err)
	}
	return n, nil
}

func isSemverIdentifier(s string) bool {
	for _, r := range s {
		if r == '-' || unicode.IsDigit(r) || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			continue
		}
		return false
	}
	return true
}

func compareSemver(a, b string) (int, error) {
	av, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	if av.major != bv.major {
		return cmpInt(av.major, bv.major), nil
	}
	if av.minor != bv.minor {
		return cmpInt(av.minor, bv.minor), nil
	}
	if av.patch != bv.patch {
		return cmpInt(av.patch, bv.patch), nil
	}
	return comparePrerelease(av.pre, bv.pre), nil
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
		ai, aNum := semverIdentifierNumber(a[i])
		bi, bNum := semverIdentifierNumber(b[i])
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

func semverIdentifierNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return 0, false
		}
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
