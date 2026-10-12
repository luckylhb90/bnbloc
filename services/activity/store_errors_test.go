package activity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
)

var errStoreDown = errors.New("db exploded: connection refused")

// failingJobs makes the job store's write paths return fail.
type failingJobs struct {
	store.BridgeJobStore
	fail, lookupFail error
}

func (f *failingJobs) UpdateAgentJobStatus(ctx context.Context, u store.JobStatusUpdate) (store.AgentJob, error) {
	if f.fail != nil {
		return store.AgentJob{}, f.fail
	}
	return f.BridgeJobStore.UpdateAgentJobStatus(ctx, u)
}

func (f *failingJobs) RenewAgentJobLease(ctx context.Context, r store.LeaseRenewal) error {
	if f.fail != nil {
		return f.fail
	}
	return f.BridgeJobStore.RenewAgentJobLease(ctx, r)
}

func (f *failingJobs) CancelAgentJob(ctx context.Context, r store.CancelRequest) (store.AgentJob, error) {
	if f.fail != nil {
		return store.AgentJob{}, f.fail
	}
	return f.BridgeJobStore.CancelAgentJob(ctx, r)
}

func (f *failingJobs) BridgeOwner(ctx context.Context, id string) (string, error) {
	if f.lookupFail != nil {
		return "", f.lookupFail
	}
	return f.BridgeJobStore.BridgeOwner(ctx, id)
}

// TestStoreErrorsNotConflict: only the store's lease sentinel is a 409 (the
// job is no longer the caller's); an unknown job is a 404, and a store failure
// is a logged 500 whose body never carries the database error. A failure used
// to read as "lease lost" (409, error text in the body).
func TestStoreErrorsNotConflict(t *testing.T) {
	routes := []struct {
		name, path string
		body       map[string]any
	}{
		{"status", "/v1/bridge/jobs/job-1/status", map[string]any{"bridgeId": "bridge-maya", "status": "running"}},
		{"lease", "/v1/bridge/jobs/job-1/lease", map[string]any{"bridgeId": "bridge-maya"}},
		{"cancel", "/v1/rooms/room-1/agent-jobs/job-1/cancel", map[string]any{}},
	}
	for _, rt := range routes {
		for _, tc := range []struct {
			name         string
			fail, lookup error
			want         int
		}{
			{"store failure", errStoreDown, nil, http.StatusInternalServerError},
			{"lease lost", store.ErrAgentJobLeaseMismatch, nil, http.StatusConflict},
			{"unknown job", store.ErrAgentJobNotFound, nil, http.StatusNotFound},
			{"bridge lookup failure", nil, errStoreDown, http.StatusInternalServerError},
			{"unknown bridge", nil, store.ErrBridgeNotFound, http.StatusForbidden},
		} {
			if rt.name == "cancel" && tc.lookup != nil {
				continue // a cancel is a room action: no bridge lookup
			}
			t.Run(rt.name+"/"+tc.name, func(t *testing.T) {
				_, _, mem := newJobServer(t)
				jobs := &failingJobs{BridgeJobStore: mem, fail: tc.fail, lookupFail: tc.lookup}
				rooms := membership.NewStatic()
				rooms.Add("room-1", "maya")
				svc := &Service{Store: store.NewMemory(), Publisher: &capturePublisher{}, Membership: rooms, BridgeJobs: jobs}
				srv := httptest.NewServer(svc.Handler())
				t.Cleanup(srv.Close)
				resp := post(t, srv, rt.path, "maya", rt.body)
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != tc.want {
					t.Fatalf("status = %d (%s), want %d", resp.StatusCode, body, tc.want)
				}
				if strings.Contains(string(body), "db exploded") {
					t.Fatalf("response body leaks the store error: %s", body)
				}
			})
		}
	}
}
