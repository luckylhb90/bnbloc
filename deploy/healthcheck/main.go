// Command healthcheck probes one bnbloc service's HTTP probe endpoint and exits
// 0 on HTTP 200, 1 otherwise. It exists for container HEALTHCHECKs: the
// service images are distroless (no shell, no curl/wget), so the probe ships as
// this one static binary beside every service binary instead of a per-service
// copy of a shell one-liner.
//
//	healthcheck [url]   default http://127.0.0.1:8080/readyz
//
// Point it at /readyz (dependency-gated: 503 while the database is
// unreachable) for compose `depends_on: service_healthy` ordering, or /healthz
// for pure liveness. See internal/obs for the probe semantics.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

const defaultURL = "http://127.0.0.1:8080/readyz"

func main() {
	url := defaultURL
	if len(os.Args) > 1 && os.Args[1] != "" {
		url = os.Args[1]
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck %s: %v\n", url, err)
		os.Exit(1)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck %s: HTTP %d\n", url, resp.StatusCode)
		os.Exit(1)
	}
}
