package pkgmeta

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/extgov"
)

// KindMCPPackageV1 is the package-envelope kind of an MCP tool component
// (docs/prd/2026-10-12-experts-and-teams.md §6): a tool server content
// (experts, skill packs) may require by name. It is the admin-approved tool
// catalog: content can never inline an MCP command.
//
// Two runtimes:
//   - "stdio": per-platform binaries (artifacts, sha256-pinned and staged on
//     the gateway like sidecar binaries) the member's desktop materializes
//     and the agent spawns per session as a stdio MCP server. The artifact is
//     the command; mcp.stdio.args are its arguments.
//   - "endpoint": an MCP server over streamable HTTP at endpoint.baseUrl,
//     reached under permissions.network and probed like every endpoint
//     component (internal/endpointpkg).
const KindMCPPackageV1 = "bnbloc.mcp.package/v1"

// MCPContractV1 pins the MCP component package shape (the mcp block).
const MCPContractV1 = "bnbloc.mcp.contract/v1"

// RuntimeStdio is a stdio MCP server: binaries, no process the desktop
// supervises, no endpoint.
const RuntimeStdio Runtime = "stdio"

// Where an MCP component's env values come from (MCP.Credentials).
const (
	// MCPCredentialsMember: each member enters them on their own desktop
	// (Settings → Tools); they never leave that machine.
	MCPCredentialsMember = "member"
	// MCPCredentialsOrg: an org admin configures one connector per
	// organization, stored sealed server-side.
	MCPCredentialsOrg = "org"
)

// HTTP auth schemes of an endpoint MCP component (MCPHTTPAuth.Scheme).
const (
	MCPAuthNone   = "none"
	MCPAuthBearer = "bearer"
	MCPAuthHeader = "header"
	// MCPAuthOAuth: each member signs in to the server's authorization
	// server from their desktop (the MCP authorization flow: protected
	// resource metadata, PKCE, a loopback redirect); the access token is
	// the member's own and never leaves their machine. The authorization
	// server is discovered from the server at sign-in, never declared.
	MCPAuthOAuth = "oauth"
)

// MCP is an MCP component's payload (Package.MCP).
type MCP struct {
	// Tools are the tools the server offers, for display (the server's own
	// tools/list stays authoritative at run time).
	Tools []MCPTool `json:"tools"`
	// Env are the variables the server needs; values never ship in a
	// package (Credentials says where they come from).
	Env []MCPEnv `json:"env,omitempty"`
	// Credentials is member or org; required exactly when Env is declared.
	Credentials string    `json:"credentials,omitempty"`
	Stdio       *MCPStdio `json:"stdio,omitempty"`
	HTTP        *MCPHTTP  `json:"http,omitempty"`
}

// MCPTool is one tool of the server.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MCPEnv is one variable an MCP server needs.
type MCPEnv struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// MCPStdio is how a stdio server is started: the materialized binary with
// these arguments (env values arrive as its process environment).
type MCPStdio struct {
	Args []string `json:"args,omitempty"`
}

// MCPHTTP is how an endpoint server is called.
type MCPHTTP struct {
	Auth MCPHTTPAuth `json:"auth"`
}

// MCPHTTPAuth is the header an endpoint server expects: none, a bearer token
// (Authorization: Bearer <Env>), a named header (<Header>: <Env>), or a
// member's OAuth sign-in (Authorization: Bearer <their access token>).
type MCPHTTPAuth struct {
	Scheme string `json:"scheme"`
	Header string `json:"header,omitempty"`
	Env    string `json:"env,omitempty"`
	// ClientID (oauth, optional) is a client the publisher registered with
	// the server's authorization server; without it the desktop registers
	// itself where the authorization server allows it.
	ClientID string `json:"clientId,omitempty"`
	// Scopes (oauth, optional) are requested when the server's challenge
	// names none.
	Scopes []string `json:"scopes,omitempty"`
}

// MCP bounds: a display catalog, not the server's schema.
const (
	maxMCPTools        = 64
	maxMCPToolDesc     = 1024
	maxMCPEnv          = 16
	maxMCPEnvDesc      = 300
	maxMCPArgs         = 32
	maxMCPArgLen       = 1024
	mcpReservedEnvPref = "BNBLOC_"
	maxMCPOAuthScopes  = 32
	maxMCPOAuthScope   = 128
	maxMCPClientID     = 256
)

