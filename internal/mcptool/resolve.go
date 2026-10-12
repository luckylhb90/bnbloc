package mcptool

import (
	"errors"
	"sort"

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

// OrgValues returns the organization connector's values of a component
// (ok=false: not configured; err: the store failed or connectors are off).
type OrgValues func(name string) (map[string]string, bool, error)

// Resolve turns the MCP requirements of the packages in effect for a turn
// into the tools the bridge attaches, and names the unmet ones (the turn
// still runs; they show as missing). A requirement is met by an installed
// MCP component the requester may use (Lookup's visibility is the
// requester's) that declares every env name the package needs; an org
// credential tool also needs its organization connector configured. Member
// credentials are resolved by the bridge, on the member's machine.
func Resolve(refs []wire.ContentRef, lookup Lookup, org OrgValues) ([]wire.ContentMCPTool, []string, error) {
	type need struct {
		env        map[string]bool
		requiredBy []string
	}
	needs := map[string]*need{}
	var order []string
	for _, ref := range refs {
		for _, req := range ref.MCP {
			n := needs[req.Name]
			if n == nil {
				n = &need{env: map[string]bool{}}
				needs[req.Name] = n
				order = append(order, req.Name)
			}
			for _, e := range req.Env {
				n.env[e] = true
			}
			if !contains(n.requiredBy, ref.ComponentID) {
				n.requiredBy = append(n.requiredBy, ref.ComponentID)
			}
		}
	}
	var tools []wire.ContentMCPTool
	var missing []string
	for _, name := range order {
		n := needs[name]
		cand, ok := lookup(name)
		if !ok || !cand.Visibility.Installed || cand.Config.MCP == nil {
			missing = append(missing, "MCP tool "+name)
			continue
		}
		cfg := cand.Config
		declared := map[string]pkgmeta.MCPEnv{}
		for _, e := range cfg.MCP.Env {
			declared[e.Name] = e
		}
		undeclared := false
		for e := range n.env {
			if _, ok := declared[e]; !ok {
				undeclared = true
			}
		}
		if undeclared {
			missing = append(missing, "MCP tool "+name+" (it no longer takes the variables the content needs)")
			continue
		}
		tool := wire.ContentMCPTool{
			Name: name, Version: cand.Record.Version, Transport: Transport(cfg),
			Credentials: cfg.MCP.Credentials, RequiredBy: n.requiredBy,
		}
		for _, e := range cfg.MCP.Env {
			tool.Env = append(tool.Env, wire.ContentMCPEnv{Name: e.Name, Secret: e.Secret, Required: e.Required || n.env[e.Name]})
		}
		switch tool.Transport {
		case wire.MCPTransportStdio:
			tool.Artifacts = map[string]string{}
			for platform, art := range cfg.Artifacts {
				tool.Artifacts[platform] = art.SHA256
			}
			if cfg.MCP.Stdio != nil {
				tool.Args = append([]string(nil), cfg.MCP.Stdio.Args...)
			}
		case wire.MCPTransportHTTP:
			tool.URL = cfg.Endpoint.BaseURL
			auth := wire.ContentMCPAuth{Scheme: pkgmeta.MCPAuthNone}
			if cfg.MCP.HTTP != nil {
				auth = wire.ContentMCPAuth{Scheme: cfg.MCP.HTTP.Auth.Scheme, Header: cfg.MCP.HTTP.Auth.Header, Env: cfg.MCP.HTTP.Auth.Env}
			}
			tool.Auth = &auth
		}
		if cfg.MCP.Credentials == pkgmeta.MCPCredentialsOrg {
			values, ok, err := org(name)
			if err != nil {
				if errors.Is(err, ErrConnectorsOff) {
					missing = append(missing, "MCP tool "+name+" (organization connectors are off on this deployment)")
					continue
				}
				return nil, nil, err
			}
			if !ok || !requiredSet(tool.Env, values) {
				missing = append(missing, "MCP tool "+name+" (an organization admin has not configured its connector)")
				continue
			}
			tool.Values = values
		}
		tools = append(tools, tool)
	}
	sort.Strings(missing)
	return tools, missing, nil
}

func requiredSet(env []wire.ContentMCPEnv, values map[string]string) bool {
	for _, e := range env {
		if e.Required && values[e.Name] == "" {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
