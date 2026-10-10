package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

// confineSpec is the host sandbox scope of one agent CLI process (and the
// hooks and MCP servers it spawns): what it may read and write.
//
//   - Reads are "allow by default, deny what matters": the CONTENT of
//     credential stores, personal folders and every visible (non-dot) home
//     directory — i.e. the member's other projects —, the desktop app's own
//     data, other room workspaces (siblings under the workspaces root) and the
//     state of harnesses that are not part of this run is denied; metadata
//     stays readable so path traversal works (macOS: deny file-read-data, not
//     file-read*). The workspace, the CLI install dir, the listed harnesses'
//     trees, installed plugin/skill sources, vmexec and allowRead are
//     re-allowed on top (an allow of a sub-path wins over the deny of its
//     ancestor).
//   - Writes are "deny by default": only the workspace, temp dirs, the listed
//     harnesses' STATE (explicit sub-directories and files: transcripts,
//     sessions, logs, caches, token refresh) and allowWrite are writable.
//     Everything that executes on the host later — settings, hooks, plugins,
//     skills, agents, commands, MCP config, user-level CLAUDE.md/AGENTS.md,
//     plugin package caches — stays read-only, so a borrowed prompt cannot
//     plant an escape that runs unsandboxed in the owner's next own session.
//     DenyWrite (core-declared, e.g. routing config in the workdir) wins over
//     every allow.
//   - Network: allowed (the CLI talks to its model API from the host).
type confineSpec struct {
	Home       string
	Workspace  string
	Harnesses  []string // the CLI's harness first, then harnesses its plugins use
	CLIPath    string   // resolved executable of the confined CLI
	VMExecDir  string
	AllowRead  []string
	AllowWrite []string
	// AllowWritePrefix are absolute path prefixes (not directories) the CLI
	// may create/write, for temp-then-rename files beside a read-only config
	// (e.g. "<dir>/.state-" for "<dir>/.state-<random>"). Seatbelt only:
	// bubblewrap binds existing paths, so on Linux the prefix covers files
	// that already exist when the CLI starts.
	AllowWritePrefix []string
	DenyRead         []string
	DenyWrite        []string
	DenyKeychain     bool
	TempDirs         []string

	layouts    []harnessLayout // listed harnesses (resolve)
	others     []harnessLayout // known harnesses not listed: denied
	pluginDirs []string
}

// harnessLayout is one CLI's documented on-disk layout (a property of the CLI,
// not of a deployment; relocation env vars are honored).
type harnessLayout struct {
	name string
	// roots are the CLI's trees (config + state), readable when listed and
	// read-denied when not; rootFiles are its top-level state/config files
	// outside those trees (regexes).
	roots     []string
	rootFiles []string
	// stateDirs / stateFiles (regexes) are what the CLI writes while running.
	stateDirs  []string
	stateFiles []string
	// readOnly are executable-bearing paths INSIDE a state dir (plugin
	// package caches, runtimes) that stay read-only.
	readOnly []string
}

func prefixRegex(path string) string { return "^" + regexp.QuoteMeta(path) }

