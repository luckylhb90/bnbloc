package manifestcore

import (
	"encoding/json"
	"fmt"
	"github.com/luckylhb90/bnbloc/core"
	"strings"
)

// Manifest is the declarative description of one REST-daemon core.
type Manifest struct {
	// Comment is an ignored free-text field ("_comment") so a manifest may carry
	// a provenance/marker note (e.g. an internal-mirror marker) and still pass
	// the strict package decoder that embeds it.
	Comment       string      `json:"_comment,omitempty"`
	Name          string      `json:"name"`
	Discover      *DiscoverOp `json:"discover,omitempty"`
	CreateWksp    Op          `json:"createWorkspace"`
	CreateSession Op          `json:"createSession"`
	SendInput     Op          `json:"sendInput"`
	WriteFile     Op          `json:"writeWorkspaceFile,omitempty"`
	DeleteWksp    Op          `json:"deleteWorkspace,omitempty"`
	Interrupt     Op          `json:"interrupt,omitempty"`
	ListTargets   Op          `json:"listTargets"`
	// Messages reads the session's replies through items (the reflow
	// path); PollTurn reports the active turn through idleWhen (plus the
	// failed/failMessage/failCode extracts). Both are required: a core
	// whose replies cannot be read would time every turn out silently.
	Messages Op `json:"messages"`
	PollTurn Op `json:"pollTurn"`
	// ListSessionFiles + ReadSessionFile (declared together) offer
	// core.SessionFiles: the agent session's workspace files, which the bridge
	// publishes as room artifacts. listSessionFiles maps its list through
	// items (fields path, bytes, mtimeMs); readSessionFile takes {relPath} and
	// extracts content (text) or contentBase64 (exact bytes: binary-safe,
	// reported as core.CapabilitySessionFileBytes).
	ListSessionFiles Op `json:"listSessionFiles,omitempty"`
	ReadSessionFile  Op `json:"readSessionFile,omitempty"`
	// SessionWorkdir offers core.SessionWorkdirReporter: extract.workdir is the
	// session's working directory on the core host (the default live-sync dir).
	SessionWorkdir Op `json:"sessionWorkdir,omitempty"`
	// TargetIDPrefix, when set, lets the provider derive a target id from a
	// provider name without a network call (prefix "local:" resolves "codex"
	// to "local:codex"). Empty = no target resolution.
	TargetIDPrefix string `json:"targetIdPrefix,omitempty"`
}

// Op is one HTTP call template.
type Op struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	PathWhen map[string]string `json:"pathWhen,omitempty"`
	Body     json.RawMessage   `json:"body,omitempty"`
	Extract  map[string]string `json:"extract,omitempty"`
	Targets  *TargetsSpec      `json:"targets,omitempty"`
	Items    *ItemsSpec        `json:"items,omitempty"`
	IdleWhen *IdlePredicate    `json:"idleWhen,omitempty"`
}

// declared reports whether the manifest configures this op at all. Optional
// ops (interrupt, writeWorkspaceFile, deleteWorkspace, listSessionFiles,
// readSessionFile, sessionWorkdir) are absent from manifests that do not
// support them.
func (o Op) declared() bool { return o.Method != "" && o.Path != "" }

// TargetsSpec shapes a list response into targets.
type TargetsSpec struct {
	List     string `json:"list"`
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// EnabledWhen keeps only list elements whose field equals a value (a daemon
	// may list disabled targets too; they must not be offered).
	EnabledWhen *EqualsPredicate `json:"enabledWhen,omitempty"`
	// Enrich runs one secondary call per listed target to fill its
	// model/reasoning catalog (e.g. a per-provider composer-options call).
	Enrich *EnrichSpec `json:"enrich,omitempty"`
	// MCPTransports declares the MCP transports every listed target's agent
	// accepts in a session profile ("stdio", "http", "sse"); a list
	// element's own "mcpTransports" array wins. Empty = stdio only.
	MCPTransports []string `json:"mcpTransports,omitempty"`
}

// ItemsSpec shapes a list response into typed records: reflow messages
// (messages op) or session files (listSessionFiles op). Fields maps each
// record field to a dotted path within one list element.
type ItemsSpec struct {
	List          string              `json:"list"`
	Fields        map[string]string   `json:"fields"`
	CompletedWhen *EqualsAnyPredicate `json:"completedWhen,omitempty"`
}

// EqualsAnyPredicate reports true when Field equals one of EqualsAnyOf.
type EqualsAnyPredicate struct {
	Field       string   `json:"field"`
	EqualsAnyOf []string `json:"equalsAnyOf"`
}

// EqualsPredicate reports true when Field equals Equals (bool, number, or
// string — compared after stringification, matching JSON-path coercion).
type EqualsPredicate struct {
	Field  string `json:"field"`
	Equals any    `json:"equals"`
}

// IdlePredicate reads Field and reports idle when its value is NOT in NotIn.
type IdlePredicate struct {
	Field string   `json:"field"`
	NotIn []string `json:"notIn"`
	// UnlessSet, when named, reports idle whenever that field is present
	// (non-null) regardless of Field: a turn that has settled is idle even
	// while its phase still reads as working.
	UnlessSet string `json:"unlessSet,omitempty"`
}

// DiscoverOp locates the daemon. Exactly one form is used: a fixed-URL probe
// (probeUrl+probePath, 200 = found) or a listener descriptor file (listener),
// a JSON file the daemon rewrites each run with its random address and token.
type DiscoverOp struct {
	ProbeURL    string           `json:"probeUrl,omitempty"`
	ProbePath   string           `json:"probePath,omitempty"`
	Listener    *ListenerSpec    `json:"listener,omitempty"`
	HostRewrite *HostRewriteSpec `json:"hostRewrite,omitempty"`
}

