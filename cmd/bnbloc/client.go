package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

// defaultGateway is the local development gateway (compose.yml's public edge).
const defaultGateway = "http://localhost:7090"

// config is the resolved endpoint + credential for one CLI process.
//
// Credentials, highest precedence first:
//  1. a static bearer: --token, else BNBLOC_TOKEN;
//  2. a super-admin-issued API token, BNBLOC_API_TOKEN: exchanged once per
//     process (POST /v1/auth/api-token) for a 1-hour session — the
//     credential of unattended runs (CI), which cannot keep a rotating
//     refresh token;
//  3. the sign-in stored by `bnbloc login` (~/.bnbloc/config.json): its
//     refresh token is exchanged once per process for a fresh session, and
//     the rotated refresh token is written back.
//
// The stored sign-in is used only against the gateway it was issued by.
type config struct {
	Gateway string
	Token   string
	// APIToken is BNBLOC_API_TOKEN (exchanged for a session per process).
	APIToken string
	// RefreshToken is the stored login for Gateway ("" when the stored login
	// belongs to another gateway, or there is none).
	RefreshToken string
	// StoredGateway is the gateway the stored login belongs to.
	StoredGateway string
	// configFile is where the rotated refresh token is persisted.
	configFile string
}

// storedLogin is the part of ~/.bnbloc/config.json the CLI owns. Other keys in
// the file are preserved on every write.
type storedLogin struct {
	Gateway      string `json:"gateway,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	Subject      string `json:"subject,omitempty"`
}

// resolveConfig layers flag > env > ~/.bnbloc/config.json > default.
func resolveConfig(flagGateway, flagToken string) config {
	cfg := config{Gateway: defaultGateway}
	if path, err := configPath(); err == nil {
		cfg.configFile = path
		if stored, err := readStoredLogin(path); err == nil {
			cfg.StoredGateway = normalizeGateway(stored.Gateway)
			if stored.Gateway != "" {
				cfg.Gateway = stored.Gateway
			}
			cfg.RefreshToken = stored.RefreshToken
		}
	}
	if v := os.Getenv("BNBLOC_GATEWAY"); v != "" {
		cfg.Gateway = v
	}
	if v := os.Getenv("BNBLOC_TOKEN"); v != "" {
		cfg.Token = v
	}
	cfg.APIToken = strings.TrimSpace(os.Getenv(apiTokenEnv))
	if flagGateway != "" {
		cfg.Gateway = flagGateway
	}
	if flagToken != "" {
		cfg.Token = flagToken
	}
	cfg.Gateway = normalizeGateway(cfg.Gateway)
	if cfg.StoredGateway != cfg.Gateway {
		// A refresh token is a credential of the gateway that issued it; it
		// is never sent anywhere else.
		cfg.RefreshToken = ""
	}
	return cfg
}

func normalizeGateway(g string) string { return strings.TrimRight(strings.TrimSpace(g), "/") }

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bnbloc", "config.json"), nil
}

// expandHome resolves a leading "~/" (a flag written as --out=~/x is not
// expanded by the shell).
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func readStoredLogin(path string) (storedLogin, error) {
	var stored storedLogin
	raw, err := os.ReadFile(path)
	if err != nil {
		return stored, err
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return stored, fmt.Errorf("%s: %w", path, err)
	}
	return stored, nil
}

// writeStoredLogin merges login into the config file atomically (temp file in
// the same directory, 0600, then rename), keeping every other key. Empty
// login fields delete their keys.
func writeStoredLogin(path string, login storedLogin) error {
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for key, value := range map[string]string{"gateway": login.Gateway, "refreshToken": login.RefreshToken, "subject": login.Subject} {
		if value == "" {
			delete(doc, key)
			continue
		}
		encoded, _ := json.Marshal(value)
		doc[key] = encoded
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'), 0o600)
}

// writeFileAtomic writes data to path via a same-directory temp file and a
// rename, so a crash never leaves a truncated file and the final file has
// exactly mode perm (even when it existed before with another mode).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type client struct {
	cfg  config
	http *http.Client

	// The stored login is exchanged for a session at most once per client
	// (one client per command, so once per process).
	authOnce sync.Once
	grant    *sessionGrant
	authErr  error
}

// sessionGrant is the gateway's login/refresh/claim response.
type sessionGrant struct {
	SessionToken string `json:"sessionToken"`
	RefreshToken string `json:"refreshToken"`
	Subject      string `json:"subject"`
	Tenant       string `json:"tenant"`
	Role         string `json:"role"`
}

// apiTimeout bounds one JSON control-plane call. An artifact upload streams up
// to 256 MiB, so it has no total deadline (that would cut a slow but healthy
// upload); the transport still bounds connecting and waiting for the answer.
const apiTimeout = 30 * time.Second

func newClient(cfg config) *client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 2 * time.Minute // after the body is sent
	return &client{cfg: cfg, http: &http.Client{Transport: transport}}
}

// errNotSignedIn is the hint for every credential failure.
func errNotSignedIn(gateway, why string) error {
	return fmt.Errorf("%s; run `bnbloc login --gateway %s` (or pass --token / BNBLOC_TOKEN, or BNBLOC_API_TOKEN)", why, gateway)
}

// bearer returns the session token to authenticate with: the static token, or
// a session freshly minted from the stored login. "" means unauthenticated.
func (c *client) bearer() (string, error) {
	if c.cfg.Token != "" {
		return c.cfg.Token, nil
	}
	if c.cfg.APIToken != "" {
		c.authOnce.Do(func() { c.grant, c.authErr = c.exchangeAPIToken() })
		if c.authErr != nil {
			return "", c.authErr
		}
		return c.grant.SessionToken, nil
	}
	if c.cfg.RefreshToken == "" {
		return "", nil
	}
	c.authOnce.Do(func() { c.grant, c.authErr = c.refresh() })
	if c.authErr != nil {
		return "", c.authErr
	}
	return c.grant.SessionToken, nil
}

// identity is who the CLI acts as: the session the credential precedence
// (bearer) selects, and which credential selected it.
type identity struct {
	Source                string
	Subject, Tenant, Role string
}

// identity reports the session every command of this process uses: a
// static bearer's own claims (the gateway verifies the signature on every
// call; this only reads them), else the session minted from the API token
// or the stored sign-in.
func (c *client) identity() (identity, error) {
	if c.cfg.Token != "" {
		claims, err := auth.PeekSession(c.cfg.Token)
		if err != nil {
			return identity{}, err
		}
		if claims.ExpiresAtUnix != 0 && time.Now().Unix() > claims.ExpiresAtUnix {
			return identity{}, errNotSignedIn(c.cfg.Gateway, "the session token expired")
		}
		if claims.Subject == "" {
			return identity{}, errors.New("the session carries no subject")
		}
		return identity{Source: "session token (--token / BNBLOC_TOKEN)", Subject: claims.Subject, Tenant: claims.Tenant, Role: claims.Role}, nil
	}
	if c.cfg.APIToken == "" && c.cfg.RefreshToken == "" {
		return identity{}, errNotSignedIn(c.cfg.Gateway, "not signed in to "+c.cfg.Gateway)
	}
	if _, err := c.bearer(); err != nil {
		return identity{}, err
	}
	source := "stored sign-in"
	if c.cfg.APIToken != "" {
		source = apiTokenEnv
	}
	if c.grant.Subject == "" {
		return identity{}, errors.New("the session carries no subject")
	}
	return identity{Source: source, Subject: c.grant.Subject, Tenant: c.grant.Tenant, Role: c.grant.Role}, nil
}

// refresh exchanges the stored refresh token for a session and persists the
// rotated refresh token before anything else uses the session.
func (c *client) refresh() (*sessionGrant, error) {
	body, _ := json.Marshal(map[string]string{"refreshToken": c.cfg.RefreshToken})
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	raw, status, err := c.send(ctx, http.MethodPost, "/v1/auth/refresh", bytes.NewReader(body), "application/json", "")
	if err != nil {
		return nil, fmt.Errorf("refresh stored sign-in: %w", err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusBadRequest {
		return nil, errNotSignedIn(c.cfg.Gateway, "the stored sign-in was refused (expired, revoked, or the tenant is suspended)")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("refresh stored sign-in: %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var g sessionGrant
	if err := json.Unmarshal(raw, &g); err != nil || g.SessionToken == "" || g.RefreshToken == "" {
		return nil, fmt.Errorf("refresh stored sign-in: invalid response")
	}
	if c.cfg.configFile != "" {
		if err := writeStoredLogin(c.cfg.configFile, storedLogin{Gateway: c.cfg.Gateway, RefreshToken: g.RefreshToken, Subject: g.Subject}); err != nil {
			return nil, fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	c.cfg.RefreshToken = g.RefreshToken
	return &g, nil
}

// call is the CLI's one JSON control-plane call: in (nil for none) is sent
// as the JSON body, and a 2xx answer is decoded into out (nil discards it;
// a *json.RawMessage keeps the bytes). It is bounded by timeout and
// cancelled with ctx, and every failure is returned: encoding, transport,
// a non-2xx status (responseError) and an undecodable answer.
func (c *client) call(ctx context.Context, timeout time.Duration, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s %s: encode request: %w", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := c.doReader(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil // no body (204) leaves out as it is
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: invalid response: %w", method, path, err)
	}
	return nil
}

// statusError is a gateway answer outside 2xx.
type statusError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Body)
}

// responseError maps an answer outside 2xx: a 401 is the one sign-in hint
// (not signed in, or the session was refused), anything else a
// statusError.
func (c *client) responseError(method, path string, status int, body []byte, token string) error {
	if status == http.StatusUnauthorized {
		why := "not signed in"
		if token != "" {
			why = "the session was refused (expired, revoked, or the tenant is suspended)"
		}
		return errNotSignedIn(c.cfg.Gateway, fmt.Sprintf("%s %s: %s", method, path, why))
	}
	return &statusError{Method: method, Path: path, Status: status, Body: strings.TrimSpace(string(body))}
}

// doReader sends an authenticated request with an arbitrary body reader and
// content type. Used to stream a raw artifact binary to the upload endpoint
// without loading a JSON envelope around it. ctx carries the caller's deadline
// (none for a streamed upload).
func (c *client) doReader(ctx context.Context, method, path string, r io.Reader, contentType string) ([]byte, error) {
	token, err := c.bearer()
	if err != nil {
		return nil, err
	}
	out, status, err := c.send(ctx, method, path, r, contentType, token)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if status < 200 || status >= 300 {
		return nil, c.responseError(method, path, status, out, token)
	}
	return out, nil
}

// send performs one request and returns the body and status.
func (c *client) send(ctx context.Context, method, path string, r io.Reader, contentType, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.Gateway+path, r)
	if err != nil {
		return nil, 0, err
	}
	if r != nil {
		req.Header.Set("Content-Type", contentType)
		if f, ok := r.(*os.File); ok {
			// Stream a file with a known length (no chunked encoding).
			if st, err := f.Stat(); err == nil {
				req.ContentLength = st.Size()
			}
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}
	return out, resp.StatusCode, nil
}

// stream sends an authenticated GET and hands the response body to consume.
// It has no total deadline of its own (verifying a large artifact, following a
// long-lived SSE stream); ctx bounds or cancels it.
func (c *client) stream(ctx context.Context, path string, consume func(io.Reader) error) error {
	token, err := c.bearer()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Gateway+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return c.responseError(http.MethodGet, path, resp.StatusCode, raw, token)
	}
	return consume(resp.Body)
}