var (
	mcpToolNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	mcpHeaderRE   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	// mcpScopeRE is an OAuth scope token (RFC 6749 §3.3 NQCHAR).
	mcpScopeRE = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]+$`)
)

func validateMCPPackage(pkg Package) error {
	if pkg.Contract != MCPContractV1 {
		return fmt.Errorf("unknown mcp contract %q", pkg.Contract)
	}
	switch {
	case pkg.MCP == nil:
		return fmt.Errorf("mcp package requires mcp")
	case pkg.Sidecar != nil:
		return fmt.Errorf("mcp package must not declare sidecar (a stdio server is spawned by the agent, never supervised)")
	case pkg.Manifest != nil:
		return fmt.Errorf("mcp package must not declare manifest")
	case len(pkg.Hooks) > 0:
		return fmt.Errorf("hooks are only valid for %s packages", KindPluginPackageV1)
	case len(pkg.Capabilities) > 0:
		return fmt.Errorf("mcp package must not declare capabilities")
	case pkg.Protocol.Min != "" || pkg.Protocol.Max != "":
		return fmt.Errorf("mcp package must not declare protocol (MCP negotiates its own)")
	}
	m := *pkg.MCP
	if err := validateMCPTools(m.Tools); err != nil {
		return err
	}
	if err := validateMCPEnv(m); err != nil {
		return err
	}
	switch pkg.Runtime {
	case RuntimeStdio:
		if len(pkg.Artifacts) == 0 {
			return fmt.Errorf(`mcp runtime "stdio" requires artifacts (one binary per platform)`)
		}
		for key := range pkg.Artifacts {
			if strings.Contains(key, "/") {
				return fmt.Errorf("artifacts.%s: an mcp stdio package declares one binary per platform", key)
			}
		}
		if pkg.Endpoint != nil {
			return fmt.Errorf(`mcp runtime "stdio" must not declare endpoint`)
		}
		if len(pkg.Permissions.Network) > 0 {
			return fmt.Errorf(`mcp runtime "stdio" must not declare permissions.network`)
		}
		if m.HTTP != nil {
			return fmt.Errorf(`mcp runtime "stdio" must not declare mcp.http`)
		}
		if m.Stdio != nil {
			if len(m.Stdio.Args) > maxMCPArgs {
				return fmt.Errorf("mcp.stdio.args: at most %d arguments", maxMCPArgs)
			}
			for i, a := range m.Stdio.Args {
				if len(a) > maxMCPArgLen || !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
					return fmt.Errorf("mcp.stdio.args[%d] must be text of at most %d bytes", i, maxMCPArgLen)
				}
			}
		}
	case RuntimeEndpoint:
		if len(pkg.Artifacts) > 0 {
			return fmt.Errorf(`mcp runtime "endpoint" must not declare artifacts`)
		}
		if m.Stdio != nil {
			return fmt.Errorf(`mcp runtime "endpoint" must not declare mcp.stdio`)
		}
		if pkg.Endpoint == nil || strings.TrimSpace(pkg.Endpoint.BaseURL) == "" {
			return fmt.Errorf(`mcp runtime "endpoint" requires endpoint.baseUrl (the MCP URL)`)
		}
		if err := validateAbsoluteHTTPURL(pkg.Endpoint.BaseURL); err != nil {
			return fmt.Errorf("endpoint.baseUrl must be an absolute http(s) URL")
		}
		if len(pkg.Permissions.Network) == 0 {
			return fmt.Errorf(`mcp runtime "endpoint" requires permissions.network`)
		}
		if !extgov.NewNetGuard(pkg.Permissions.Network).AllowURL(pkg.Endpoint.BaseURL) {
			return fmt.Errorf("mcp endpoint host is not in permissions.network")
		}
		if err := validateMCPHTTP(m); err != nil {
			return err
		}
	default:
		return fmt.Errorf(`mcp runtime must be "stdio" or "endpoint", got %q`, pkg.Runtime)
	}
	return nil
}

func validateMCPTools(tools []MCPTool) error {
	if len(tools) == 0 {
		return fmt.Errorf("mcp.tools: declare at least one tool")
	}
	if len(tools) > maxMCPTools {
		return fmt.Errorf("mcp.tools: at most %d tools", maxMCPTools)
	}
	seen := map[string]bool{}
	for i, t := range tools {
		if !mcpToolNameRE.MatchString(t.Name) {
			return fmt.Errorf("mcp.tools[%d].name %q: use letters, digits, '_', '.' and '-' (at most 64)", i, t.Name)
		}
		if seen[t.Name] {
			return fmt.Errorf("mcp.tools: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
		if utf8.RuneCountInString(t.Description) > maxMCPToolDesc || !utf8.ValidString(t.Description) {
			return fmt.Errorf("mcp.tools[%d].description must be text of at most %d characters", i, maxMCPToolDesc)
		}
	}
	return nil
}

func validateMCPEnv(m MCP) error {
	if len(m.Env) > maxMCPEnv {
		return fmt.Errorf("mcp.env: at most %d variables", maxMCPEnv)
	}
	seen := map[string]bool{}
	for i, e := range m.Env {
		if !sidecarEnvNameRE.MatchString(e.Name) {
			return fmt.Errorf("mcp.env[%d].name %q must be an upper-case identifier", i, e.Name)
		}
		if strings.HasPrefix(e.Name, mcpReservedEnvPref) {
			return fmt.Errorf("mcp.env[%d].name %q: the %s prefix is reserved", i, e.Name, mcpReservedEnvPref)
		}
		if seen[e.Name] {
			return fmt.Errorf("mcp.env: duplicate variable %q", e.Name)
		}
		seen[e.Name] = true
		if utf8.RuneCountInString(e.Description) > maxMCPEnvDesc || strings.ContainsAny(e.Description, "\r\n") {
			return fmt.Errorf("mcp.env[%d].description must be one line of at most %d characters", i, maxMCPEnvDesc)
		}
	}
	switch m.Credentials {
	case "":
		if len(m.Env) > 0 {
			return fmt.Errorf("mcp.credentials is required with mcp.env (%q or %q)", MCPCredentialsMember, MCPCredentialsOrg)
		}
	case MCPCredentialsMember, MCPCredentialsOrg:
		if len(m.Env) == 0 {
			return fmt.Errorf("mcp.credentials needs mcp.env naming the variables")
		}
	default:
		return fmt.Errorf("mcp.credentials must be %q or %q, got %q", MCPCredentialsMember, MCPCredentialsOrg, m.Credentials)
	}
	return nil
}

// validateMCPHTTP checks an endpoint server's auth: the header value comes
// from one declared env, and an endpoint takes no other env (its only
// credential is the header).
func validateMCPHTTP(m MCP) error {
	auth := MCPHTTPAuth{Scheme: MCPAuthNone}
	if m.HTTP != nil {
		auth = m.HTTP.Auth
	}
	if auth.Scheme != MCPAuthOAuth && (auth.ClientID != "" || len(auth.Scopes) > 0) {
		return fmt.Errorf(`mcp.http.auth clientId and scopes belong to scheme "oauth"`)
	}
	switch auth.Scheme {
	case MCPAuthOAuth:
		return validateMCPOAuth(m, auth)
	case MCPAuthNone:
		if auth.Env != "" || auth.Header != "" {
			return fmt.Errorf(`mcp.http.auth scheme "none" takes no env or header`)
		}
		if len(m.Env) > 0 {
			return fmt.Errorf("mcp.env: an endpoint server without auth takes no env")
		}
		return nil
	case MCPAuthBearer:
		if auth.Header != "" {
			return fmt.Errorf(`mcp.http.auth scheme "bearer" takes no header (it is Authorization)`)
		}
	case MCPAuthHeader:
		if !mcpHeaderRE.MatchString(auth.Header) {
			return fmt.Errorf("mcp.http.auth.header %q must be a header name", auth.Header)
		}
	default:
		return fmt.Errorf("mcp.http.auth.scheme must be none, bearer, header or oauth, got %q", auth.Scheme)
	}
	if len(m.Env) != 1 || m.Env[0].Name != auth.Env {
		return fmt.Errorf("mcp.http.auth.env must name the one declared mcp.env variable (an endpoint server's only credential is its auth header)")
	}
	return nil
}

// validateMCPOAuth checks an oauth endpoint: each member signs in for
// themselves, so it takes no env, header or org connector (credentials are
// always the member's own), and the optional client id and scopes are
// plain OAuth values.
func validateMCPOAuth(m MCP, auth MCPHTTPAuth) error {
	if auth.Env != "" || auth.Header != "" {
		return fmt.Errorf(`mcp.http.auth scheme "oauth" takes no env or header (each member signs in)`)
	}
	if m.Credentials != "" || len(m.Env) > 0 {
		return fmt.Errorf(`mcp.http.auth scheme "oauth" takes no mcp.env or mcp.credentials: each member signs in for themselves, never through an organization connector`)
	}
	if len(auth.ClientID) > maxMCPClientID || (auth.ClientID != "" && !mcpScopeRE.MatchString(auth.ClientID)) {
		return fmt.Errorf("mcp.http.auth.clientId must be printable text of at most %d characters without spaces", maxMCPClientID)
	}
	if len(auth.Scopes) > maxMCPOAuthScopes {
		return fmt.Errorf("mcp.http.auth.scopes: at most %d scopes", maxMCPOAuthScopes)
	}
	seen := map[string]bool{}
	for i, sc := range auth.Scopes {
		if len(sc) > maxMCPOAuthScope || !mcpScopeRE.MatchString(sc) {
			return fmt.Errorf("mcp.http.auth.scopes[%d] %q is not an OAuth scope", i, sc)
		}
		if seen[sc] {
			return fmt.Errorf("mcp.http.auth.scopes: duplicate scope %q", sc)
		}
		seen[sc] = true
	}
	return nil
}

func init() { RegisterKindValidator(KindMCPPackageV1, validateMCPPackage) }
