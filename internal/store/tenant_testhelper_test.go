package store

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// testTenant is the tenant every store unit test runs under. The store fails
// closed without a tenant in context (multi-tenant isolation), so tests scope
// their context to a fixed tenant; the dedicated isolation test uses two.
const testTenant = "t-test"

// testCtx returns a context scoped to the default test tenant.
func testCtx() context.Context {
	return tenant.NewContext(context.Background(), testTenant)
}

// tenantCtx returns a context scoped to a specific tenant, for isolation tests.
func tenantCtx(id string) context.Context {
	return tenant.NewContext(context.Background(), id)
}

// bg returns a bare context with NO tenant, for fail-closed assertions.
func bg() context.Context { return context.Background() }
