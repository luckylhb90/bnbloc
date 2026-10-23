package component

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ServiceClient is a service component's side of the contract: it checks
// the platform's signed calls and makes the component's own signed calls
// (the store). One install of the component at one platform.
type ServiceClient struct {
	// BaseURL is the gateway as the component reaches it.
	BaseURL string `json:"-"`
	// Name is the component's install name; Secret its credential (a
	// comma list during a rotation: every one verifies, the first signs).
	Name   string           `json:"-"`
	Secret string           `json:"-"`
	HTTP   *http.Client     `json:"-"`
	Now    func() time.Time `json:"-"`
}

func (c *ServiceClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ErrUnsigned is a call whose signature does not verify.
var ErrUnsigned = errors.New("the call is not signed by bnbloc")

// Verify checks a call from the platform: its signature over the method,
// the path and query (prefix, where the component is mounted, stripped),
// the identity header's value and the body.
func (c *ServiceClient) Verify(r *http.Request, prefix, identityHeader string, body []byte) error {
	identity := ""
	if identityHeader != "" {
		identity = r.Header.Get(identityHeader)
	}
	signed := SignedRequest(r.Method, strings.TrimPrefix(r.URL.RequestURI(), prefix), identity, body)
	for _, secret := range strings.Split(c.Secret, ",") {
		if s := strings.TrimSpace(secret); s != "" && Verify(r.Header.Get(SignatureHeader), s, signed, c.now(), SignatureTolerance) == nil {
			return nil
		}
	}
	return ErrUnsigned
}

func (c *ServiceClient) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set(ComponentHeader, "service/"+c.Name)
	req.Header.Set(SignatureHeader, Sign(strings.TrimSpace(strings.Split(c.Secret, ",")[0]), c.now(), SignedRequest(method, path, "", body)))
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("bnbloc unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxStoreValue+1<<10))
	return resp.StatusCode, raw, err
}

func storeErr(code int, raw []byte) error {
	return fmt.Errorf("bnbloc store answered %d: %s", code, strings.TrimSpace(string(raw)))
}

// StoreGet reads a value (found=false when there is none). scope and owner
// as StorePath.
func (c *ServiceClient) StoreGet(ctx context.Context, scope, owner, key string) ([]byte, bool, error) {
	code, raw, err := c.do(ctx, http.MethodGet, StorePath(scope, owner, key), nil)
	switch {
	case err != nil:
		return nil, false, err
	case code == http.StatusNotFound:
		return nil, false, nil
	case code != http.StatusOK:
		return nil, false, storeErr(code, raw)
	}
	return raw, true, nil
}

// StorePut writes a value.
func (c *ServiceClient) StorePut(ctx context.Context, scope, owner, key string, value []byte) error {
	if len(value) > MaxStoreValue {
		return fmt.Errorf("a value is at most %d bytes", MaxStoreValue)
	}
	code, raw, err := c.do(ctx, http.MethodPut, StorePath(scope, owner, key), value)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent && code != http.StatusOK {
		return storeErr(code, raw)
	}
	return nil
}

// StoreDelete removes a value.
func (c *ServiceClient) StoreDelete(ctx context.Context, scope, owner, key string) error {
	code, raw, err := c.do(ctx, http.MethodDelete, StorePath(scope, owner, key), nil)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent && code != http.StatusOK {
		return storeErr(code, raw)
	}
	return nil
}

// StoreKeys lists a scope's keys.
func (c *ServiceClient) StoreKeys(ctx context.Context, scope, owner string) ([]string, error) {
	code, raw, err := c.do(ctx, http.MethodGet, StorePath(scope, owner, ""), nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, storeErr(code, raw)
	}
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}
