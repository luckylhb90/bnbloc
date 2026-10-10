// Package tenant carries the organization (tenant) scope through a request
// context so every persistence and authorization boundary can enforce it.
//
// Multi-tenancy retrofit: the control plane was originally a flat single
// namespace (identity = subject, isolation = room membership). Tenancy is
// resolved once at the gateway edge from the verified IdP identity, stamped
// into the session token, forwarded to backends as the X-Bnbloc-Tenant header
// (svcauth-signed), and read into the request context here. The store layer
// reads FromContext on every query and MUST refuse an empty tenant, so a code
// path that forgets to propagate tenancy fails closed instead of leaking across
// organizations.
package tenant

import (
	"context"
	"errors"
)

// Default is the tenant assigned to pre-tenancy data and to the dev/loopback
// identity. A backfill migration stamps existing rows with this id so the
// upgrade is non-breaking; a fresh multi-tenant deployment simply never uses
// it for real orgs.
const Default = "default"

// ErrNoTenant is returned by Require when the context carries no tenant. It is
// a fail-closed signal: a store or authorization check that cannot resolve a
// tenant must reject the operation rather than run unscoped.
var ErrNoTenant = errors.New("tenant: no tenant in context")

type ctxKey struct{}

// NewContext returns a copy of ctx carrying the tenant id. An empty id is
// stored as-is; Require will then reject it.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the tenant id carried by ctx, or "" when none is set.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Require returns the context's tenant id, or ErrNoTenant when it is empty.
// Store and authorization code call this so a missing tenant is a hard error,
// never a silent cross-tenant query.
func Require(ctx context.Context) (string, error) {
	id := FromContext(ctx)
	if id == "" {
		return "", ErrNoTenant
	}
	return id, nil
}
