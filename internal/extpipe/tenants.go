package extpipe

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ErrNoTenants rejects a tenant restriction that names no tenant. An empty list
// is ambiguous (all tenants? none?), so the pipeline refuses it instead of
// guessing: restricting is SetTenants with at least one tenant, lifting the
// restriction is ClearTenants, and distributing to nobody is rollout off.
var ErrNoTenants = errors.New("enabledTenants must name at least one tenant (use allTenants to lift the restriction, or rollout off to distribute to nobody)")

// SetTenants restricts an installed extension to exactly the given tenants.
// Ids are trimmed, de-duplicated and sorted; a list that is empty after that
// is rejected with ErrNoTenants (fail closed — it never means "all tenants").
func (p *Pipeline) SetTenants(ctx context.Context, kind extreg.Kind, name string, tenants []string, actor string) error {
	normalized := NormalizeTenants(tenants)
	if len(normalized) == 0 {
		return ErrNoTenants
	}
	return p.writeTenants(ctx, kind, name, normalized, actor)
}

// ClearTenants lifts the tenant restriction: the extension is distributed to
// every tenant again (persisted as a nil list).
func (p *Pipeline) ClearTenants(ctx context.Context, kind extreg.Kind, name, actor string) error {
	return p.writeTenants(ctx, kind, name, nil, actor)
}

func (p *Pipeline) writeTenants(ctx context.Context, kind extreg.Kind, name string, tenants []string, actor string) error {
	rec, ok, err := p.load(ctx, kind, name)
	if err != nil || !ok {
		return notFound(err, kind, name)
	}
	rec.EnabledTenants = tenants
	if err := p.save(ctx, rec, actor); err != nil {
		return err
	}
	detail := strings.Join(tenants, ",")
	if tenants == nil {
		detail = "*"
	}
	p.audit(ctx, rec, "set-tenants", wire.AuditOutcomeAllow, actor, detail)
	return nil
}

// NormalizeTenants trims, de-duplicates and sorts tenant ids (the shape a
// tenant restriction is stored in, for executable records and platform
// content alike).
func NormalizeTenants(tenants []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tenants {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
