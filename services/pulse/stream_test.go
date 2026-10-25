package pulsesvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// lockedRecorder is a ResponseRecorder that can be read while a handler writes.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (l *lockedRecorder) Header() http.Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Header()
}

func (l *lockedRecorder) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Write(b)
}

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.WriteHeader(code)
}

func (l *lockedRecorder) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.Flush()
}

func (l *lockedRecorder) snapshot() (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Body.String(), l.rec.Header().Get("Content-Type")
}

// The stream pushes a snapshot event now and on every tick, and stops when the
// client goes away.
func TestStreamPushesSnapshotsUntilTheClientLeaves(t *testing.T) {
	rdb := testRedis(t)
	old := streamEvery
	streamEvery = 10 * time.Millisecond
	t.Cleanup(func() { streamEvery = old })

	srv := &Server{State: NewLive(rdb), Rows: newFakeRows(), Now: time.Now}
	ctx, cancel := context.WithCancel(tenant.NewContext(context.Background(), uniq("tenant")))
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/pulse/stream", nil).WithContext(ctx)
	rec := &lockedRecorder{rec: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() { srv.Handler().ServeHTTP(rec, req); close(done) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		body, _ := rec.snapshot()
		if strings.Count(body, "event: snapshot\n") >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	body, ct := rec.snapshot()
	if n := strings.Count(body, "event: snapshot\n"); n < 3 {
		t.Fatalf("got %d snapshot events, want at least 3: code body=%q", n, body)
	}
	if ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("a healthy stream must not report an error:\n%s", body)
	}
}

// A stream with no tenant is refused before anything is written.
func TestStreamRefusesARequestWithoutATenant(t *testing.T) {
	srv := &Server{State: NewLive(nil), Rows: newFakeRows(), Now: time.Now}
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/pulse/stream", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}
