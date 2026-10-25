package pulse

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// Gate G7: only the Pulse packages import the Redis client. Product code writes
// events through Emit and never reads a Pulse key; the Redis dependency stays
// inside the two packages that own it.
func TestOnlyThePulsePackagesImportTheRedisClient(t *testing.T) {
	allowed := []string{"internal/pulse/", "services/pulse/"}
	var offenders []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Tests: true}, func(rel string, src []byte) {
		if !strings.Contains(string(src), "\"github.com/redis/go-redis/") {
			return
		}
		for _, prefix := range allowed {
			if strings.HasPrefix(rel, prefix) {
				return
			}
		}
		offenders = append(offenders, rel)
	})
	if len(offenders) > 0 {
		t.Fatalf("the Redis client is imported outside the Pulse packages:\n  %s", strings.Join(offenders, "\n  "))
	}
}
