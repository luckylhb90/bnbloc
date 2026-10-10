package conformance

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
)

// TestConformance runs the suite against a live implementation:
//
//	SANDBOX_URL=http://127.0.0.1:7311 SANDBOX_TOKEN=... go test ./sandbox/conformance -run TestConformance -v
//
// SANDBOX_IMAGE overrides the guest image; SANDBOX_SHARE_DIR names the host
// directory to share (default: a temp dir for a loopback endpoint);
// SANDBOX_NETWORK_TESTS=1 enables the checks that need internet access from
// the host; SANDBOX_EXEC_P95_MS fails the run when space exec p95 exceeds it.
func TestConformance(t *testing.T) {
	base := os.Getenv("SANDBOX_URL")
	if base == "" {
		t.Skip("SANDBOX_URL not set")
	}
	var budget time.Duration
	if ms, err := strconv.Atoi(os.Getenv("SANDBOX_EXEC_P95_MS")); err == nil && ms > 0 {
		budget = time.Duration(ms) * time.Millisecond
	}
	Run(t, Config{
		Endpoint:      core.Endpoint{BaseURL: base, Token: os.Getenv("SANDBOX_TOKEN")},
		Image:         os.Getenv("SANDBOX_IMAGE"),
		ShareDir:      os.Getenv("SANDBOX_SHARE_DIR"),
		NetworkTests:  os.Getenv("SANDBOX_NETWORK_TESTS") == "1",
		AllowHost:     os.Getenv("SANDBOX_ALLOW_HOST"),
		ExecP95Budget: budget,
		Timeout:       15 * time.Minute,
	})
}
