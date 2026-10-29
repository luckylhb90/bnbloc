package component

// Local egress providers: a program on the member's own computer (a
// desktop plugin, a sidecar) that answers the bridge the same two questions
// an organization's egress service answers through the gateway (which hosts
// it handles for the turn, and what to do with one request), over a
// loopback address instead of the turn proxy. The credential it injects
// stays on this computer: the bridge applies it to one request in its own
// memory and the agent only ever holds a placeholder
// (docs/prd/2026-10-27-model-credentials.md §3.3).
//
// A provider announces itself with a descriptor, written whole and replaced
// on every change:
//
//	<bridge state dir>/egress-providers/<name>/provider.json
//	{"contract":"bnbloc.egress.provider/v1","url":"http://127.0.0.1:4821","token":"…"}
//
// The bridge reads it at each turn. The provider is untrusted by the agent
// and by the platform's other parts: the bridge only asks a provider about
// the member's own runs in the sandbox (Turn.Drive is DriveOwn), accepts no host it cannot
// claim as a service could (ValidEgressHost), and never sends it a body.

// EgressProviderContract names the descriptor of a local egress provider.
const EgressProviderContract = "bnbloc.egress.provider/v1"

// EgressProviderFileName is the descriptor's file name, in the provider's
// own folder of the bridge's providers directory.
const EgressProviderFileName = "provider.json"

// EgressProviderFile is the descriptor. URL is a loopback http address
// (127.0.0.1, ::1 or localhost, with a port); Token, when set, is sent as
// the bearer of every call to it.
type EgressProviderFile struct {
	Contract string `json:"contract"`
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
}

// Paths a local provider serves (relative to its URL), called by the bridge
// with the attested ProviderTurn in TurnHeader.
const (
	// EgressProviderHostsPath answers EgressHosts for the turn (GET).
	EgressProviderHostsPath = "/egress/hosts"
	// EgressProviderRequestPath takes an EgressRequest (no body: the
	// provider never sees one) and answers an EgressResponse (POST), as
	// ServiceEgressRequestPath does for a service.
	EgressProviderRequestPath = "/egress/request"
	// EgressProviderReportPath takes an EgressUsage after a relayed request
	// ended (POST); its answer is ignored.
	EgressProviderReportPath = "/egress/report"
)

// ProviderTurn is the turn a local provider is asked about, attested by the
// bridge: the platform's Turn facts and the agent the commands run for (so a
// provider can allow a credential per agent).
type ProviderTurn struct {
	Turn
	Core     string `json:"core,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Sandboxed is always true today: a provider is asked about the member's
	// own runs in the sandbox only (the agent of a host run holds its own
	// credentials).
	Sandboxed bool `json:"sandboxed,omitempty"`
}

// EgressUsage is what a relayed request did, for the provider's counters
// and its page: never a header, a query or a body.
type EgressUsage struct {
	Host   string `json:"host"`
	Method string `json:"method"`
	Status int    `json:"status"`
	// Bytes sent back to the agent; Millis the request's whole time.
	Bytes  int64 `json:"bytes"`
	Millis int64 `json:"millis"`
	// Error is set when the answer did not complete (the agent dropped it,
	// the system stopped sending): a cancelled stream is still counted.
	Error string `json:"error,omitempty"`
}

// MaxEgressProviders bounds the local providers the bridge asks.
const MaxEgressProviders = 8

// MaxEgressRelayIdle bounds EgressSend.RelayIdleSeconds, and
// DefaultEgressRelayIdle is its default: a relayed answer ends after this
// long with no byte moving (a model that thinks), never after a total time.
const (
	MaxEgressRelayIdle     = 30 * 60
	DefaultEgressRelayIdle = 10 * 60
)
