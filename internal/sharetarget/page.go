package sharetarget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
)

// FetchPage reads the target's share page (GET {baseUrl}/v1/share/page): one
// self-contained HTML document of at most MaxPageBytes. The endpoint client
// enforces the component's permissions.network.
func FetchPage(ctx context.Context, ep endpointpkg.Endpoint) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.BaseURL+PagePath, nil)
	if err != nil {
		return nil, errors.New("build the page request failed")
	}
	client := http.DefaultClient
	if ep.Client != nil {
		client = ep.Client
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return nil, errors.New("the share target is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the share target's page answered HTTP %d", resp.StatusCode)
	}
	if mt := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(mt), "text/html") {
		return nil, errors.New("the share target's page is not an HTML document")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPageBytes+1))
	if err != nil {
		return nil, errors.New("read the share target's page failed")
	}
	if len(body) > MaxPageBytes {
		return nil, errors.New("the share target's page is larger than 512 KiB")
	}
	return body, nil
}
