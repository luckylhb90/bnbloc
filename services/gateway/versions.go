package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/buildinfo"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// GET /v1/platform/versions (super-admin): the deployment's build identity.
// The gateway reports itself and its embedded web bundle in-process and
// samples each addressed service's GET /version several times at once on
// fresh connections, so replicas behind one Service DNS name (connection-level
// load balancing) each get a chance to answer. Instances are deduplicated;
// buildinfo.Evaluate decides consistency.

const (
	// versionSamples is how many /version requests each backend gets.
	versionSamples = 5
	// versionTimeout bounds one /version request.
	versionTimeout = 3 * time.Second
	// slowVersionProbe is how long sampling one backend may take before it
	// is logged.
	slowVersionProbe = time.Second
)

// versionClient never reuses a connection: a reused one always lands on the
// same replica.
var versionClient = &http.Client{
	Timeout: versionTimeout,
	Transport: func() http.RoundTripper {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DisableKeepAlives = true
		return t
	}(),
}

func (s *Service) handlePlatformVersions(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	httpx.WriteJSON(w, http.StatusOK, s.deploymentVersions(r.Context()))
}

func (s *Service) deploymentVersions(ctx context.Context) wire.DeploymentVersions {
	out := wire.DeploymentVersions{
		Services: []wire.ServiceVersion{buildinfo.Get("bnbloc-gateway")},
		Web:      s.WebBuild,
	}
	backends := []struct{ name, base string }{
		{"bnbloc-room", s.Backends.Room},
		{"bnbloc-activity", s.Backends.Activity},
		{"bnbloc-realtime", s.Backends.Realtime},
		{"bnbloc-shareagent", s.Backends.ShareAgent},
		{"bnbloc-relay", s.Backends.Relay},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, b := range backends {
		if strings.TrimSpace(b.base) == "" {
			continue
		}
		wg.Add(1)
		go func(name, base string) {
			defer wg.Done()
			started := time.Now()
			seen, err := sampleVersions(ctx, strings.TrimRight(base, "/")+"/version")
			// A probe that is slow says so: the card waits for the slowest.
			if took := time.Since(started); took > slowVersionProbe {
				slog.Warn("bnbloc-gateway: version probe was slow", "event", "bnbloc.versions.slow", "service", name, "took", took.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 {
				slog.Warn("bnbloc-gateway: version probe failed", "event", "bnbloc.versions.unreachable", "service", name, "error", err)
				out.Unreachable = append(out.Unreachable, wire.UnreachableService{Service: name, Error: err.Error()})
				return
			}
			out.Services = append(out.Services, seen...)
		}(b.name, b.base)
	}
	wg.Wait()
	buildinfo.Evaluate(&out)
	return out
}

// sampleVersions GETs url versionSamples times, all at once, and returns the
// distinct instances that answered in the order the samples were taken; err is
// the last failure (non-nil when none did). Together, not in turn: the
// Deployment card waits for the slowest backend, and taking the samples one
// after another cost five round trips (and, for a backend that does not
// answer, five timeouts) where one is enough.
func sampleVersions(ctx context.Context, url string) ([]wire.ServiceVersion, error) {
	type sample struct {
		v   wire.ServiceVersion
		err error
	}
	results := make([]sample, versionSamples)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].v, results[i].err = fetchVersion(ctx, url)
		}(i)
	}
	wg.Wait()
	var out []wire.ServiceVersion
	seen := map[string]bool{}
	var lastErr error
	for _, r := range results {
		if r.err != nil {
			lastErr = r.err
			continue
		}
		if !seen[r.v.Instance] {
			seen[r.v.Instance] = true
			out = append(out, r.v)
		}
	}
	return out, lastErr
}

func fetchVersion(ctx context.Context, url string) (wire.ServiceVersion, error) {
	var v wire.ServiceVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return v, err
	}
	resp, err := versionClient.Do(req)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return v, err
	}
	if resp.StatusCode != http.StatusOK {
		return v, fmt.Errorf("GET /version: %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("GET /version: invalid response: %w", err)
	}
	return v, nil
}
