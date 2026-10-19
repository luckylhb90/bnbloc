package pkgmeta

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
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

// MCPCredentialsRoom is the one value of MCP.Credentials: the room the
// agent works in is the credential. The bridge gives the server the room
// relay (RoomRelayURLEnv, RoomRelayTokenEnv): a loopback address and the
// agent session's bearer, through which the server calls the gateway routes
// that act as the requester of the turn being driven. The gateway session
// never reaches the server. Stdio servers only. Everything else a tool
// needs is a setting of its own (settings.go), of any scope.
const MCPCredentialsRoom = "room"

// The room relay's variables (MCPCredentialsRoom), set by the bridge. They
// carry the reserved prefix: no setting delivers them.
const (
	RoomRelayURLEnv   = "BNBLOC_ROOM_RELAY_URL"
	RoomRelayTokenEnv = "BNBLOC_ROOM_RELAY_TOKEN"
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

// MCP bounds: a display catalog, not the server's schema.
const (
	maxMCPTools       = 64
	maxMCPToolDesc    = 1024
	maxMCPArgs        = 32
	maxMCPArgLen      = 1024
	maxMCPOAuthScopes = 32
	maxMCPOAuthScope  = 128
	maxMCPClientID    = 256
)

var (
	mcpToolNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	mcpHeaderRE   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	// mcpScopeRE is an OAuth scope token (RFC 6749 §3.3 NQCHAR).
	mcpScopeRE = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]+$`)
)

// ReservedMCPServerName is the name bnbloc's own room tools server is
// attached under in every driven session; no MCP tool component may take
// it.
const ReservedMCPServerName = "bnbloc-room"

// ValidMCPToolName is the one rule for an MCP tool component's name, the
// name content requires it by and its member settings file name: a package
// name (never a path) that is not ReservedMCPServerName. Installing a
// package, validating a requires.mcp entry and attaching a tool in the
// bridge all apply it.
func ValidMCPToolName(name string) error {
	if !ValidPackageName(name) {
		return fmt.Errorf("MCP tool name %q must use lowercase letters, digits, dot, underscore, or dash", name)
	}
	if name == ReservedMCPServerName {
		return fmt.Errorf("MCP tool name %q is reserved for the room tools", name)
	}
	return nil
}

func validateMCPPackage(pkg Package) error {
	if err := ValidMCPToolName(pkg.Name); err != nil {
		return err
	}
	if pkg.Contract != MCPContractV1 {
		return fmt.Errorf("unknown mcp contract %q", pkg.Contract)
	}
	if pkg.MCP == nil {
		return fmt.Errorf("mcp package requires mcp")
	}
	m := *pkg.MCP
	if err := validateMCPTools(m.Tools); err != nil {
		return err
	}
	if m.Credentials != "" && m.Credentials != MCPCredentialsRoom {
		return fmt.Errorf("mcp.credentials must be %q or absent, got %q (a tool's own values are settings)", MCPCredentialsRoom, m.Credentials)
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
		if err := requireGuardedEndpoint(pkg, "mcp"); err != nil {
			return err
		}
		if m.Credentials == MCPCredentialsRoom {
			// The relay is a loopback address on the member's machine and
			// its bearer acts in their rooms: never sent to a remote server.
			return fmt.Errorf(`mcp.credentials %q is for runtime "stdio" only`, MCPCredentialsRoom)
		}
		if m.Stdio != nil {
			return fmt.Errorf(`mcp runtime "endpoint" must not declare mcp.stdio`)
		}
		if err := validateMCPHTTP(m, pkg.Settings); err != nil {
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

// validateMCPHTTP checks an endpoint server's credential and that every
// setting reaches it: the server runs elsewhere, so a value it gets is a
// request header, the auth block's (a bearer or a named header carrying
// the setting it names) or a setting's own deliver.header or
// deliver.bearer.
func validateMCPHTTP(m MCP, settings []Setting) error {
	auth := MCPHTTPAuth{Scheme: MCPAuthNone}
	if m.HTTP != nil {
		auth = m.HTTP.Auth
	}
	if auth.Scheme != MCPAuthOAuth && (auth.ClientID != "" || len(auth.Scopes) > 0) {
		return fmt.Errorf(`mcp.http.auth clientId and scopes belong to scheme "oauth"`)
	}
	bearer := slices.ContainsFunc(settings, func(s Setting) bool { return s.Deliver != nil && s.Deliver.Bearer })
	switch auth.Scheme {
	case MCPAuthOAuth:
		if err := validateMCPOAuth(auth); err != nil {
			return err
		}
		if bearer {
			return fmt.Errorf(`mcp.http.auth scheme "oauth": the bearer is the member's sign-in, no setting is delivered as one`)
		}
	case MCPAuthNone:
		if auth.Setting != "" || auth.Header != "" {
			return fmt.Errorf(`mcp.http.auth scheme "none" takes no setting or header`)
		}
	case MCPAuthBearer, MCPAuthHeader:
		if auth.Scheme == MCPAuthBearer && auth.Header != "" {
			return fmt.Errorf(`mcp.http.auth scheme "bearer" takes no header (it is Authorization)`)
		}
		if auth.Scheme == MCPAuthHeader {
			if err := validMCPHeader(auth.Header); err != nil {
				return fmt.Errorf("mcp.http.auth.header: %w", err)
			}
			for _, s := range settings {
				if s.Deliver != nil && strings.EqualFold(s.Deliver.Header, auth.Header) {
					return fmt.Errorf("mcp.http.auth.header %q is also settings.%s's header", auth.Header, s.Key)
				}
			}
		}
		if auth.Scheme == MCPAuthBearer && bearer {
			return fmt.Errorf(`mcp.http.auth scheme "bearer" is the bearer: no setting is delivered as another`)
		}
		i := slices.IndexFunc(settings, func(s Setting) bool { return s.Key == auth.Setting })
		if i < 0 {
			return fmt.Errorf("mcp.http.auth.setting must name the setting carrying the credential, got %q", auth.Setting)
		}
		if settings[i].Deliver != nil {
			return fmt.Errorf("settings.%s is delivered by mcp.http.auth: it declares no deliver", auth.Setting)
		}
	default:
		return fmt.Errorf("mcp.http.auth.scheme must be none, bearer, header or oauth, got %q", auth.Scheme)
	}
	for _, s := range settings {
		if s.Key != auth.Setting && (s.Deliver == nil || (s.Deliver.Header == "" && !s.Deliver.Bearer)) {
			return fmt.Errorf("settings.%s never reaches an http MCP tool: deliver it in a header (deliver.header, deliver.bearer or mcp.http.auth.setting)", s.Key)
		}
	}
	return nil
}

// validateMCPOAuth checks an oauth endpoint: each member signs in for
// themselves, so it names no setting or header, and the optional client id
// and scopes are plain OAuth values.
func validateMCPOAuth(auth MCPHTTPAuth) error {
	if auth.Setting != "" || auth.Header != "" {
		return fmt.Errorf(`mcp.http.auth scheme "oauth" takes no setting or header (each member signs in)`)
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
