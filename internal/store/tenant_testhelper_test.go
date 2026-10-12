package store

import (
	"context"
	"testing"

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

// must unwraps a (value, error) store read, failing the test on the error:
// must(s.Members(ctx, room))(t). A read that fails is never an empty answer.
func must[T any](v T, err error) func(testing.TB) T {
	return func(t testing.TB) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// must2 is must for a (value, found, error) store read.
func must2[A, B any](a A, b B, err error) func(testing.TB) (A, B) {
	return func(t testing.TB) (A, B) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
}
