package wire

// ServerEgress is GET/PUT /v1/platform/server-egress: where the platform's
// own server-side requests may go (today the pages an assist reads,
// docs/architecture/server-egress.md). Mode public (the default): any
// host on a public address, and the listed hosts on any address (an
// external name the deployment's egress proxy answers on a private one);
// allowlist: the listed hosts only, and no other name is looked up.
type ServerEgress struct {
	Mode        string   `json:"mode"`
	Hosts       []string `json:"hosts"`
	UpdatedAtMs int64    `json:"updatedAtMs,omitempty"`
	UpdatedBy   string   `json:"updatedBy,omitempty"`
}

// Server egress modes.
const (
	ServerEgressPublic    = "public"
	ServerEgressAllowlist = "allowlist"
)

// MaxServerEgressHosts bounds ServerEgress.Hosts.
const MaxServerEgressHosts = 200
