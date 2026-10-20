package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dripServer answers a room file in parts, a pause before each; its
// headers come at once, or after the first pause when late.
func dripServer(t *testing.T, parts int, pause time.Duration, late bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		w.Header().Set("Content-Disposition", `attachment; filename="Close Chat New.gif"`)
		if !late {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		for i := 0; i < parts; i++ {
			time.Sleep(pause)
			_, _ = w.Write([]byte(strings.Repeat("g", 1024)))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A room file that keeps arriving is downloaded however long it takes,
// beyond the short-call client's limit (QA 2026-10-20: two GIFs were cut at
// 20 s and never reached the agent).
func TestAttachmentDownloadHasNoTotalLimit(t *testing.T) {
	stall := downloadStall
	downloadStall = 200 * time.Millisecond
	t.Cleanup(func() { downloadStall = stall })
	srv := dripServer(t, 6, 80*time.Millisecond, false)
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "maya"})
	b.http.Timeout = 150 * time.Millisecond // the short-call limit, well under the download's half second (not the download's client)
	data, ct, name, err := b.gatewayGETAttachment(context.Background(), "/v1/rooms/r/attachments/a")
	if err != nil || len(data) != 6*1024 || ct != "image/gif" || name != "Close Chat New.gif" {
		t.Fatalf("download = %d bytes %q %q, %v", len(data), ct, name, err)
	}
}

// A download that receives nothing for downloadStall is given up.
func TestAttachmentDownloadEndsOnAStall(t *testing.T) {
	stall := downloadStall
	downloadStall = 100 * time.Millisecond
	t.Cleanup(func() { downloadStall = stall })
	for _, late := range []bool{false, true} { // stalled in the body; no answer at all
		srv := dripServer(t, 2, 400*time.Millisecond, late)
		b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "maya"})
		start := time.Now()
		_, _, _, err := b.gatewayGETAttachment(context.Background(), "/v1/rooms/r/attachments/a")
		if !errors.Is(err, errDownloadStalled) || time.Since(start) > 350*time.Millisecond {
			t.Fatalf("late headers %v: a stalled download = %v after %v", late, err, time.Since(start))
		}
	}
}
