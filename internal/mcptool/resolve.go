package mcptool

import (
	"slices"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Candidate is one installed MCP component as a member may see it.
type Candidate struct {
	Record extpipe.Record
	Config extpipe.PackageConfig
	// Visibility is the member's, from extpipe.MemberVisibility.
	Visibility extpipe.Visibility
}

// Lookup finds an installed MCP component by name.
type Lookup func(name string) (Candidate, bool)

// Settings answers a tool's platform and organization values for the
// job's organization and the required settings of those scopes that are
// not set (componentsettings.Store.Resolved), under the declaration as the
// job needs it.
type Settings func(name string, decl []pkgmeta.Setting) (map[string]string, []string, error)

// Resolve turns the MCP tools in effect for a turn into the tools the
// bridge attaches, and names the unmet requirements (the turn still runs;
// they show as missing). Two things put a tool in effect
// (docs/prd/2026-10-17-browser-component.md §3.6):
//
//   - a package in effect requires it (refs): met by an installed MCP
//     component the requester may use (Lookup's visibility is the
//     requester's) that declares every setting the package needs, which
//     the job then needs set too;
//   - the requester installed it (installed, names in order): offered with
//     no requirement, so one that cannot be attached is left out quietly
//     rather than reported missing.
//
// A tool whose required platform or organization settings are not set
// needs setup: it is not offered to the agent. Its member settings are
// resolved by the bridge, on the member's machine.
func Resolve(refs []wire.ContentRef, installed []string, lookup Lookup, settings Settings) ([]wire.ContentMCPTool, []string, error) {
	type need struct {
		settings   map[string]bool
		requiredBy []string
	}
	// unmet records a requirement no tool meets; an offered tool (nothing
	// requires it) is dropped without a word.
	var missing []string
	unmet := func(n *need, why string) {
		if len(n.requiredBy) > 0 {
			missing = append(missing, why)
		}
	}
	needs := map[string]*need{}
	var order []string
	for _, ref := range refs {
		for _, req := range ref.MCP {
			n := needs[req.Name]
			if n == nil {
				n = &need{settings: map[string]bool{}}
				needs[req.Name] = n
				order = append(order, req.Name)
			}
			for _, key := range req.Settings {
				n.settings[key] = true
			}
			if !slices.Contains(n.requiredBy, ref.ComponentID) {
				n.requiredBy = append(n.requiredBy, ref.ComponentID)
			}
		}
	}
	for _, name := range installed {
		if needs[name] == nil {
			needs[name] = &need{settings: map[string]bool{}}
			order = append(order, name)
		}
	}
	var tools []wire.ContentMCPTool
	for _, name := range order {
		n := needs[name]
		cand, ok := lookup(name)
		if !ok || !cand.Visibility.Installed || cand.Config.MCP == nil {
			unmet(n, "MCP tool "+name)
			continue
		}
		cfg := cand.Config
		decl := make([]pkgmeta.Setting, 0, len(cfg.Settings))
		for _, s := range cfg.Settings {
			s.Required = s.Required || n.settings[s.Key]
			delete(n.settings, s.Key)
			decl = append(decl, s)
		}
		if len(n.settings) > 0 {
			unmet(n, "MCP tool "+name+" (it no longer has the settings the content needs)")
			continue
		}
		values, unset, err := settings(name, decl)
		if err != nil {
			return nil, nil, err
		}
		if len(unset) > 0 {
			unmet(n, "MCP tool "+name+" (needs setup: an admin has not set "+settingLabels(decl, unset)+")")
			continue
		}
		tool := wire.ContentMCPTool{
			Name: name, Version: cand.Record.Version, Transport: Transport(cfg),
			Settings: decl, Credentials: cfg.MCP.Credentials, RequiredBy: n.requiredBy,
		}
		if len(values) > 0 {
			tool.Values = values
		}
		switch tool.Transport {
		case wire.MCPTransportStdio:
			tool.Artifacts = map[string]string{}
			for platform, art := range cfg.Artifacts {
				tool.Artifacts[platform] = art.SHA256
			}
			if cfg.MCP.Stdio != nil {
				tool.Args = append([]string(nil), cfg.MCP.Stdio.Args...)
				tool.Placement = cfg.MCP.Stdio.Placement
				tool.PrivatePaths = append([]string(nil), cfg.MCP.Stdio.PrivatePaths...)
			}
		case wire.MCPTransportHTTP:
			tool.URL = cfg.Endpoint.BaseURL
			auth := pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthNone}
			if cfg.MCP.HTTP != nil {
				auth = cfg.MCP.HTTP.Auth
			}
			tool.Auth = &auth
		}
		tools = append(tools, tool)
	}
	sort.Strings(missing)
	return tools, missing, nil
}

// settingLabels names settings by their labels, in declaration order.
func settingLabels(decl []pkgmeta.Setting, keys []string) string {
	var out []string
	for _, s := range decl {
		if slices.Contains(keys, s.Key) {
			out = append(out, s.Label)
		}
	}
	return strings.Join(out, ", ")
}
