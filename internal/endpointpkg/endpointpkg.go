// Package endpointpkg is the shared install path of endpoint components:
// packages whose runtime is "endpoint" — an externally supervised HTTP
// service at endpoint.baseUrl that bnbloc reaches only under the package's
// permissions.network allowlist (extgov.NetGuard). Every such kind
// (auth-provider, notifier) decodes the shared PackageConfig, validates
// runtime/contract/baseUrl, checks the base URL against the allowlist, probes
// the endpoint's discover document for its contract, and activates a guarded
// HTTP client for it. A kind only states its label, contract and probe paths.
package endpointpkg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// DefaultTimeout bounds every call through an endpoint's client unless the
// kind sets its own.
const DefaultTimeout = 3 * time.Second

// probeTimeout bounds one install/activation probe (health + discover).
const probeTimeout = 3 * time.Second

// Kind describes one endpoint component kind.
type Kind struct {
	Kind        extreg.Kind
	PackageKind string
	// Label names the kind in every error ("auth-provider", "notifier").
	Label    string
	Contract string
	// HealthPath, when set, must answer 200 before discover is consulted.
	HealthPath string
	// DiscoverPath answers {name, contract}; contract must equal Contract.
	// Both paths are appended to the (slash-trimmed) base URL, so a base URL
	// with a path prefix (http://host:port/prefix) works unchanged.
	DiscoverPath string
	// Timeout bounds every call through the endpoint's client (0 = DefaultTimeout).
	Timeout time.Duration
	// Discover, when set, replaces the health + discover document probe for
	// a kind whose endpoints speak a foreign protocol with its own discovery
	// (an MCP server answers initialize, not a bnbloc discover document). It
	// runs under the same probe timeout and network guard.
	Discover func(ctx context.Context, endpoint Endpoint) error
	// Instance wraps an activated endpoint into the registry instance the
	// kind's host type-asserts (nil = the Endpoint itself).
	Instance func(Endpoint) any
}

// Endpoint is one activated endpoint component: its install name, base URL
// (no trailing slash) and an HTTP client held to permissions.network.
type Endpoint struct {
	Name    string
	BaseURL string
	Client  *http.Client
}

type discoverResponse struct {
	Name     string `json:"name"`
	Contract string `json:"contract"`
}

// Installer returns the kind's extpipe lifecycle vtable.
func (k Kind) Installer() extpipe.KindInstaller {
	return extpipe.KindInstaller{
		Kind:         k.Kind,
		PackageKinds: []string{k.PackageKind},
		Decode:       DecodePackage,
		Probe:        k.Probe,
		Activate:     k.Activate,
	}
}

// DecodePackage projects a verified endpoint package onto its config.
func DecodePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		return nil, "", err
	}
	return raw, extreg.BackendSidecar, nil
}

// Probe validates a record and checks its live endpoint.
func (k Kind) Probe(ctx context.Context, rec extpipe.Record, guard extgov.Guard) error {
	endpoint, err := k.Endpoint(rec, guard)
	if err != nil {
		return err
	}
	return k.ProbeEndpoint(ctx, endpoint)
}

// Activate validates and probes a record, returning its registry instance.
func (k Kind) Activate(ctx context.Context, rec extpipe.Record, guard extgov.Guard) (any, error) {
	endpoint, err := k.Endpoint(rec, guard)
	if err != nil {
		return nil, err
	}
	if err := k.ProbeEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	if k.Instance != nil {
		return k.Instance(endpoint), nil
	}
	return endpoint, nil
}

// Endpoint decodes and validates a record and builds its guarded endpoint,
// without touching the network.
func (k Kind) Endpoint(rec extpipe.Record, guard extgov.Guard) (Endpoint, error) {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return Endpoint{}, err
	}
	if err := k.Validate(cfg, guard); err != nil {
		return Endpoint{}, err
	}
	return k.NewEndpoint(rec, cfg, guard), nil
}

// Validate applies the endpoint rules to a decoded config: runtime
// "endpoint", the kind's contract, a base URL, and that URL's host in
// permissions.network.
func (k Kind) Validate(cfg extpipe.PackageConfig, guard extgov.Guard) error {
	if cfg.Runtime != pkgmeta.RuntimeEndpoint {
		return fmt.Errorf(`%s runtime must be "endpoint"`, k.Label)
	}
	if cfg.Contract != k.Contract {
		return fmt.Errorf("unknown %s contract %q", k.Label, cfg.Contract)
	}
	if cfg.Endpoint == nil || strings.TrimSpace(cfg.Endpoint.BaseURL) == "" {
		return fmt.Errorf("%s endpoint requires endpoint.baseUrl", k.Label)
	}
	if !guard.Net.AllowURL(cfg.Endpoint.BaseURL) {
		return fmt.Errorf("%s endpoint host is not in permissions.network", k.Label)
	}
	return nil
}

// NewEndpoint builds the guarded endpoint for a validated config.
func (k Kind) NewEndpoint(rec extpipe.Record, cfg extpipe.PackageConfig, guard extgov.Guard) Endpoint {
	timeout := k.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return Endpoint{
		Name:    rec.Name,
		BaseURL: strings.TrimRight(strings.TrimSpace(cfg.Endpoint.BaseURL), "/"),
		Client:  &http.Client{Transport: guard.Net.RoundTripper(nil), Timeout: timeout},
	}
}

// ProbeEndpoint checks health (when the kind has a health path) and that the
// discover document advertises the kind's contract.
func (k Kind) ProbeEndpoint(ctx context.Context, endpoint Endpoint) error {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if k.Discover != nil {
		return k.Discover(probeCtx, endpoint)
	}
	if k.HealthPath != "" {
		if err := k.getOK(probeCtx, endpoint, k.HealthPath); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint.BaseURL+k.DiscoverPath, nil)
	if err != nil {
		return fmt.Errorf("%s discover request invalid: %w", k.Label, err)
	}
	resp, err := endpoint.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%s discover probe failed: %w", k.Label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s discover probe status %d", k.Label, resp.StatusCode)
	}
	var body discoverResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("%s discover response invalid: %w", k.Label, err)
	}
	if body.Contract != k.Contract {
		return fmt.Errorf("%s discover contract = %q", k.Label, body.Contract)
	}
	return nil
}

func (k Kind) getOK(ctx context.Context, endpoint Endpoint, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("%s probe request invalid: %w", k.Label, err)
	}
	resp, err := endpoint.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%s health probe failed: %w", k.Label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s health probe status %d", k.Label, resp.StatusCode)
	}
	return nil
}
