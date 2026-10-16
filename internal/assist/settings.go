package assist

import (
	"context"
	"errors"
	"fmt"

	"github.com/luckylhb90/bnbloc/internal/modelsettings"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The assist model's settings: the platform's model and an organization's
// own, each one platform setting, its key sealed (internal/sealbox, the
// settings key as additional data, so a row moved to another organization
// does not open) and never returned. Without the secret store key no model
// can be configured (a key is never stored in clear), and a stored one is
// not opened.
//
// The platform's model carries OrgHosts: the hosts an organization's own
// model may name (none by default), so an org admin cannot point the
// gateway at an arbitrary address. An organization's model replaces the
// platform's for its members while its host stays allowed (Effective).
//
// The rules themselves (base URL, model, sealed key, org hosts, the
// effective/allowed-host resolution) are owned by internal/modelsettings,
// extracted from this file when media providers grew the same shape
// (docs/prd/2026-10-16-voice-and-image-generation.md §2); this type
// projects them onto the assist model's wire shapes.

// settingKey is the platform setting holding the platform's model; an
// organization's is settingKey + "/" + its tenant id.
const settingKey = "assist.model"

// ErrInvalidSettings wraps a refused update (its message says what to fix).
var ErrInvalidSettings = errors.New("invalid assist model")

// ErrSettingsOff: the gateway has no secret store key.
var ErrSettingsOff = modelsettings.ErrSettingsOff

// Settings reads and writes one assist model: the platform's (Tenant "")
// or an organization's.
type Settings struct {
	Store store.PlatformSettingsStore
	// Box seals the key; nil = the model cannot be configured.
	Box *sealbox.Box
	Now func() int64
	// Tenant names the organization whose model this is ("" = the
	// platform's).
	Tenant string
}

// inner is the modelsettings view of s.
func (s *Settings) inner() *modelsettings.Settings {
	return &modelsettings.Settings{Store: s.Store, Box: s.Box, Now: s.Now, Tenant: s.Tenant, Key: settingKey}
}

// Platform is the platform's model settings beside s.
func (s *Settings) Platform() *Settings {
	return &Settings{Store: s.Store, Box: s.Box, Now: s.Now}
}

// Org is an organization's model settings beside s.
func (s *Settings) Org(tenant string) *Settings {
	return &Settings{Store: s.Store, Box: s.Box, Now: s.Now, Tenant: tenant}
}

// Configurable reports whether the model can be stored and read.
func (s *Settings) Configurable() bool { return s.inner().Configurable() }

// Model returns the stored model with its key opened. A model is
// unconfigured (Configured false) when unset or the settings are off.
func (s *Settings) Model(ctx context.Context) (Model, error) {
	m, err := s.inner().Model(ctx)
	return Model(m), err
}

// View is the model as the platform console shows it: never the key.
func (s *Settings) View(ctx context.Context) (wire.AssistModelConfig, error) {
	v, err := s.inner().View(ctx)
	return assistView(v), err
}

// assistView projects a modelsettings view onto the assist wire shape.
func assistView(v modelsettings.View) wire.AssistModelConfig {
	out := wire.AssistModelConfig{Configurable: v.Configurable, BaseURL: v.BaseURL, Model: v.Model,
		KeySet: v.KeySet, OrgHosts: v.OrgHosts, UpdatedBy: v.UpdatedBy, UpdatedAtMs: v.UpdatedAtMs}
	if out.OrgHosts == nil {
		out.OrgHosts = []string{}
	}
	return out
}

// Put stores an update: an empty key keeps the stored one, ClearKey
// removes it, an empty base URL turns the model off (and forgets it; an
// organization's members go back to the platform's). OrgHosts is the
// platform's to set; an organization's base URL must name one of them.
func (s *Settings) Put(ctx context.Context, u wire.AssistModelUpdate, actor string) (wire.AssistModelConfig, error) {
	v, err := s.inner().Put(ctx, modelsettings.Update{BaseURL: u.BaseURL, Model: u.Model, APIKey: u.APIKey,
		ClearKey: u.ClearKey, OrgHosts: u.OrgHosts}, actor)
	switch {
	case err == nil:
		return assistView(v), nil
	case errors.As(err, &modelsettings.InvalidError{}):
		// A refused update (modelsettings.Validate and its org-host rules),
		// wrapped so the caller answers it as a 400.
		return wire.AssistModelConfig{}, fmt.Errorf("%w: %s", ErrInvalidSettings, err)
	default:
		return wire.AssistModelConfig{}, err
	}
}

// AllowedHosts are the hosts an organization's own model may name: the
// platform model's OrgHosts (none when the platform has no model).
func (s *Settings) AllowedHosts(ctx context.Context) ([]string, error) {
	return s.inner().AllowedHosts(ctx)
}

// Effective is the model an organization's members run on: its own while
// set and its host still allowed (Model.Org), else the platform's.
func (s *Settings) Effective(ctx context.Context, tenant string) (Model, error) {
	m, err := s.inner().Effective(ctx, tenant)
	return Model(m), err
}
