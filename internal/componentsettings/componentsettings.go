// Package componentsettings stores the values of the settings components
// declare (docs/prd/2026-10-19-component-settings.md): one sealed record
// per component and scope, in platform settings under
// "component.settings/platform/<kind>/<name>" (the super admin's, for the
// deployment) and "component.settings/org/<tenant>/<kind>/<name>" (an
// organization admin's, for their organization). A component's name is
// unique within its kind only, so the key names both. Member values stay
// on the member's desktop.
//
// A record is sealed whole (internal/sealbox, the settings key as the
// additional data, so a row moved to another key does not open). It is
// opened to show an admin their scope's values (a secret never returned),
// into the per-job resolution of an MCP tool answered to the bridge
// holding the job's lease, and into the resolved values a member's desktop
// reads to launch a component installed for them there.
package componentsettings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const keyPrefix = "component.settings/"

// ErrOff reports a gateway without a secret store key: values cannot be
// stored (never in clear).
var ErrOff = errors.New("component settings are off: the gateway has no " + sealbox.KeyEnv)

// Record is one scope's stored values of a component.
type Record struct {
	Values      map[string]string
	UpdatedBy   string
	UpdatedAtMs int64
}

// Store reads and writes component settings.
type Store struct {
	Settings store.PlatformSettingsStore
	// Box seals the values; nil = settings are off.
	Box *sealbox.Box
	Now func() int64
}

// Available reports whether values can be stored and read.
func (s *Store) Available() bool { return s != nil && s.Box != nil && s.Settings != nil }

// Key is the platform settings key of a scope's record: tenantID names the
// organization of an org record and is ignored for a platform one.
func Key(scope, tenantID, kind, name string) (string, error) {
	switch scope {
	case pkgmeta.SettingScopePlatform:
		return keyPrefix + scope + "/" + kind + "/" + name, nil
	case pkgmeta.SettingScopeOrg:
		if tenantID == "" {
			return "", fmt.Errorf("an organization's settings need its tenant")
		}
		return keyPrefix + scope + "/" + tenantID + "/" + kind + "/" + name, nil
	}
	return "", fmt.Errorf("%s settings are not stored on the server", scope)
}

type sealedRecord struct {
	Sealed string `json:"sealed"`
}

// Get returns a scope's record of a component (found=false: none stored).
func (s *Store) Get(ctx context.Context, scope, tenantID, kind, name string) (Record, bool, error) {
	if !s.Available() {
		return Record{}, false, ErrOff
	}
	key, err := Key(scope, tenantID, kind, name)
	if err != nil {
		return Record{}, false, err
	}
	row, ok, err := s.Settings.GetPlatformSetting(ctx, key)
	if err != nil || !ok {
		return Record{}, false, err
	}
	values, err := s.open(key, row.Value)
	if err != nil {
		return Record{}, false, err
	}
	return Record{Values: values, UpdatedBy: row.UpdatedBy, UpdatedAtMs: row.UpdatedAtMs}, true, nil
}

// Update applies change to a scope's stored values of a component as one
// read-modify-write (store.UpdatePlatformSetting): a concurrent save is
// re-read and change applied again, so neither is lost. change gets the
// stored values (empty when none) and answers the values to store; it
// returns them.
func (s *Store) Update(ctx context.Context, scope, tenantID, kind, name, actor string, change func(current map[string]string) (map[string]string, error)) (map[string]string, error) {
	if !s.Available() {
		return nil, ErrOff
	}
	key, err := Key(scope, tenantID, kind, name)
	if err != nil {
		return nil, err
	}
	var next map[string]string
	var now int64
	if s.Now != nil {
		now = s.Now()
	}
	_, err = store.UpdatePlatformSetting(ctx, s.Settings, key, actor, now, func(cur string, found bool) (string, error) {
		current := map[string]string{}
		if found {
			if current, err = s.open(key, cur); err != nil {
				return "", err
			}
		}
		if next, err = change(current); err != nil {
			return "", err
		}
		plain, err := json.Marshal(next)
		if err != nil {
			return "", err
		}
		sealed, err := s.Box.Seal(plain, key)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(sealedRecord{Sealed: sealed})
		return string(raw), err
	})
	return next, err
}

// Values reads the stored values of a component's settings of the scopes
// given (platform, org) for an organization: only keys the declaration has
// in the scope stored under. With the store off nothing is stored.
func (s *Store) Values(ctx context.Context, tenantID, kind, name string, decl []pkgmeta.Setting, scopes ...string) (map[string]string, error) {
	values := map[string]string{}
	for _, scope := range scopes {
		scoped := pkgmeta.SettingsOfScope(decl, scope)
		if len(scoped) == 0 || !s.Available() {
			continue
		}
		rec, _, err := s.Get(ctx, scope, tenantID, kind, name)
		if err != nil {
			return nil, err
		}
		for _, setting := range scoped {
			if v := strings.TrimSpace(rec.Values[setting.Key]); v != "" {
				values[setting.Key] = v
			}
		}
	}
	return values, nil
}

