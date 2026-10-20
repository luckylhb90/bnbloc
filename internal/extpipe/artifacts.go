package extpipe

import (
	"context"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

func validateArtifactContract(ctx context.Context, pkg pkgmeta.Package, artifactBaseURL string, artifactProbe func(context.Context, pkgmeta.Package, string) error) error {
	if len(pkg.Pins()) == 0 {
		return nil
	}
	if err := pkgmeta.ValidateCompleteArtifacts(pkg); err != nil {
		return err
	}
	if strings.TrimSpace(artifactBaseURL) == "" {
		return nil
	}
	if err := pkgmeta.ValidateGatewayArtifactURLs(pkg, artifactBaseURL); err != nil {
		return err
	}
	if artifactProbe != nil {
		return artifactProbe(ctx, pkg, artifactBaseURL)
	}
	return nil
}
