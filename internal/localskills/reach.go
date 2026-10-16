package localskills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ExpandHome turns a leading "~" into the home folder.
func ExpandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

// ReachPaths are the paths a skill can be reached by: its own folder, and the
// entry named after it in each agent folder that links to it (an agent
// resolves a skill through its own folder, so a command it runs names that
// path). Folders that do not exist, entries that are some other skill of the
// same name, and repeats are left out.
func ReachPaths(sk Skill, agentDirs []string, home string) []string {
	out := []string{sk.Dir}
	real, err := filepath.EvalSymlinks(sk.Dir)
	if err != nil {
		return out
	}
	for _, dir := range agentDirs {
		entry := filepath.Join(ExpandHome(dir, home), sk.Name)
		if slices.Contains(out, entry) {
			continue
		}
		if _, err := os.Lstat(entry); err != nil {
			continue
		}
		if got, err := filepath.EvalSymlinks(entry); err == nil && got == real {
			out = append(out, entry)
		}
	}
	return out
}