func harnessLayoutFor(name, home string) (harnessLayout, bool) {
	h := func(rel string) string { return filepath.Join(home, rel) }
	switch name {
	case "claude":
		c := firstNonEmptyStr(os.Getenv("CLAUDE_CONFIG_DIR"), h(".claude"))
		in := func(rels ...string) []string {
			out := make([]string, 0, len(rels))
			for _, r := range rels {
				out = append(out, filepath.Join(c, r))
			}
			return out
		}
		// ~/.claude.json is readable but NOT writable: besides UI state it
		// holds the user/project MCP server config (executable on the host);
		// Claude runs fine without persisting it.
		var files []string
		for _, f := range []string{"history.jsonl", "stats-cache.json", "mcp-needs-auth-cache.json", ".last-cleanup", ".last-update-result.json", "jobs.json", ".credentials.json"} {
			files = append(files, prefixRegex(filepath.Join(c, f)))
		}
		return harnessLayout{
			name:      name,
			roots:     []string{c, h(".local/share/claude"), h(".local/state/claude"), h(".cache/claude"), h("Library/Caches/claude-cli-nodejs")},
			rootFiles: []string{prefixRegex(h(".claude.json"))},
			stateDirs: append(in("projects", "transcripts", "todos", "shell-snapshots", "statsig", "session-env", "sessions",
				"file-history", "paste-cache", "tasks", "telemetry", "debug", "backups", "cache", "downloads", "ide", "teams", "plans", "plugins/data"),
				h(".local/state/claude"), h(".cache/claude"), h("Library/Caches/claude-cli-nodejs")),
			stateFiles: files,
		}, true
	case "codex":
		x := firstNonEmptyStr(os.Getenv("CODEX_HOME"), h(".codex"))
		in := func(rels ...string) []string {
			out := make([]string, 0, len(rels))
			for _, r := range rels {
				out = append(out, filepath.Join(x, r))
			}
			return out
		}
		files := []string{prefixRegex(x) + `/[a-z_]+_[0-9]+\.sqlite`}
		for _, f := range []string{"history.jsonl", "session_index.jsonl", "transcription-history.jsonl", "version.json", "installation_id",
			"auth.json", ".codex-global-state.json", ".models_cache", "models_cache.json", ".sqlite-maintenance.lock"} {
			files = append(files, prefixRegex(filepath.Join(x, f)))
		}
		return harnessLayout{
			name:  name,
			roots: []string{x, h(".cache/codex-runtimes")},
			stateDirs: in("sessions", "archived_sessions", "log", ".tmp", "tmp", "cache", "shell_snapshots", "sqlite", "memories",
				"app-server-control", "app-server-daemon", "ipc", "mcp-oauth-locks", "project-metadata-locks", "thread-writer-locks",
				"rollout-migrations", "visualizations"),
			stateFiles: files,
		}, true
	case "opencode":
		cache := h(".cache/opencode")
		return harnessLayout{
			name:      name,
			roots:     []string{h(".config/opencode"), h(".local/share/opencode"), h(".local/state/opencode"), cache},
			stateDirs: []string{h(".local/share/opencode"), h(".local/state/opencode"), cache},
			readOnly:  []string{filepath.Join(cache, "packages"), filepath.Join(cache, "bin"), filepath.Join(cache, "node_modules")},
		}, true
	}
	return harnessLayout{}, false
}

var knownHarnesses = []string{"claude", "codex", "opencode"}

// harnessFor derives the harness from the CLI's executable name.
func harnessFor(argv0 string) string {
	base := strings.ToLower(trimExe(filepath.Base(argv0)))
	for _, h := range knownHarnesses {
		if base == h || strings.HasPrefix(base, h+"-") {
			return h
		}
	}
	return "generic"
}

// sensitiveHomePaths are read-denied (content) for every confined CLI: SSH,
// cloud and cluster credentials, git/registry tokens, the login keychain
// files, browser profiles and mail, and shell startup and history files —
// they routinely export API keys (2026-10-09: a sandboxed agent read
// LITELLM_API_KEY from ~/.zshrc). The CLI gets its environment from its
// launcher, and a shell spawned confined skips an unreadable startup file
// silently.
var sensitiveHomePaths = []string{
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout", ".zsh_history", ".zsh_sessions",
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile", ".bash_history", ".bash_sessions",
	".config/fish", ".env", ".envrc",
	".ssh", ".aws", ".azure", ".config/gcloud", ".kube", ".gnupg", ".docker", ".config/gh",
	".netrc", ".git-credentials", ".npmrc", ".pypirc", ".terraform.d", ".vault-token",
	"Library/Keychains", "Library/Cookies", "Library/Mail", "Library/Messages", "Library/Safari",
	"Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
	"Library/Application Support/BraveSoftware", "Library/Application Support/Microsoft Edge",
	".mozilla", ".config/google-chrome", ".config/chromium",
	// The bnbloc desktop's own data (settings, session material, component
	// artifacts): identifier from apps/desktop/src-tauri/tauri.conf.json.
	"Library/Application Support/sh.bnbloc.desktop", ".local/share/sh.bnbloc.desktop", ".config/sh.bnbloc.desktop",
}

