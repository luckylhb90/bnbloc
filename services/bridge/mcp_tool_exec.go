package bridge

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The stdio tool launcher (docs/bugs/2026-10-19-tool-update-resets-sessions.md):
// a session's profile names a stdio MCP tool as this executable,
//
//	bnbloc-bridge mcp-tool --dir <MCPToolsDir> [--store <MCPToolsStore>] --name <tool> -- <tool args>
//
// never as the tool's binary, whose path carries its version. The profile
// (and the session's binding checksum) then stays the same when the tool is
// updated, so an update never restarts the agent's conversation; each
// start of the tool runs the version the desktop verified last. The
// turn's own check that the gateway's pin is materialized (mcpBinary) is
// unchanged.

// MCPToolCommand is the launcher's subcommand.
const MCPToolCommand = "mcp-tool"

// UseToolLauncher makes stdio tools start through launcher (this
// executable; "" names the tool's binary directly).
func (b *Bridge) UseToolLauncher(launcher string) {
	b.toolLauncher = strings.TrimSpace(launcher)
}

// toolCommand is how a session starts stdio tool name, materialized at
// path: through the launcher when there is one.
func (b *Bridge) toolCommand(name, path string, args []string) (string, []string) {
	dir := strings.TrimSpace(b.cfg.MCPToolsDir)
	if b.toolLauncher == "" || dir == "" {
		return path, append([]string(nil), args...)
	}
	out := []string{MCPToolCommand, "--dir", dir}
	if store := strings.TrimSpace(b.cfg.MCPToolsStore); store != "" {
		out = append(out, "--store", store)
	}
	return b.toolLauncher, append(append(out, "--name", name, "--"), args...)
}

// materializedTool is the binary of tool name in index idx: the entry
// carrying pin ("" any: the launcher takes the one the desktop verified
// last), a regular file inside store, the directory the desktop keeps the
// binaries in (the index's own directory when it names none). why says
// what is wrong otherwise. The one place a tool's binary is looked up.
func materializedTool(store string, idx *mcpIndex, name, pin string) (string, string) {
	for _, e := range idx.Tools {
		if e.Name != name || (pin != "" && e.SHA256 != pin) {
			continue
		}
		rel, err := filepath.Rel(store, e.Path)
		if err != nil || !filepath.IsAbs(e.Path) || rel == "." || strings.HasPrefix(rel, "..") {
			return "", "its binary is outside the tools store"
		}
		if st, err := os.Stat(e.Path); err != nil || !st.Mode().IsRegular() {
			return "", "not downloaded on this computer yet"
		}
		return e.Path, ""
	}
	return "", "not downloaded on this computer yet"
}

// RunMCPToolMain runs the launcher: it finds the tool's binary in the
// desktop's index and runs it in its place, with the tool's args, this
// process's environment and stdio (the MCP channel). It answers the exit
// code.
func RunMCPToolMain(args []string) int {
	fs := flag.NewFlagSet(MCPToolCommand, flag.ContinueOnError)
	dir := fs.String("dir", "", "the directory of the desktop's tools index")
	store := fs.String("store", "", "where the desktop keeps the tools' binaries (default: --dir)")
	name := fs.String("name", "", "the tool")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "bnbloc-bridge mcp-tool: --dir and --name are required")
		return 2
	}
	b := &Bridge{cfg: Config{MCPToolsDir: *dir, MCPToolsStore: *store}}
	path, why := materializedTool(b.mcpToolsStore(), b.loadMCPIndex(), *name, "")
	if why != "" {
		fmt.Fprintf(os.Stderr, "bnbloc-bridge mcp-tool: %s: %s\n", *name, why)
		return 1
	}
	return execTool(path, fs.Args())
}
