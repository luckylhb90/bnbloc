package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/conformance"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extgov"
)

func (s *Service) probeCorePackage(ctx context.Context, pkg pkgmeta.Package, guard extgov.Guard) error {
	switch pkg.Runtime {
	case pkgmeta.RuntimeEndpoint:
		ep := core.Endpoint{BaseURL: pkg.Endpoint.BaseURL}
		if err := conformance.CheckLive(ctx, conformance.Config{Endpoint: ep, Timeout: 5 * time.Second, HTTPClient: governedHTTPClient(guard)}); err != nil {
			return fmt.Errorf("conformance probe failed: %w", err)
		}
		return nil
	case pkgmeta.RuntimeManifest:
		return s.probeManifestCorePackage(ctx, pkg, guard)
	case pkgmeta.RuntimeSidecar:
		return nil
	default:
		return fmt.Errorf("unknown runtime %q", pkg.Runtime)
	}
}

func (s *Service) probeManifestCorePackage(ctx context.Context, pkg pkgmeta.Package, guard extgov.Guard) error {
	provider, err := manifestcore.New(*pkg.Manifest)
	if err != nil {
		return err
	}
	provider.HTTP = governedHTTPClient(guard)
	// The probe reaches the daemon activation will drive: the same entry.
	ep := coreset.EntryFor(pkg.Name, provider, declaredEndpoint(pkg)).Endpoint
	if !ep.OK() {
		// A listener-discovery manifest points at a daemon on the USER's
		// machine (random port+token per run, addressed by a local descriptor
		// file): no central probe can reach it, so requiring one would make
		// every per-machine core uninstallable. Manifest validation above is
		// the whole install gate for that form. Probe-based manifests stay
		// strict — a probe URL that resolves to nothing is a broken install.
		if pkg.Endpoint == nil && listenerOnlyDiscover(pkg.Manifest.Discover) {
			return nil
		}
		return fmt.Errorf("conformance probe requires endpoint.baseUrl or manifest discover probe")
	}
	if err := conformance.CheckProvider(ctx, provider, conformance.Config{Endpoint: ep, Timeout: 5 * time.Second, HTTPClient: governedHTTPClient(guard)}); err != nil {
		return fmt.Errorf("conformance probe failed: %w", err)
	}
	return nil
}

// listenerOnlyDiscover reports a discover clause that can only locate the
// daemon through a local listener descriptor (no fixed probe URL).
func listenerOnlyDiscover(d *manifestcore.DiscoverOp) bool {
	return d != nil && d.Listener != nil && strings.TrimSpace(d.ProbeURL) == ""
}
