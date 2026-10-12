package mcptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Organization connectors: the values of an org-credential MCP tool
// (credentials "org"), one connector per (organization, component),
// configured by an org admin. They are sealed (internal/sealbox, the
// settings key as additional data, so a row moved to another organization
// does not open) and stored in platform settings under
// "mcp.connector/<tenant>/<component>". They are opened only to list them
// for the organization's admins (secret values never returned) and into the
// effective resolution of a job in that organization, answered to the bridge
// holding the job's lease.

const connectorKeyPrefix = "mcp.connector/"

// maxConnectorValue bounds one value (a key or token, never a document).
const maxConnectorValue = 4 << 10

// ErrConnectorsOff reports a gateway without a secret store key: org
// connectors cannot be stored (never in clear).
var ErrConnectorsOff = errors.New("organization connectors are off: the gateway has no " + sealbox.KeyEnv)

// Connector is one organization's stored values for a component.
type Connector struct {
	Values      map[string]string
	UpdatedBy   string
	UpdatedAtMs int64
}

// Connectors reads and writes organization connectors.
type Connectors struct {
	Settings store.PlatformSettingsStore
	// Box seals the values; nil = connectors are off.
	Box *sealbox.Box
	Now func() int64
}

// Available reports whether connectors can be stored and read.
func (c *Connectors) Available() bool { return c != nil && c.Box != nil && c.Settings != nil }

func connectorKey(tenantID, name string) string { return connectorKeyPrefix + tenantID + "/" + name }

type sealedConnector struct {
	Sealed string `json:"sealed"`
}

// Get returns an organization's connector for a component.
func (c *Connectors) Get(ctx context.Context, tenantID, name string) (Connector, bool, error) {
	if !c.Available() {
		return Connector{}, false, ErrConnectorsOff
	}
	key := connectorKey(tenantID, name)
	row, ok, err := c.Settings.GetPlatformSetting(ctx, key)
	if err != nil || !ok {
		return Connector{}, false, err
	}
	var sc sealedConnector
	if err := json.Unmarshal([]byte(row.Value), &sc); err != nil {
		return Connector{}, false, fmt.Errorf("decode connector %s: %w", name, err)
	}
	plain, err := c.Box.Open(sc.Sealed, key)
	if err != nil {
		return Connector{}, false, fmt.Errorf("open connector %s: %w", name, err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(plain, &values); err != nil {
		return Connector{}, false, fmt.Errorf("decode connector %s values: %w", name, err)
	}
	return Connector{Values: values, UpdatedBy: row.UpdatedBy, UpdatedAtMs: row.UpdatedAtMs}, true, nil
}

// Put stores an organization's values for a component (already merged and
// checked against its env, MergeValues).
func (c *Connectors) Put(ctx context.Context, tenantID, name string, values map[string]string, actor string) error {
	if !c.Available() {
		return ErrConnectorsOff
	}
	key := connectorKey(tenantID, name)
	plain, err := json.Marshal(values)
	if err != nil {
		return err
	}
	sealed, err := c.Box.Seal(plain, key)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sealedConnector{Sealed: sealed})
	if err != nil {
		return err
	}
	var now int64
	if c.Now != nil {
		now = c.Now()
	}
	return c.Settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: key, Value: string(raw), UpdatedAtMs: now, UpdatedBy: actor})
}

// Delete removes an organization's connector for a component.
func (c *Connectors) Delete(ctx context.Context, tenantID, name string) error {
	if c == nil || c.Settings == nil {
		return nil
	}
	return c.Settings.DeletePlatformSetting(ctx, connectorKey(tenantID, name))
}

// DeleteComponent removes every organization's connector of an uninstalled
// component (works without the key: nothing is opened).
func (c *Connectors) DeleteComponent(ctx context.Context, name string) error {
	if c == nil || c.Settings == nil {
		return nil
	}
	rows, err := c.Settings.ListPlatformSettings(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		rest, ok := strings.CutPrefix(row.Key, connectorKeyPrefix)
		if !ok {
			continue
		}
		if i := strings.LastIndex(rest, "/"); i >= 0 && rest[i+1:] == name {
			if err := c.Settings.DeletePlatformSetting(ctx, row.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// MergeValues applies an update to stored values under a component's env:
// only declared names, bounded values, an empty secret keeps the stored one,
// an empty non-secret clears it, and every required variable ends set.
func MergeValues(env []pkgmeta.MCPEnv, current, update map[string]string) (map[string]string, error) {
	declared := map[string]bool{}
	for _, e := range env {
		declared[e.Name] = true
	}
	for k, v := range update {
		if !declared[k] {
			return nil, fmt.Errorf("unknown variable %q", k)
		}
		if len(v) > maxConnectorValue || !utf8.ValidString(v) {
			return nil, fmt.Errorf("%s must be text of at most %d bytes", k, maxConnectorValue)
		}
	}
	next := map[string]string{}
	for _, e := range env {
		v := strings.TrimSpace(update[e.Name])
		if v == "" && e.Secret {
			v = current[e.Name]
		}
		if v != "" {
			next[e.Name] = v
		}
	}
	if unset := pkgmeta.UnsetEnv(pkgmeta.RequiredEnv(env), next); len(unset) > 0 {
		return nil, fmt.Errorf("%s is required", unset[0])
	}
	return next, nil
}

// Configured reports whether values satisfy a component's required env.
func Configured(env []pkgmeta.MCPEnv, values map[string]string) bool {
	return len(pkgmeta.UnsetEnv(pkgmeta.RequiredEnv(env), values)) == 0
}