// resolve fills the derived lists: listed and unlisted harness layouts and
// the plugin/skill sources of the listed ones.
func (s *confineSpec) resolve() {
	s.layouts, s.others = nil, nil
	listed := map[string]bool{}
	for _, name := range s.Harnesses {
		if l, ok := harnessLayoutFor(name, s.Home); ok && !listed[name] {
			s.layouts = append(s.layouts, l)
			s.pluginDirs = append(s.pluginDirs, pluginSources(name, l.roots)...)
		}
		listed[name] = true
	}
	for _, name := range knownHarnesses {
		if l, ok := harnessLayoutFor(name, s.Home); ok && !listed[name] {
			s.others = append(s.others, l)
		}
	}
}

// pluginSources returns the directories the CLI loads plugins and skills
// from that may live OUTSIDE its config dir: Claude's installed plugin
// install paths and marketplace locations (incl. local directory sources),
// and the resolved targets of symlinked skills/commands/agents. They become
// readable, never writable.
func pluginSources(harness string, roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	cfg := roots[0]
	var out []string
	if harness == "claude" {
		var installed struct {
			Plugins map[string][]struct {
				InstallPath string `json:"installPath"`
			} `json:"plugins"`
		}
		if raw, err := os.ReadFile(filepath.Join(cfg, "plugins", "installed_plugins.json")); err == nil && json.Unmarshal(raw, &installed) == nil {
			for _, list := range installed.Plugins {
				for _, p := range list {
					out = append(out, p.InstallPath)
				}
			}
		}
		var markets map[string]struct {
			InstallLocation string `json:"installLocation"`
			Source          struct {
				Path string `json:"path"`
			} `json:"source"`
		}
		if raw, err := os.ReadFile(filepath.Join(cfg, "plugins", "known_marketplaces.json")); err == nil && json.Unmarshal(raw, &markets) == nil {
			for _, m := range markets {
				out = append(out, m.InstallLocation, m.Source.Path)
			}
		}
	}
	for _, sub := range []string{"skills", "skill", "commands", "agents", "plugins", "prompts"} {
		entries, err := os.ReadDir(filepath.Join(cfg, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if target, err := filepath.EvalSymlinks(filepath.Join(cfg, sub, e.Name())); err == nil {
				if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
					target = filepath.Dir(target)
				}
				out = append(out, target)
			}
		}
	}
	var abs []string
	for _, p := range out {
		if filepath.IsAbs(p) {
			abs = append(abs, p)
		}
	}
	return abs
}

// canonical resolves symlinks of the deepest existing ancestor (the sandbox
// matches resolved paths: /var → /private/var on macOS).
func canonical(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, rest := filepath.Dir(p), filepath.Base(p)
	if dir == p {
		return p
	}
	return filepath.Join(canonical(dir), rest)
}

// canonicalRegex canonicalizes the literal path prefix of a prefixRegex.
func canonicalRegex(re string) string {
	lit, rest := literalPrefix(re)
	if lit == "" {
		return re
	}
	return "^" + regexp.QuoteMeta(canonical(lit)) + rest
}

// literalPrefix splits "^<quoted path><rest>" into the unquoted path (up to
// the first regex metacharacter) and the remaining pattern.
func literalPrefix(re string) (string, string) {
	if !strings.HasPrefix(re, "^") {
		return "", re
	}
	var b strings.Builder
	body := re[1:]
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\\' && i+1 < len(body):
			b.WriteByte(body[i+1])
			i++
		case strings.IndexByte(`.+*?()|[]{}^$`, c) >= 0:
			return b.String(), body[i:]
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), ""
}

