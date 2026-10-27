package roomtools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
)

// Real git through the proxy (docs/prd/2026-10-27-streamed-member-sends.md):
// a clone whose pack is larger than an envelope, and a push of one, go to
// the code host from this computer with the member's credential, which the
// command never had.
func TestEgressGitCloneAndPushStream(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(env []string, wd string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir, cmd.Env = wd, append(append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir, "GIT_TERMINAL_PROMPT=0"), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	bigFile := func(path string) {
		raw := make([]byte, 12<<20) // random: the pack cannot compress it below 8 MiB
		_, _ = rand.Read(raw)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bare := filepath.Join(dir, "repos", "team", "repo.git")
	run(nil, dir, "init", "-q", "--bare", "-b", "main", bare)
	run(nil, bare, "config", "http.receivepack", "true")
	seed := filepath.Join(dir, "seed")
	run(nil, dir, "init", "-q", "-b", "main", seed)
	bigFile(filepath.Join(seed, "one.bin"))
	run(nil, seed, "add", ".")
	run(nil, seed, "-c", "user.name=m", "-c", "user.email=m@example.com", "commit", "-q", "-m", "one")
	run(nil, seed, "push", "-q", bare, "main")

	backend := &cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(dir, "repos"), "GIT_HTTP_EXPORT_ALL=1"}}
	sys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic bWF5YTp0b2tlbg==" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// CGI needs a length: the test's host reads a chunked body first (a
		// real code host takes it as it comes).
		raw, _ := io.ReadAll(r.Body)
		r.Body, r.ContentLength, r.TransferEncoding = io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
		backend.ServeHTTP(w, r)
	}))
	defer sys.Close()
	old := hereClient
	hereClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableCompression: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sys.Listener.Addr().String())
		}}}
	defer func() { hereClient = old }()

	var streamed int
	gw := &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		var in component.EgressRequest
		_ = json.Unmarshal(body, &in)
		if in.Report != nil {
			return http.StatusOK, component.EgressResponse{Accept: true}
		}
		if in.Streamed {
			streamed++
		}
		return http.StatusOK, component.EgressResponse{Send: &component.EgressSend{Remove: []string{"Authorization"}, Header: map[string][]string{"Authorization": {"Basic bWF5YTp0b2tlbg=="}}}}
	}}
	srv, token, sess := startServer(t, gw)
	sess.SetEgressHosts(map[string]string{"git.example.com": "connections"}, map[string]bool{"connections": true})
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	caPEM, _ := srv.EgressCA()
	caFile := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caFile, caPEM, 0o644)
	env := []string{"HTTPS_PROXY=" + srv.EgressEnv(token, "")["HTTPS_PROXY"], "GIT_SSL_CAINFO=" + caFile}

	clone := filepath.Join(dir, "clone")
	run(env, dir, "clone", "-q", "https://git.example.com/team/repo.git", clone)
	if st, err := os.Stat(filepath.Join(clone, "one.bin")); err != nil || st.Size() != 12<<20 {
		t.Fatalf("cloned file = %v %v", st, err)
	}
	bigFile(filepath.Join(clone, "two.bin"))
	run(nil, clone, "add", ".")
	run(nil, clone, "-c", "user.name=m", "-c", "user.email=m@example.com", "commit", "-q", "-m", "two")
	run(env, clone, "push", "-q", "origin", "main")
	if log := run(nil, bare, "log", "--format=%s", "main"); !strings.HasPrefix(log, "two\n") {
		t.Fatalf("the host's log = %q", log)
	}
	if streamed == 0 {
		t.Fatal("the push's pack did not stream")
	}
	if cfg, _ := os.ReadFile(filepath.Join(clone, ".git", "config")); bytes.Contains(cfg, []byte("bWF5YTp0b2tlbg")) || bytes.Contains(cfg, []byte("token")) {
		t.Fatalf("a credential reached the clone's config:\n%s", cfg)
	}
}
