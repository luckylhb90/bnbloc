package extpipe

import (
	"context"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// InstallRequest is the decoded extension publish request.
type InstallRequest struct {
	Body            []byte
	Signature       []byte
	Actor           string
	Rollout         string
	ArtifactBaseURL string
	ArtifactProbe   func(context.Context, pkgmeta.Package, string) error
}