func uniqCanonical(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if strings.TrimSpace(p) == "" || !filepath.IsAbs(p) {
			continue
		}
		c := canonical(p)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func uniqRegex(res []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range res {
		c := canonicalRegex(r)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// homeContentDirs are the visible (non-dot) top-level home directories —
// documents and every other project the member keeps — except the
// macOS/desktop system folders.
func (s *confineSpec) homeContentDirs() []string {
	entries, err := os.ReadDir(s.Home)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.IsDir() || name == "Library" || name == "Applications" || name == "Public" {
			continue
		}
		out = append(out, filepath.Join(s.Home, name))
	}
	return out
}

// goModCache is the Go module cache (read-only dependency sources a host
// language server needs), re-allowed when it sits in a denied home dir.
func goModCache(home string) string {
	if gp := strings.TrimSpace(os.Getenv("GOMODCACHE")); gp != "" {
		return gp
	}
	gopath := strings.TrimSpace(os.Getenv("GOPATH"))
	if gopath == "" {
		gopath = filepath.Join(home, "go")
	}
	return filepath.Join(strings.Split(gopath, string(os.PathListSeparator))[0], "pkg", "mod")
}

func (s *confineSpec) denyReads() []string {
	deny := make([]string, 0, len(sensitiveHomePaths)+16)
	for _, r := range sensitiveHomePaths {
		deny = append(deny, filepath.Join(s.Home, r))
	}
	for _, l := range s.others {
		deny = append(deny, l.roots...)
	}
	deny = append(deny, s.DenyRead...)
	deny = append(deny, s.homeContentDirs()...)
	// Every other room workspace: the workspace's parent (the workspaces
	// root), unless that is the home directory or the filesystem root.
	if root := filepath.Dir(filepath.Clean(s.Workspace)); root != filepath.Clean(s.Home) && root != "/" && root != "." {
		deny = append(deny, root)
	}
	return uniqCanonical(deny)
}

func (s *confineSpec) denyReadFiles() []string {
	var out []string
	for _, l := range s.others {
		out = append(out, l.rootFiles...)
	}
	return uniqRegex(out)
}

func (s *confineSpec) allowReads() []string {
	allow := []string{s.Workspace, s.VMExecDir, goModCache(s.Home)}
	if s.CLIPath != "" {
		allow = append(allow, filepath.Dir(s.CLIPath))
	}
	for _, l := range s.layouts {
		allow = append(allow, l.roots...)
	}
	allow = append(allow, s.pluginDirs...)
	allow = append(allow, s.AllowRead...)
	allow = append(allow, s.AllowWrite...)
	allow = append(allow, s.TempDirs...)
	return uniqCanonical(allow)
}

func (s *confineSpec) allowReadFiles() []string {
	var out []string
	for _, l := range s.layouts {
		out = append(out, l.rootFiles...)
	}
	return uniqRegex(out)
}

func (s *confineSpec) allowWrites() []string {
	w := []string{s.Workspace}
	for _, l := range s.layouts {
		w = append(w, l.stateDirs...)
	}
	w = append(w, s.AllowWrite...)
	w = append(w, s.TempDirs...)
	return uniqCanonical(w)
}

func (s *confineSpec) allowWriteFiles() []string {
	var out []string
	for _, l := range s.layouts {
		out = append(out, l.stateFiles...)
	}
	for _, p := range s.AllowWritePrefix {
		out = append(out, prefixRegex(canonical(p)))
	}
	return uniqRegex(out)
}

// denyWrites win over every allow: executable-bearing paths inside state
// dirs, and the core-declared DenyWrite.
func (s *confineSpec) denyWrites() []string {
	var d []string
	for _, l := range s.layouts {
		d = append(d, l.readOnly...)
	}
	d = append(d, s.DenyWrite...)
	return uniqCanonical(d)
}

// seatbeltProfile renders the macOS Seatbelt profile. Precedence: a later
// rule wins only for the SAME operation, so every deny/allow pair uses the
// identical operation (file-read-data / file-write*).
func (s *confineSpec) seatbeltProfile() string {
	var b strings.Builder
	b.WriteString("(version 1)\n;; bnbloc-vmexec confine: harnesses=" + strings.Join(s.Harnesses, ",") + "\n(allow default)\n")
	rule := func(action, op string, paths, regexes []string, extra string) {
		if len(paths)+len(regexes) == 0 && extra == "" {
			return
		}
		fmt.Fprintf(&b, "(%s %s\n", action, op)
		for _, p := range paths {
			fmt.Fprintf(&b, "  (subpath %s)\n", sbString(p))
		}
		for _, r := range regexes {
			fmt.Fprintf(&b, "  (regex %s)\n", sbString(r))
		}
		b.WriteString(extra)
		b.WriteString(")\n")
	}
	rule("deny", "file-read-data", s.denyReads(), s.denyReadFiles(), "")
	rule("allow", "file-read-data", s.allowReads(), s.allowReadFiles(), "")
	b.WriteString("(deny file-write*)\n")
	rule("allow", "file-write*", s.allowWrites(), s.allowWriteFiles(),
		`  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty") (literal "/dev/dtracehelper")
  (literal "/dev/stdout") (literal "/dev/stderr") (subpath "/dev/fd") (regex #"^/dev/ttys[0-9]+$")
`)
	rule("deny", "file-write*", s.denyWrites(), nil, "")
	if s.DenyKeychain {
		b.WriteString(`(deny mach-lookup (global-name "com.apple.SecurityServer") (global-name "com.apple.securityd.xpc"))` + "\n")
	}
	return b.String()
}

func sbString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// matchingFiles lists the existing files a state-file regex matches (bwrap
// binds files individually: it has no regex rules).
func matchingFiles(re string) []string {
	lit, _ := literalPrefix(re)
	if lit == "" {
		return nil
	}
	rx, err := regexp.Compile(re)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(lit)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !e.IsDir() && rx.MatchString(p) {
			out = append(out, p)
		}
	}
	return out
}

// bwrapArgs renders the Linux bubblewrap command line: the root filesystem
// read-only, tmpfs over read-denied directories, re-bound readable trees,
// writable binds for the write set, and read-only binds for denyWrites last
// (later mounts win); network shared. State files are bound one by one (an
// atomic rename into a read-only parent is not possible under bwrap).
func (s *confineSpec) bwrapArgs(bwrap string, argv []string) []string {
	args := []string{bwrap, "--die-with-parent", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	exists := func(p string) (os.FileInfo, bool) {
		fi, err := os.Stat(p)
		return fi, err == nil
	}
	for _, d := range s.denyReads() {
		if fi, ok := exists(d); ok {
			if fi.IsDir() {
				args = append(args, "--tmpfs", d)
			} else {
				args = append(args, "--ro-bind", os.DevNull, d)
			}
		}
	}
	for _, re := range s.denyReadFiles() {
		for _, f := range matchingFiles(re) {
			args = append(args, "--ro-bind", os.DevNull, f)
		}
	}
	for _, d := range s.allowReads() {
		if _, ok := exists(d); ok {
			args = append(args, "--ro-bind", d, d)
		}
	}
	for _, d := range s.allowWrites() {
		if _, ok := exists(d); ok {
			args = append(args, "--bind", d, d)
		}
	}
	for _, re := range s.allowWriteFiles() {
		for _, f := range matchingFiles(re) {
			args = append(args, "--bind", f, f)
		}
	}
	for _, d := range s.denyWrites() {
		if _, ok := exists(d); ok {
			args = append(args, "--ro-bind", d, d)
		}
	}
	return append(append(args, "--"), argv...)
}

// confine is `vmexec confine [flags] -- <cli…>`: build the profile from the
// flags and BNBLOC_CONFINE_* and exec the CLI under it (no fork: signals and
// the exit status are the CLI's own).
func confine(args []string, stdout, stderr io.Writer) int {
	spec, argv, printOnly, err := parseConfine(args, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec confine: %v\n", err)
		return 2
	}
	spec.resolve()
	switch runtime.GOOS {
	case "darwin":
		profile := spec.seatbeltProfile()
		if printOnly {
			fmt.Fprint(stdout, profile)
			return 0
		}
		cli, err := exec.LookPath(argv[0])
		if err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: %v\n", err)
			return 127
		}
		const sandboxExec = "/usr/bin/sandbox-exec"
		if _, err := os.Stat(sandboxExec); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: %s is unavailable; refusing to run %s unconfined\n", sandboxExec, argv[0])
			return exitRefused
		}
		if err := execHost(append([]string{sandboxExec, "-p", profile, cli}, argv[1:]...), os.Environ()); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: %v\n", err)
			return exitFailure
		}
		return 0
	case "linux":
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: bubblewrap (bwrap) is not installed; refusing to run %s unconfined\n", argv[0])
			return exitRefused
		}
		cmd := spec.bwrapArgs(bwrap, argv)
		if printOnly {
			fmt.Fprintln(stdout, strings.Join(cmd, " "))
			return 0
		}
		if err := execHost(cmd, os.Environ()); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: %v\n", err)
			return exitFailure
		}
		return 0
	default:
		fmt.Fprintf(stderr, "bnbloc-vmexec confine: no host sandbox on %s; refusing to run %s unconfined\n", runtime.GOOS, argv[0])
		return exitRefused
	}
}

