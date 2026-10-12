package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Satisfies reports whether version v is admitted by a requirement range:
//
//   - "" or "*" or "latest": any release (no prerelease);
//   - "1.2.3": exactly that version;
//   - "^1.2.3" / "^1" / "^1.2": same major (for 0.x: same minor), >= the base;
//   - "~1.2.3" / "~1.2": same major.minor, >= the base;
//   - ">=1.2.3": at least the base.
//
// A prerelease version satisfies only an exact match.
func Satisfies(v, rng string) (bool, error) {
	pv, err := Parse(v)
	if err != nil {
		return false, err
	}
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" || rng == "latest" {
		return len(pv.Pre) == 0, nil
	}
	op := ""
	for _, p := range []string{">=", "^", "~", "="} {
		if strings.HasPrefix(rng, p) {
			op, rng = p, strings.TrimSpace(strings.TrimPrefix(rng, p))
			break
		}
	}
	base, parts, err := parsePartial(rng)
	if err != nil {
		return false, err
	}
	if op == "" || op == "=" {
		if parts < 3 {
			op = "~"
			if parts == 1 {
				op = "^"
			}
		} else {
			c, err := Compare(v, rng)
			return c == 0, err
		}
	}
	if len(pv.Pre) > 0 {
		return false, nil
	}
	if cmpCore(pv, base) < 0 {
		return false, nil
	}
	switch op {
	case ">=":
		return true, nil
	case "^":
		if base.Major > 0 || parts == 1 {
			return pv.Major == base.Major, nil
		}
		return pv.Major == 0 && pv.Minor == base.Minor, nil
	case "~":
		if parts == 1 {
			return pv.Major == base.Major, nil
		}
		return pv.Major == base.Major && pv.Minor == base.Minor, nil
	}
	return false, fmt.Errorf("unsupported range %q", rng)
}

// ValidRange reports whether rng is a range Satisfies understands.
func ValidRange(rng string) bool {
	_, err := Satisfies("0.0.0", rng)
	return err == nil
}

// BumpPatch returns v with its patch number incremented (prerelease and
// build dropped).
func BumpPatch(v string) (string, error) {
	pv, err := Parse(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d", pv.Major, pv.Minor, pv.Patch+1), nil
}

// parsePartial parses "1", "1.2" or "1.2.3" (missing parts are zero) and
// reports how many parts were given.
func parsePartial(s string) (Version, int, error) {
	if v, err := Parse(s); err == nil && !strings.HasPrefix(s, "v") {
		return v, 3, nil
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, 0, fmt.Errorf("invalid range version %q", s)
	}
	if len(parts) == 3 {
		v, err := Parse(s)
		return v, 3, err
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, 0, fmt.Errorf("invalid range version %q", s)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, len(parts), nil
}

func cmpCore(a, b Version) int {
	if a.Major != b.Major {
		return cmpInt(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return cmpInt(a.Minor, b.Minor)
	}
	return cmpInt(a.Patch, b.Patch)
}
