package bridge

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Room files (chat attachments, a file's version to edit) are downloaded
// with no total time limit: a large file on a slow link takes what it takes.
// Only a stall ends one, no bytes for downloadStall (no-fixed-time-limits).
// QA 2026-10-20: the bridge's short-call client cut attachments of 7.6 and
// 11.9 MB at its 20 s limit, the agent never saw them, and the member's
// task failed for it. Downloads still go through send (one sender), on the
// download client below.

// downloadStall is how long a download may receive nothing (its answer's
// headers, then each part of its body) before it is given up. A variable:
// tests shorten it before building a bridge.
var downloadStall = 60 * time.Second

// errDownloadStalled: a download received nothing for downloadStall.
var errDownloadStalled = errors.New("the download stalled")

// newDownloadClient is the client of room file downloads: no total timeout;
// the answer's headers wait at most downloadStall, and its body ends with
// errDownloadStalled once a read waits that long.
func newDownloadClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = downloadStall
	return &http.Client{Transport: stallTransport{base: base, stall: downloadStall}}
}

// stallTransport hands each answer's body to a stallBody.
type stallTransport struct {
	base  http.RoundTripper
	stall time.Duration
}

func (t stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() && req.Context().Err() == nil {
		return nil, fmt.Errorf("%w: %v", errDownloadStalled, err) // no answer within the stall
	}
	if err != nil {
		return nil, err
	}
	resp.Body = newStallBody(resp.Body, t.stall)
	return resp, nil
}

// stallBody closes its body once no byte arrived for stall, and then
// answers errDownloadStalled; every byte that arrives restarts the wait.
type stallBody struct {
	io.ReadCloser
	stall   time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

func newStallBody(rc io.ReadCloser, stall time.Duration) *stallBody {
	s := &stallBody{ReadCloser: rc, stall: stall}
	s.timer = time.AfterFunc(stall, func() {
		s.stalled.Store(true)
		_ = rc.Close()
	})
	return s
}

func (s *stallBody) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if n > 0 {
		s.timer.Reset(s.stall)
	}
	if err != nil && err != io.EOF && s.stalled.Load() {
		return n, errDownloadStalled
	}
	return n, err
}

func (s *stallBody) Close() error {
	s.timer.Stop()
	return s.ReadCloser.Close()
}