// parseConfine reads the flags (repeatable path flags) and the
// BNBLOC_CONFINE_* environment written by core/protocol.VMExecEnv.
func parseConfine(args []string, getenv func(string) string) (spec confineSpec, argv []string, printOnly bool, err error) {
	home, herr := os.UserHomeDir()
	if herr != nil {
		return spec, nil, false, fmt.Errorf("home directory: %w", herr)
	}
	spec.Home = home
	spec.Workspace = strings.TrimSpace(getenv(coreprotocol.EnvConfineWorkspace))
	for name, dst := range map[string]*[]string{
		coreprotocol.EnvConfineAllowRead:  &spec.AllowRead,
		coreprotocol.EnvConfineAllowWrite: &spec.AllowWrite,
		coreprotocol.EnvConfineDenyWrite:  &spec.DenyWrite,
	} {
		if raw := strings.TrimSpace(getenv(name)); raw != "" {
			if jerr := json.Unmarshal([]byte(raw), dst); jerr != nil {
				return spec, nil, false, fmt.Errorf("%s: %w", name, jerr)
			}
		}
	}
	if p := strings.TrimSpace(getenv(coreprotocol.EnvVMExecPath)); p != "" {
		spec.VMExecDir = filepath.Dir(p)
	} else if exe, xerr := os.Executable(); xerr == nil {
		spec.VMExecDir = filepath.Dir(exe)
	}
	pathFlags := map[string]*[]string{
		"--allow-read": &spec.AllowRead, "--allow-write": &spec.AllowWrite,
		"--allow-write-prefix": &spec.AllowWritePrefix,
		"--deny-read":          &spec.DenyRead, "--deny-write": &spec.DenyWrite,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if dst, ok := pathFlags[a]; ok || a == "--harness" || a == "--workspace" {
			if i+1 >= len(args) {
				return spec, nil, false, fmt.Errorf("%s needs a value", a)
			}
			i++
			switch {
			case ok:
				*dst = append(*dst, args[i])
			case a == "--harness":
				spec.Harnesses = append(spec.Harnesses, args[i])
			default:
				spec.Workspace = args[i]
			}
			continue
		}
		switch {
		case a == "--":
			argv = args[i+1:]
			i = len(args)
		case a == "--deny-keychain":
			spec.DenyKeychain = true
		case a == "--print-profile":
			printOnly = true
		case strings.HasPrefix(a, "-"):
			return spec, nil, false, fmt.Errorf("unknown flag %q", a)
		default:
			argv = args[i:]
			i = len(args)
		}
	}
	if len(argv) == 0 {
		return spec, nil, false, errors.New("no CLI to run (usage: confine [flags] -- <cli...>)")
	}
	if spec.Workspace == "" || !filepath.IsAbs(spec.Workspace) {
		return spec, nil, false, fmt.Errorf("the workspace must be an absolute path (%s or --workspace)", coreprotocol.EnvConfineWorkspace)
	}
	for _, list := range [][]string{spec.AllowRead, spec.AllowWrite, spec.AllowWritePrefix, spec.DenyRead, spec.DenyWrite} {
		for _, p := range list {
			if !filepath.IsAbs(p) {
				return spec, nil, false, fmt.Errorf("path %q is not absolute", p)
			}
		}
	}
	if len(spec.Harnesses) == 0 {
		spec.Harnesses = []string{harnessFor(argv[0])}
	}
	if cli, lerr := exec.LookPath(argv[0]); lerr == nil {
		if resolved, rerr := filepath.EvalSymlinks(cli); rerr == nil {
			cli = resolved
		}
		spec.CLIPath = cli
	}
	spec.TempDirs = tempDirs()
	return spec, argv, printOnly, nil
}

// tempDirs are the temp locations a CLI legitimately writes (its own cwd
// tracking files, scratch files): $TMPDIR and the system temp roots.
func tempDirs() []string {
	dirs := []string{os.TempDir(), "/tmp"}
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, "/private/tmp", "/private/var/folders")
	}
	return dirs
}
