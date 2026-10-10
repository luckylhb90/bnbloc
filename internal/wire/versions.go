package wire

// ServiceVersion is one running service instance's build identity, served
// by every service at GET /version (internal/obs) and aggregated by the
// gateway at GET /v1/platform/versions.
type ServiceVersion struct {
	// Service is the process name (bnbloc-gateway, bnbloc-room, ...).
	Service string `json:"service"`
	// Commit is the source commit the binary was built from ("" = unknown:
	// built without CI_COMMIT_SHA outside a git checkout).
	Commit string `json:"commit"`
	// BuiltAt is the build time (RFC 3339, UTC), "" when not stamped.
	BuiltAt string `json:"builtAt,omitempty"`
	// Instance is the host name of the answering process (the pod name
	// under Kubernetes), so replicas behind one Service DNS name differ.
	Instance string `json:"instance"`
}

// WebBundleVersion is the build identity of the web bundle a gateway embeds.
type WebBundleVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// UnreachableService is a service whose /version could not be read.
type UnreachableService struct {
	Service string `json:"service"`
	Error   string `json:"error"`
}

// DeploymentVersions is GET /v1/platform/versions (super-admin): every
// reachable instance of every service the gateway addresses, plus the
// gateway's own and its embedded web bundle. Consistent is false when known
// commits differ, a service is unreachable, or the web identity is missing
// (Problems says why). A component built without a commit stamp cannot be
// compared: it is a Warning (unverifiable), not skew.
type DeploymentVersions struct {
	Services    []ServiceVersion     `json:"services"`
	Web         *WebBundleVersion    `json:"web,omitempty"`
	Unreachable []UnreachableService `json:"unreachable,omitempty"`
	Consistent  bool                 `json:"consistent"`
	Problems    []string             `json:"problems,omitempty"`
	Warnings    []string             `json:"warnings,omitempty"`
}
