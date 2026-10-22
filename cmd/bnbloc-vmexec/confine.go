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
	"strconv"
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
//     DenyWrite (core-declared, e.g. routing config in the workdir; a
//     trailing "/" names a directory) wins over every allow, including for
//     paths that do not exist yet.
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
	// DenyReadFinal are paths the profile denies reading AFTER every allow: the
	// descriptor's hostConfine.denyRead (a host tool's private state), which no
	// allow root (the workspace, a temp dir, a harness tree, an allow-read flag)
	// may re-open. DenyRead, the flag's list, keeps its older meaning: a deny
	// that a later allow of a sub-path overrides (how a core hides a directory
	// but one folder in it).
	DenyReadFinal []string
	DenyWrite     []string
	DenyKeychain  bool
	TempDirs      []string

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

// harness is one agent CLI vmexec knows: its documented host layout (what
// confine keeps readable and writable for it, and read-denies when it is
// not the confined CLI) and, when the CLI has one, the command-rewriting
// hook dialect `vmexec hook <name>` speaks for it.
type harness struct {
	name   string
	layout func(home string) harnessLayout
	// plugins lists the plugin sources the CLI declares in its config dir
	// that may live outside it (pluginSources adds symlinked skills).
	plugins func(cfg string) []string
	hook    func(c commandRewriter, raw []byte, stdout, stderr io.Writer) int
}

// harnesses is the one harness list: confine's layouts, the executable
// name a harness is derived from, the hook dialects and the usage text all
// read it. A CLI not in it is "generic" (no state paths of its own).
var harnesses = []harness{
	{name: "claude", layout: claudeLayout, plugins: claudePluginSources, hook: commandRewriter.claudeHook},
	{name: "codex", layout: codexLayout},
	{name: "opencode", layout: opencodeLayout, hook: commandRewriter.opencodeHook},
}

// genericHarness names a CLI with no known layout.
const genericHarness = "generic"

func lookupHarness(name string) (harness, bool) {
	for _, h := range harnesses {
		if h.name == name {
			return h, true
		}
	}
	return harness{}, false
}

// harnessNames lists the harness names (with a hook dialect only, when
// hooks is set), "|"-separated for usage and error text.
func harnessNames(hooks bool) string {
	var names []string
	for _, h := range harnesses {
		if !hooks || h.hook != nil {
			names = append(names, h.name)
		}
	}
	if !hooks {
		names = append(names, genericHarness)
	}
	return strings.Join(names, "|")
}

func harnessLayoutFor(name, home string) (harnessLayout, bool) {
	h, ok := lookupHarness(name)
	if !ok {
		return harnessLayout{}, false
	}
	l := h.layout(home)
	l.name = name
	return l, true
}

func claudeLayout(home string) harnessLayout {
	h := func(rel string) string { return filepath.Join(home, rel) }
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
		roots:     []string{c, h(".local/share/claude"), h(".local/state/claude"), h(".cache/claude"), h("Library/Caches/claude-cli-nodejs")},
		rootFiles: []string{prefixRegex(h(".claude.json"))},
		stateDirs: append(in("projects", "transcripts", "todos", "shell-snapshots", "statsig", "session-env", "sessions",
			"file-history", "paste-cache", "tasks", "telemetry", "debug", "backups", "cache", "downloads", "ide", "teams", "plans", "plugins/data"),
			h(".local/state/claude"), h(".cache/claude"), h("Library/Caches/claude-cli-nodejs")),
		stateFiles: files,
	}
}

func codexLayout(home string) harnessLayout {
	h := func(rel string) string { return filepath.Join(home, rel) }
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
		roots: []string{x, h(".cache/codex-runtimes")},
		stateDirs: in("sessions", "archived_sessions", "log", ".tmp", "tmp", "cache", "shell_snapshots", "sqlite", "memories",
			"app-server-control", "app-server-daemon", "ipc", "mcp-oauth-locks", "project-metadata-locks", "thread-writer-locks",
			"rollout-migrations", "visualizations"),
		stateFiles: files,
	}
}

