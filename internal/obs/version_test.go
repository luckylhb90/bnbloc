package obs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestWithVersionServesBuildIdentity(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := withVersion("bnbloc-room", app)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	var v wire.ServiceVersion
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Service != "bnbloc-room" || v.Instance == "" {
		t.Fatalf("GET /version = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rooms", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("other paths must reach the app handler, got %d", rec.Code)
	}
}
