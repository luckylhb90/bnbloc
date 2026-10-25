package pulsesvc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotConfiguredAnswersTheBoardRoutesUnavailable(t *testing.T) {
	h := NotConfigured()
	for _, path := range []string{"/v1/tenant/pulse/snapshot", "/v1/tenant/pulse/stream"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d, want 503", path, rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: no Retry-After", path)
		}
	}
}