func opencodeLayout(home string) harnessLayout {
	h := func(rel string) string { return filepath.Join(home, rel) }
	cache := h(".cache/opencode")
	return harnessLayout{
		roots:     []string{h(".config/opencode"), h(".local/share/opencode"), h(".local/state/opencode"), cache},
		stateDirs: []string{h(".local/share/opencode"), h(".local/state/opencode"), cache},
		readOnly:  []string{filepath.Join(cache, "packages"), filepath.Join(cache, "bin"), filepath.Join(cache, "node_modules")},
	}
}

// harnessFor derives the harness from the CLI's executable name.
func harnessFor(argv0 string) string {
	base := strings.ToLower(trimExe(filepath.Base(argv0)))
	for _, h := range harnesses {
		if base == h.name || strings.HasPrefix(base, h.name+"-") {
			return h.name
		}
	}
	return genericHarness
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
}

// desktopIdentifier is the bnbloc desktop's bundle identifier, the name of
// its per-user directories (apps/desktop/src-tauri/tauri.conf.json;
// TestConfineDeniesBnblocCredentialPaths reads it from there).
const desktopIdentifier = "sh.bnbloc.desktop"

// credentialPaths are bnbloc's own credential stores under home, read-denied
// for every confined CLI: the CLI's sign-in (refresh and API tokens) and
// publisher signing key under ~/.bnbloc, and the desktop's data, config and
// web storage (session material, plugin and MCP settings and sign-ins). The
// network is open to a confined CLI, so a readable credential is an
// exfiltrated one. The desktop's cache dir is not here: it holds no secret,
// and the stdio MCP tool binaries a confined CLI spawns live there. This is
// the one list; a new bnbloc credential location is added here.
var credentialPaths = []string{
	".bnbloc",
	"Library/Application Support/" + desktopIdentifier,
	"Library/WebKit/" + desktopIdentifier,
	".local/share/" + desktopIdentifier,
	".config/" + desktopIdentifier,
}

// resolve fills the derived lists: listed and unlisted harness layouts and
// the plugin/skill sources of the listed ones.
func (s *confineSpec) resolve() {
	s.layouts, s.others = nil, nil
	listed := map[string]bool{}
	for _, name := range s.Harnesses {
		if h, ok := lookupHarness(name); ok && !listed[name] {
			l, _ := harnessLayoutFor(name, s.Home)
			s.layouts = append(s.layouts, l)
			s.pluginDirs = append(s.pluginDirs, pluginSources(h, l.roots)...)
		}
		listed[name] = true
	}
	for _, h := range harnesses {
		if !listed[h.name] {
			l, _ := harnessLayoutFor(h.name, s.Home)
			s.others = append(s.others, l)
		}
	}
}

