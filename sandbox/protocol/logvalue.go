package protocol

import (
	"log/slog"

	"github.com/luckylhb90/bnbloc/core"
)

// The wire types that carry a secret (an exec token, a secret binding's
// value, exec and space environment) log through core.LogRedacted, the one
// redaction rule.

func (t ExecToken) LogValue() slog.Value            { return core.LogRedacted(t) }
func (s SecretRequest) LogValue() slog.Value        { return core.LogRedacted(s) }
func (r CreateMachineRequest) LogValue() slog.Value { return core.LogRedacted(r) }
func (r SpaceRequest) LogValue() slog.Value         { return core.LogRedacted(r) }
func (s Space) LogValue() slog.Value                { return core.LogRedacted(s) }
func (r ListSpacesResponse) LogValue() slog.Value   { return core.LogRedacted(r) }
func (r ExecRequest) LogValue() slog.Value          { return core.LogRedacted(r) }
