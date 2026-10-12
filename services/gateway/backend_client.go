package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// The gateway's own calls to a backend (not the reverse proxy, which asserts
// the session on the forwarded request with svcauth's Assert): every request
// is built by svcauth.NewRequest, so it always carries the subject, a tenant
// and the signature, in that order, and goes out on a client with a timeout.
// The subject is the member the gateway acts for, or gatewayServiceSubject
// for a call on its own behalf (blob reads it has already authorized, share
// resolution, room tenants).

// backendHTTP performs the gateway's short calls to backends.
var backendHTTP = &http.Client{Timeout: 15 * time.Second}

// backendStreamHTTP carries a streamed upload: the request's context bounds
// it, never a client timeout (a large artifact outlives any fixed one).
var backendStreamHTTP = &http.Client{}

// callBackend makes one signed call as subject in tenantID ("" = the default
// tenant) with an optional JSON body. The caller closes the response body.
func callBackend(ctx context.Context, method, url string, body []byte, subject, tenantID string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := svcauth.NewRequest(ctx, method, url, rd, subject, tenantID)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return backendHTTP.Do(req)
}

// gatewayServiceSubject is the identity the gateway asserts when it calls a
// backend on its OWN behalf — the svcauth service-subject convention.
var gatewayServiceSubject = svcauth.ServiceSubject("gateway")