// pluginSources returns the directories the CLI loads plugins and skills
// from that may live OUTSIDE its config dir: Claude's installed plugin
// install paths and marketplace locations (incl. local directory sources),
// and the resolved targets of symlinked skills/commands/agents. They become
// readable, never writable.
func pluginSources(h harness, roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	cfg := roots[0]
	var out []string
	if h.plugins != nil {
		out = h.plugins(cfg)
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

// claudePluginSources are Claude's installed plugin install paths and
// marketplace locations (incl. local directory sources), from its config
// dir cfg.
func claudePluginSources(cfg string) []string {
	var out []string
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
	return out
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
	deny := make([]string, 0, len(sensitiveHomePaths)+len(credentialPaths)+16)
	for _, r := range append(append([]string(nil), sensitiveHomePaths...), credentialPaths...) {
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
	out := make([]string, 0, len(s.DenyWrite)+4)
	for _, e := range s.denyWriteEntries() {
		out = append(out, e.path)
	}
	return out
}

// denyWriteEntry is one deny-write path (canonical) and whether it names a
// directory. The DenyWrite contract marks a directory with a trailing "/"
// (canonicalization drops it, so it is read first); a harness's readOnly
// paths are directories.
type denyWriteEntry struct {
	path string
	dir  bool
}

func (s *confineSpec) denyWriteEntries() []denyWriteEntry {
	dirs := map[string]bool{}
	var paths []string
	for _, l := range s.layouts {
		for _, p := range l.readOnly {
			paths = append(paths, p)
			dirs[canonical(p)] = true
		}
	}
	for _, p := range s.DenyWrite {
		paths = append(paths, p)
		if strings.HasSuffix(p, "/") {
			dirs[canonical(p)] = true
		}
	}
	clean := uniqCanonical(paths)
	out := make([]denyWriteEntry, 0, len(clean))
	for _, p := range clean {
		out = append(out, denyWriteEntry{path: p, dir: dirs[p]})
	}
	return out
}

// ensureDenyWriteMountPoints creates every deny-write path that does not
// exist yet but lies inside a writable tree, as an empty placeholder: a
// directory for a directory entry, else an empty file ("{}" for a .json
// file), parents included. Root cause: bubblewrap can only bind a path that
// exists, so a missing deny-write path (an agent-config file the workspace
// does not have yet) could not be made read-only, and the confined CLI could
// create it (Seatbelt denies the creation itself). A missing path outside
// every writable tree needs nothing: the read-only root already refuses it.
// A placeholder that cannot be created fails the run, never runs it with the
// path writable.
func (s *confineSpec) ensureDenyWriteMountPoints() error {
	writable := s.allowWrites()
	inWritable := func(p string) bool {
		for _, w := range writable {
			if strings.HasPrefix(p, strings.TrimSuffix(w, "/")+"/") {
				return true
			}
		}
		return false
	}
	for _, e := range s.denyWriteEntries() {
		if _, err := os.Lstat(e.path); err == nil || !errors.Is(err, os.ErrNotExist) || !inWritable(e.path) {
			continue
		}
		if e.dir {
			if err := os.MkdirAll(e.path, 0o755); err != nil {
				return fmt.Errorf("deny-write placeholder %s: %w", e.path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
			return fmt.Errorf("deny-write placeholder %s: %w", e.path, err)
		}
		var content []byte
		if strings.EqualFold(filepath.Ext(e.path), ".json") {
			content = []byte("{}\n")
		}
		if err := os.WriteFile(e.path, content, 0o644); err != nil {
			return fmt.Errorf("deny-write placeholder %s: %w", e.path, err)
		}
	}
	return nil
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
	// Last for reads, so nothing above re-opens it.
	rule("deny", "file-read-data", uniqCanonical(s.DenyReadFinal), nil, "")
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
// atomic rename into a read-only parent is not possible under bwrap). A
// deny-write path is bound only when it exists: confine creates the missing
// ones first (ensureDenyWriteMountPoints), and its ancestors inside the
// writable tree are pinned as mount points (denyWriteAncestors).
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
	for _, d := range s.denyWriteAncestors() {
		args = append(args, "--bind", d, d)
	}
	for _, d := range s.denyWrites() {
		if _, ok := exists(d); ok {
			args = append(args, "--ro-bind", d, d)
		}
	}
	return append(append(args, "--"), argv...)
}

// denyWriteAncestors are the existing directories between a deny-write path
// and the writable tree holding it (e.g. <workspace>/.claude for
// <workspace>/.claude/skills/). bubblewrap protects a path by mounting on
// it, and a mount moves with its parent: renaming .claude away and creating
// a fresh .claude/skills would escape the read-only bind. Binding each such
// ancestor onto itself (still writable) makes it a mount point, which
// cannot be renamed or removed. Seatbelt needs none of this: its rules
// match paths, not mounts. Shallow first, so the deny binds land on top.
func (s *confineSpec) denyWriteAncestors() []string {
	writable := s.allowWrites()
	seen := map[string]bool{}
	var out []string
	for _, e := range s.denyWriteEntries() {
		root := ""
		for _, w := range writable {
			if strings.HasPrefix(e.path, strings.TrimSuffix(w, "/")+"/") && len(w) > len(root) {
				root = w
			}
		}
		if root == "" {
			continue
		}
		for dir := filepath.Dir(e.path); dir != root && strings.HasPrefix(dir, root+"/"); dir = filepath.Dir(dir) {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() && !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if di, dj := strings.Count(out[i], "/"), strings.Count(out[j], "/"); di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
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
		if err := execHost(append([]string{sandboxExec, "-p", profile, cli}, argv[1:]...), confinedEnv(os.Environ())); err != nil {
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
		if err := spec.ensureDenyWriteMountPoints(); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: %v; refusing to run %s with it writable\n", err, argv[0])
			return exitRefused
		}
		cmd := spec.bwrapArgs(bwrap, argv)
		if printOnly {
			fmt.Fprintln(stdout, strings.Join(cmd, " "))
			return 0
		}
		if err := execHost(cmd, confinedEnv(os.Environ())); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec confine: %v\n", err)
			return exitFailure
		}
		return 0
	default:
		fmt.Fprintf(stderr, "bnbloc-vmexec confine: no host sandbox on %s; refusing to run %s unconfined\n", runtime.GOOS, argv[0])
		return exitRefused
	}
}

// gitOverrides are the git settings every confined CLI (and each git it
// runs) gets above the repository's own config: the workspace is shared
// with the guest, which can write .git/config and .git/hooks, and a hook
// or an fsmonitor command it wrote must never run on the host when the
// agent's CLI calls git there (docs/prd/2026-10-13-live-turns.md §4).
var gitOverrides = [][2]string{
	{"core.fsmonitor", "false"},
	{"core.hooksPath", "/dev/null"},
}

// confinedEnv is environ with gitOverrides appended through GIT_CONFIG_COUNT
// (git's command-line scope, above every config file), after any entries
// already there; an unreadable count drops those entries.
func confinedEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+2*len(gitOverrides)+1)
	n := 0
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			if c, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && c > 0 {
				n = c
			}
			continue
		}
		out = append(out, kv)
	}
	if n == 0 {
		// No (readable) count: entries numbered without one are not git's.
		kept := out[:0]
		for _, kv := range out {
			if !strings.HasPrefix(kv, "GIT_CONFIG_KEY_") && !strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") {
				kept = append(kept, kv)
			}
		}
		out = kept
	}
	for _, o := range gitOverrides {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, o[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, o[1]))
		n++
	}
	return append(out, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n))
}

// parseConfine reads the flags (repeatable path flags) and the host
// confinement environment written by core/protocol.VMExecEnv
// (protocol.HostConfineFromEnv).
func parseConfine(args []string, getenv func(string) string) (spec confineSpec, argv []string, printOnly bool, err error) {
	home, herr := os.UserHomeDir()
	if herr != nil {
		return spec, nil, false, fmt.Errorf("home directory: %w", herr)
	}
	spec.Home = home
	hc, herr := coreprotocol.HostConfineFromEnv(getenv)
	if herr != nil {
		return spec, nil, false, herr
	}
	if hc != nil {
		spec.Workspace, spec.AllowRead, spec.AllowWrite, spec.DenyWrite, spec.DenyReadFinal = hc.Workspace, hc.AllowRead, hc.AllowWrite, hc.DenyWrite, hc.DenyRead
	}
	spec.VMExecDir = filepath.Dir(vmexecPath(getenv))
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
	for _, list := range [][]string{spec.AllowRead, spec.AllowWrite, spec.AllowWritePrefix, spec.DenyRead, spec.DenyReadFinal, spec.DenyWrite} {
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
