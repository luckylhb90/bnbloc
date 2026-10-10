// Package authprovider is the auth-provider component kind: an endpoint
// component (internal/endpointpkg) the gateway activates for enterprise
// sign-in. Its contract probe is GET /v1/auth-provider/health then
// GET /v1/auth-provider/discover.
package authprovider

import (
	"context"
	"encoding/json"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// Config is an auth-provider's persisted config: the shared package
// projection every extension kind stores (extpipe.PackageConfig).
// Auth-providers use runtime, contract, endpoint and permissions.
type Config = extpipe.PackageConfig

// Endpoint is an activated auth-provider: the shared endpoint-component
// shape with the auth-provider host methods.
type Endpoint endpointpkg.Endpoint

// Kind is the auth-provider's endpoint-component description.
var Kind = endpointpkg.Kind{
	Kind:         extreg.KindAuthProvider,
	PackageKind:  pkgmeta.KindAuthProviderPackageV1,
	Label:        "auth-provider",
	Contract:     pkgmeta.AuthProviderContractV1,
	HealthPath:   "/v1/auth-provider/health",
	DiscoverPath: "/v1/auth-provider/discover",
	Instance:     func(e endpointpkg.Endpoint) any { return Endpoint(e) },
}

func Installer() extpipe.KindInstaller { return Kind.Installer() }

// DecodePackage projects a verified auth-provider package onto its config.
func DecodePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	return endpointpkg.DecodePackage(pkg)
}

// DecodeRecord decodes an auth-provider record's persisted config.
func DecodeRecord(rec extpipe.Record) (Config, error) {
	return extpipe.DecodeConfig(rec)
}

func ProbeRecord(ctx context.Context, rec extpipe.Record, guard extgov.Guard) error {
	return Kind.Probe(ctx, rec, guard)
}

func ActivateRecord(ctx context.Context, rec extpipe.Record, guard extgov.Guard) (any, error) {
	return Kind.Activate(ctx, rec, guard)
}

func NewEndpoint(rec extpipe.Record, cfg Config, guard extgov.Guard) Endpoint {
	return Endpoint(Kind.NewEndpoint(rec, cfg, guard))
}

func (s Endpoint) Available(ctx context.Context) bool {
	return Kind.ProbeEndpoint(ctx, endpointpkg.Endpoint(s)) == nil
}

func (s Endpoint) AuthProviderName() string { return s.Name }

func (s Endpoint) AuthProviderContract() string { return pkgmeta.AuthProviderContractV1 }

// Assertion is the auth-provider contract's ACS result: the JSON body a
// provider answers POST /v1/auth-provider/acs with (Accept: application/json)
// once it verified the IdP response. Subject (else Email) is the identity.
// Groups, Department and DisplayName are optional directory attributes; when
// present the gateway applies the organization-sync rules to them at sign-in
// (internal/orgsync).
type Assertion struct {
	Subject     string   `json:"subject"`
	Email       string   `json:"email,omitempty"`
	Relay       string   `json:"relay,omitempty"`
	Groups      []string `json:"groups,omitempty"`
	Department  string   `json:"department,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
}
