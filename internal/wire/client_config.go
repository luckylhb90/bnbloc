package wire

// ClientConfig is GET /v1/public/client-config: the deployment facts a client
// needs before it signs in. SitesOrigin is the separate origin serving
// every site and its app API (BNBLOC_SITES_ORIGIN, e.g.
// https://sites.bnbloc.example.com), empty when sites are served from the
// gateway's own origin; the desktop shell's site scheme forwards there.
type ClientConfig struct {
	SitesOrigin string `json:"sitesOrigin,omitempty"`
}