// ListenerSpec reads a daemon listener descriptor. Its path is the value of
// the env var PathEnv names when that is set and non-empty, else DefaultPath
// (a leading "~/" expands to the user's home directory), so a host-side caller
// finds the daemon from the manifest alone while an operator can still repoint
// it. AddrField/TokenField are dotted JSON paths into the descriptor.
type ListenerSpec struct {
	PathEnv     string `json:"pathEnv,omitempty"`
	DefaultPath string `json:"defaultPath,omitempty"`
	AddrField   string `json:"addrField"`
	TokenField  string `json:"tokenField,omitempty"`
}

// HostRewriteSpec replaces a loopback host in a discovered address so a
// containerized caller can reach the host daemon. Env names an override
// variable (set-to-empty disables the rewrite); Default applies when the
// variable is unset.
type HostRewriteSpec struct {
	Env     string `json:"env"`
	Default string `json:"default"`
}

// EnrichSpec runs one secondary call per listed target to fill its catalog.
// The call is templated with {provider} and {targetId}; failures are
// best-effort (the target keeps its identity, just without a catalog).
type EnrichSpec struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
	// Fields maps catalog scalars by target field name ("defaultModel",
	// "defaultReasoning"); a value may list fallback paths separated by "|",
	// first non-empty wins.
	Fields map[string]string `json:"fields,omitempty"`
	// Lists maps catalog string arrays by target field name ("models",
	// "reasoningEfforts") from an array of objects.
	Lists map[string]ListProj `json:"lists,omitempty"`
	// Maps maps the per-model reasoning table ("reasoningByModel") from an
	// object whose values each carry an array of objects.
	Maps map[string]MapProj `json:"maps,omitempty"`
}

// ListProj extracts a []string from the array of objects at Path, taking Value
// from each element. When is a narrow per-list gate (e.g. offer models only
// when modelConfig.configurable is true).
type ListProj struct {
	Path  string           `json:"path"`
	Value string           `json:"value"`
	When  *EqualsPredicate `json:"when,omitempty"`
}

// MapProj extracts map[string][]string from the object at Path: each value is
// an object whose List field holds the array, and Value names each element's
// string field.
type MapProj struct {
	Path  string `json:"path"`
	List  string `json:"list"`
	Value string `json:"value"`
}

// New builds a Provider from a parsed manifest.
func New(m Manifest) (*Provider, error) {
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("manifestcore: manifest has no name")
	}
	for label, op := range map[string]Op{
		"createWorkspace": m.CreateWksp, "createSession": m.CreateSession,
		"sendInput": m.SendInput, "listTargets": m.ListTargets,
		"messages": m.Messages, "pollTurn": m.PollTurn,
	} {
		if strings.TrimSpace(op.Method) == "" || strings.TrimSpace(op.Path) == "" {
			return nil, fmt.Errorf("manifestcore %q: verb %q missing method/path", m.Name, label)
		}
	}
	if it := m.Messages.Items; it == nil || strings.TrimSpace(it.List) == "" || strings.TrimSpace(it.Fields["text"]) == "" {
		return nil, fmt.Errorf("manifestcore %q: messages requires items.list and items.fields.text", m.Name)
	}
	if err := validateSessionFileOps(m); err != nil {
		return nil, err
	}
	if t := m.ListTargets.Targets; t != nil {
		for _, tr := range t.MCPTransports {
			if tr != core.MCPStdio && tr != core.MCPHTTP && tr != core.MCPSSE {
				return nil, fmt.Errorf("manifestcore %q: listTargets.targets.mcpTransports: unknown transport %q", m.Name, tr)
			}
		}
	}
	return &Provider{m: m}, nil
}

// validateSessionFileOps rejects a half-declared optional capability at load
// rather than at first use: session files need both ops (listing without
// reading publishes nothing), and each op needs the mapping its result is read
// through.
func validateSessionFileOps(m Manifest) error {
	if m.ListSessionFiles.declared() != m.ReadSessionFile.declared() {
		return fmt.Errorf("manifestcore %q: listSessionFiles and readSessionFile must be declared together", m.Name)
	}
	if m.ListSessionFiles.declared() {
		if it := m.ListSessionFiles.Items; it == nil || strings.TrimSpace(it.List) == "" || strings.TrimSpace(it.Fields["path"]) == "" {
			return fmt.Errorf("manifestcore %q: listSessionFiles requires items.list and items.fields.path", m.Name)
		}
		if strings.TrimSpace(m.ReadSessionFile.extractPath("content")) == "" && strings.TrimSpace(m.ReadSessionFile.extractPath("contentBase64")) == "" {
			return fmt.Errorf("manifestcore %q: readSessionFile requires extract.content or extract.contentBase64", m.Name)
		}
	}
	if m.SessionWorkdir.declared() && strings.TrimSpace(m.SessionWorkdir.extractPath("workdir")) == "" {
		return fmt.Errorf("manifestcore %q: sessionWorkdir requires extract.workdir", m.Name)
	}
	return nil
}

// Parse decodes a manifest from JSON bytes and validates it.
func Parse(raw []byte) (*Provider, error) {
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	return New(m)
}

// ParseManifest decodes and validates a manifest, returning the value (not a
// Provider) for callers that need to persist or re-marshal the manifest.
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifestcore: invalid manifest json: %w", err)
	}
	if _, err := New(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
