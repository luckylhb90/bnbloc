package obs

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A stopping process says it is not ready and ends its event streams, while
// other requests still complete.
func TestDrainingEndsStreamsAndReadiness(t *testing.T) {
	defer resetDrainForTest()
	ended, started := make(chan struct{}), make(chan struct{})
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			close(started)
			<-r.Context().Done()
			close(ended)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux := buildServeMux(slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics("svc"), app, nil)
	get := func(path string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if code := get("/readyz"); code != http.StatusOK {
		t.Fatalf("readyz before draining = %d", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(context.Background())
	req.Header.Set("Accept", "text/event-stream")
	go mux.ServeHTTP(httptest.NewRecorder(), req)
	<-started
	startDraining()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("an event stream outlived the drain")
	}
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining = %d, want 503", code)
	}
	// A new stream is refused at once, with when to try again.
	late := httptest.NewRequest(http.MethodGet, "/stream", nil)
	late.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, late)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("a new stream while draining = %d (Retry-After %q), want 503", rec.Code, rec.Header().Get("Retry-After"))
	}
	if code := get("/other"); code != http.StatusOK {
		t.Fatalf("an ordinary request while draining = %d, want 200", code)
	}
}
