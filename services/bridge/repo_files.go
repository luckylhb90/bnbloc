package bridge

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A working copy's tracked files are its source, never deliverables
// (docs/prd/2026-10-27-repo-files-and-folders.md): an agent that clones a
// repository has not made its files.

// gitListTimeout bounds one working copy's listing: it reads the index,
// nothing of the files.
const gitListTimeout = 30 * time.Second

// trackedFiles reports which of paths (workspace-relative, slash-separated)
// a git working copy inside dir tracks. A working copy is the nearest
// folder of the workspace (the workspace itself included) with a .git
// entry. When git cannot list one, all its paths count as tracked
// (decision 1 of the PRD: an unknown answer must not let a repository in).
func trackedFiles(ctx context.Context, dir string, paths []string) map[string]bool {
	out := map[string]bool{}
	if dir == "" || len(paths) == 0 {
		return out
	}
	roots := map[string]string{} // folder → its working copy ("" none)
	var rootOf func(d string) string
	rootOf = func(d string) string {
		if r, ok := roots[d]; ok {
			return r
		}
		r := ""
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(d), ".git")); err == nil {
			r = d
		} else if d != "." {
			r = rootOf(path.Dir(d))
		}
		roots[d] = r
		return r
	}
	byRoot := map[string][]string{}
	for _, p := range paths {
		if r := rootOf(path.Dir(p)); r != "" {
			byRoot[r] = append(byRoot[r], p)
		}
	}
	for root, ps := range byRoot {
		listed, err := gitTracked(ctx, filepath.Join(dir, filepath.FromSlash(root)))
		if err != nil {
			slog.Warn("bnbloc bridge working copy unlisted: its files are not published", "event", "bnbloc.bridge.repo_list_failed", "root", root, "error", err)
		}
		for _, p := range ps {
			rel := p
			if root != "." {
				rel = strings.TrimPrefix(p, root+"/")
			}
			if err != nil || listed[rel] {
				out[p] = true
			}
		}
	}
	return out
}

// gitTracked lists the files the working copy at root tracks, relative to
// it. The repository is the agent's: nothing in its config may run a
// command (no fsmonitor, no hooks), and its owner may differ from this
// process's (a sandbox's guest user).
func gitTracked(ctx context.Context, root string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "safe.directory=*", "ls-files", "-z")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for f := range bytes.SplitSeq(raw, []byte{0}) {
		if len(f) > 0 {
			out[string(f)] = true
		}
	}
	return out, nil
}