// Resolved is what a component gets from the server for an organization:
// the stored values of its platform and organization settings (Values),
// and the required ones of those scopes without an effective value
// (pkgmeta.UnsetSettings). With the store off every required one is
// missing.
func (s *Store) Resolved(ctx context.Context, tenantID, kind, name string, decl []pkgmeta.Setting) (map[string]string, []string, error) {
	scopes := []string{pkgmeta.SettingScopePlatform, pkgmeta.SettingScopeOrg}
	values, err := s.Values(ctx, tenantID, kind, name, decl, scopes...)
	if err != nil {
		return nil, nil, err
	}
	return values, pkgmeta.UnsetSettings(decl, values, scopes...), nil
}

// orgTenants lists the organizations holding an organization record of a
// component.
func (s *Store) orgTenants(ctx context.Context, kind, name string) ([]string, error) {
	rows, err := s.Settings.ListPlatformSettings(ctx)
	if err != nil {
		return nil, err
	}
	prefix, suffix := keyPrefix+pkgmeta.SettingScopeOrg+"/", "/"+kind+"/"+name
	var tenants []string
	for _, row := range rows {
		rest, ok := strings.CutPrefix(row.Key, prefix)
		if !ok {
			continue
		}
		if tenant, ok := strings.CutSuffix(rest, suffix); ok && tenant != "" && !strings.Contains(tenant, "/") {
			tenants = append(tenants, tenant)
		}
	}
	return tenants, nil
}

// Endpoint is what an endpoint component beside the gateway is answered
// when it asks for its settings (GET /v1/component-settings): its stored
// platform values, the stored organization values of every organization
// serves admits (the install's tenant scope), the required settings of
// each not set, and a version that changes with any of them. Secrets are
// included: the component needs them. With the store off it answers
// ErrOff.
func (s *Store) Endpoint(ctx context.Context, kind, name string, decl []pkgmeta.Setting, serves func(tenantID string) bool) (wire.EndpointComponentSettings, error) {
	if !s.Available() {
		return wire.EndpointComponentSettings{}, ErrOff
	}
	out := wire.EndpointComponentSettings{
		Kind: kind, Name: name, Orgs: map[string]map[string]string{},
		Missing: wire.EndpointSettingsMissing{Orgs: map[string][]string{}},
	}
	var err error
	if out.Platform, err = s.Values(ctx, "", kind, name, decl, pkgmeta.SettingScopePlatform); err != nil {
		return wire.EndpointComponentSettings{}, err
	}
	out.Missing.Platform = nonNil(pkgmeta.UnsetSettings(decl, out.Platform, pkgmeta.SettingScopePlatform))
	if len(pkgmeta.SettingsOfScope(decl, pkgmeta.SettingScopeOrg)) > 0 {
		tenants, err := s.orgTenants(ctx, kind, name)
		if err != nil {
			return wire.EndpointComponentSettings{}, err
		}
		for _, tenant := range tenants {
			if !serves(tenant) {
				continue
			}
			values, err := s.Values(ctx, tenant, kind, name, decl, pkgmeta.SettingScopeOrg)
			if err != nil {
				return wire.EndpointComponentSettings{}, err
			}
			out.Orgs[tenant] = values
			out.Missing.Orgs[tenant] = nonNil(pkgmeta.UnsetSettings(decl, values, pkgmeta.SettingScopeOrg))
		}
	}
	// encoding/json writes map keys sorted, so equal answers hash alike.
	raw, err := json.Marshal(struct {
		Platform map[string]string
		Orgs     map[string]map[string]string
		Missing  wire.EndpointSettingsMissing
	}{out.Platform, out.Orgs, out.Missing})
	if err != nil {
		return wire.EndpointComponentSettings{}, err
	}
	sum := sha256.Sum256(raw)
	out.Version = hex.EncodeToString(sum[:16])
	return out, nil
}

func nonNil(keys []string) []string {
	if keys == nil {
		return []string{}
	}
	return keys
}

// DeleteComponent removes every scope's record of an uninstalled component,
// for every organization (works without the key: nothing is opened).
func (s *Store) DeleteComponent(ctx context.Context, kind, name string) error {
	if s == nil || s.Settings == nil {
		return nil
	}
	rows, err := s.Settings.ListPlatformSettings(ctx)
	if err != nil {
		return err
	}
	suffix := "/" + kind + "/" + name
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, keyPrefix) || !strings.HasSuffix(row.Key, suffix) {
			continue
		}
		if err := s.Settings.DeletePlatformSetting(ctx, row.Key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) open(key, raw string) (map[string]string, error) {
	var rec sealedRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	plain, err := s.Box.Open(rec.Sealed, key)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", key, err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(plain, &values); err != nil {
		return nil, fmt.Errorf("decode %s values: %w", key, err)
	}
	return values, nil
}
