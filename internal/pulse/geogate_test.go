package pulse

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGeoGateFailsClosedAndCaches(t *testing.T) {
	calls := 0
	g := NewGeoGate(func(_ context.Context, tenant string) (bool, error) {
		calls++
		if tenant == "broken" {
			return true, errors.New("db down")
		}
		return tenant == "on", nil
	}, time.Minute)
	ctx := context.Background()
	if !g.On(ctx, "on") || g.On(ctx, "off") {
		t.Fatal("the gate must follow the organization's setting")
	}
	if g.On(ctx, "broken") {
		t.Fatal("a failed read must count as off")
	}
	g.On(ctx, "on")
	if calls != 3 {
		t.Fatalf("reads = %d, want 3 (each tenant read once, then cached)", calls)
	}
	var nilGate *GeoGate
	if nilGate.On(ctx, "on") {
		t.Fatal("a nil gate is off")
	}
}
