package component

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
)

// MaxNameLen bounds a content component's or a skill's name.
const MaxNameLen = 63

// The content bundle caps (experts and skill packs,
// docs/prd/2026-10-12-experts-and-teams.md): mostly text, with room for a
// skill's scripts and reference files. A component that hands the library a
// skill packs it within them.
const (
	ContentMaxBytes = 8 << 20
	ContentMaxFiles = 512
)

// The platform's contracts every pull workspace holds as files in its room
// records (docs/prd/2026-10-20-no-built-in-content.md §2.5), written by
// bnbloc and rewritten only when they change. Content that guides an agent
// (a skill, an expert's instructions) refers to them by these paths and
// never copies what they say, so nothing drifts from the platform.
const (
	// RoomAppSDKFile is the interactive apps' SDK in demo mode: every call
	// answered in the page's own memory, nothing sent (an agent's
	// self-test of an app serves it as the page's ~app/sdk.js).
	RoomAppSDKFile = core.RoomContextDir + "app-sdk/sdk.js"
	// RoomAppSDKTypesFile is the SDK's declarations: every call, its
	// options and the caps of an app's data.
	RoomAppSDKTypesFile = core.RoomContextDir + "app-sdk/sdk.d.ts"
	// RoomContentLimitsFile is the content validator's bounds: what a
	// package may hold, per kind.
	RoomContentLimitsFile = core.RoomContextDir + "content-limits.md"
)

var nameRE = regexp.MustCompile(fmt.Sprintf(`^[a-z0-9][a-z0-9-]{0,%d}$`, MaxNameLen-1))

// ValidName reports whether s is a valid component or skill name: lower-case
// letters, digits and hyphens, starting with a letter or digit, at most
// MaxNameLen.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ParseFrontmatter reads a SKILL.md / agent .md YAML frontmatter block:
// "key: value" lines between a leading "---" and the next "---", with
// quoted values and ">"/"|" block scalars. It returns the keys and the body
// after the block (the whole text when there is none).
func ParseFrontmatter(text string) (map[string]string, string) {
	out := map[string]string{}
	t := strings.TrimPrefix(text, "\ufeff")
	t = strings.ReplaceAll(t, "\r\n", "\n")
	if !strings.HasPrefix(t, "---\n") {
		return out, text
	}
	rest := t[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out, text
	}
	block := rest[:end]
	body := strings.TrimPrefix(rest[end+4:], "\n")
	lines := strings.Split(block, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, ">") || strings.HasPrefix(val, "|") {
			folded := strings.HasPrefix(val, ">")
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				parts = append(parts, strings.TrimSpace(lines[i]))
			}
			sep := "\n"
			if folded {
				sep = " "
			}
			val = strings.TrimSpace(strings.Join(parts, sep))
		} else if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
			if strings.HasPrefix(line[strings.Index(line, ":")+1:], " \"") {
				val = strings.ReplaceAll(val, `\"`, `"`)
			}
		}
		if utf8.ValidString(val) {
			out[key] = val
		}
	}
	return out, body
}
