package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/authprovider"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/mcptool"
	"github.com/luckylhb90/bnbloc/internal/pluginruntime"
	"github.com/luckylhb90/bnbloc/internal/sandboxruntime"
	"github.com/luckylhb90/bnbloc/internal/store"
)

type coreActivation struct {
	rec extpipe.Record
	cfg extpipe.PackageConfig
}

func NewCorePipeline(s *Service, audit store.ExtensionAuditStore) *extpipe.Pipeline {
	return &extpipe.Pipeline{
		Settings:      s.PlatformSettings,
		Audit:         audit,
		AllowUnsigned: s.AllowExtUnsigned,
		Installers: map[extreg.Kind]extpipe.KindInstaller{
			core.KindCore:           s.coreInstaller(),
			extreg.KindAuthProvider: authprovider.Installer(),
			extreg.KindNotifier:     notifierInstaller(),
			pkgmeta.KindPlugin:      s.pluginInstaller(),
			extreg.KindSandbox:      sandboxruntime.Installer(),
			extreg.KindPreviewer:    s.previewerInstaller(),
			extreg.KindMCP:          mcptool.Installer(),
		},
		TrustedKeysKey: componentTrustedKeysKey,
		Now:            func() int64 { return s.nowTime().UnixMilli() },
	}
}

func (s *Service) pluginInstaller() extpipe.KindInstaller {
	return pluginruntime.Installer(s.activateGatewayPluginRecord, nil)
}

func (s *Service) activateGatewayPluginRecord(_ context.Context, _ extpipe.Record, _ pluginruntime.Config, _ extgov.Guard) (any, error) {
	return nil, nil
}

func (s *Service) nowTime() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) corePipeline() *extpipe.Pipeline {
	if s.ExtPipe != nil {
		return s.ExtPipe
	}
	p := NewCorePipeline(s, s.ExtAudit)
	s.ExtPipe = p
	return p
}

func (s *Service) coreInstaller() extpipe.KindInstaller {
	return extpipe.KindInstaller{
		Kind:         core.KindCore,
		PackageKinds: []string{pkgmeta.KindCorePackageV1},
		Decode:       decodeCorePackage,
		Probe:        s.probeCoreRecord,
		Activate:     s.activateCoreRecord,
		Deactivate:   s.deactivateCoreRecord,
	}
}

func decodeCorePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		return nil, "", err
	}
	switch pkg.Runtime {
	case pkgmeta.RuntimeEndpoint, pkgmeta.RuntimeSidecar:
		return raw, core.BackendSidecar, nil
	case pkgmeta.RuntimeManifest:
		return raw, core.BackendManifest, nil
	default:
		return nil, "", fmt.Errorf("unknown runtime %q", pkg.Runtime)
	}
}

func (s *Service) probeCoreRecord(ctx context.Context, rec extpipe.Record, guard extgov.Guard) error {
	cfg, err := decodeCoreRecord(rec)
	if err != nil {
		return err
	}
	pkg := packageFromCoreRecord(rec, cfg)
	return s.probeCorePackage(ctx, pkg, guard)
}

func (s *Service) activateCoreRecord(ctx context.Context, rec extpipe.Record, guard extgov.Guard) (any, error) {
	cfg, err := decodeCoreRecord(rec)
	if err != nil {
		return nil, err
	}
	if err := ensureCoreInstallable(rec.Name); err != nil {
		return nil, err
	}
	if cfg.Runtime == pkgmeta.RuntimeSidecar {
		return sidecarDescriptor{artifacts: cfg.Artifacts, manifest: cfg.Manifest}, nil
	}
	activation := coreActivation{rec: rec, cfg: cfg}
	provider, entry, err := s.coreInstance(ctx, activation, guard)
	if err != nil {
		return nil, err
	}
	if entry.Provider != nil {
		s.addCoreEntry(entry)
	}
	return provider, nil
}

// sidecarDescriptor is the non-nil registry instance for a sidecar core. It is
// deliberately NOT a core.Provider: the gateway never drives a sidecar core
// (the user's local desktop downloads the artifact and runs it, and the local
// bridge drives it via BNBLOC_CORE_<NAME>_URL). Registering a descriptor makes
// /v1/cores list the core as installed+available-for-clients so the desktop can
// discover it; core.New still fails for it, which is correct.
type sidecarDescriptor struct {
	artifacts map[string]pkgmeta.Artifact
	manifest  *manifestcore.Manifest
}

func (s *Service) deactivateCoreRecord(_ context.Context, rec extpipe.Record) error {
	s.dropCoreEntry(rec.Name)
	return nil
}

func decodeCoreRecord(rec extpipe.Record) (extpipe.PackageConfig, error) {
	return extpipe.DecodeConfig(rec)
}

func packageFromCoreRecord(rec extpipe.Record, cfg extpipe.PackageConfig) pkgmeta.Package {
	return cfg.Package(pkgmeta.KindCorePackageV1, rec)
}

func (s *Service) coreInstance(ctx context.Context, activation coreActivation, guard extgov.Guard) (core.Provider, coreset.Entry, error) {
	pkg := packageFromCoreRecord(activation.rec, activation.cfg)
	switch activation.cfg.Runtime {
	case pkgmeta.RuntimeEndpoint:
		return endpointCoreInstance(ctx, pkg, guard)
	case pkgmeta.RuntimeManifest:
		return manifestCoreInstance(ctx, activation, guard)
	default:
		return nil, coreset.Entry{}, fmt.Errorf("unknown runtime %q", activation.cfg.Runtime)
	}
}

func ensureCoreInstallable(name string) error {
	ext, ok := core.Describe(name)
	if ok && !ext.Removable {
		return fmt.Errorf("extreg: %s/%s is a built-in and cannot be replaced", core.KindCore, name)
	}
	return nil
}

func endpointCoreInstance(_ context.Context, pkg pkgmeta.Package, guard extgov.Guard) (core.Provider, coreset.Entry, error) {
	if pkg.Endpoint == nil {
		return nil, coreset.Entry{}, fmt.Errorf(`runtime "endpoint" requires endpoint.baseUrl`)
	}
	// The install probe already ran conformance; activation (including boot
	// rehydrate) builds a lazily-discovering client, so an endpoint that is
	// briefly down when the gateway boots stays installed and is used once it
	// answers instead of vanishing until the next restart.
	ep := core.Endpoint{BaseURL: pkg.Endpoint.BaseURL}
	provider := protocol.NewClient(pkg.Name, governedHTTPClient(guard))
	return provider, coreset.Entry{Name: pkg.Name, Provider: provider, Endpoint: ep, Explicit: true}, nil
}

func manifestCoreInstance(ctx context.Context, activation coreActivation, guard extgov.Guard) (core.Provider, coreset.Entry, error) {
	pkg := packageFromCoreRecord(activation.rec, activation.cfg)
	provider, err := manifestcore.New(*pkg.Manifest)
	if err != nil {
		return nil, coreset.Entry{}, err
	}
	provider.HTTP = governedHTTPClient(guard)
	ep := core.Endpoint{}
	if d, ok := any(provider).(core.Discoverer); ok {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if found, ok := d.Discover(dctx); ok {
			ep = found
		}
		cancel()
	}
	return provider, coreset.Entry{Name: pkg.Name, Provider: provider, Endpoint: ep}, nil
}

func governedHTTPClient(guard extgov.Guard) *http.Client {
	return &http.Client{Transport: guard.Net.RoundTripper(nil)}
}
