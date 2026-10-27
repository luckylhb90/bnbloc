// Package conformance is the Sandbox Protocol v1 conformance suite. Run it
// from a Go test against any implementation's base URL:
//
//	conformance.Run(t, conformance.Config{
//		Endpoint: core.Endpoint{BaseURL: "http://127.0.0.1:7311", Token: "..."},
//	})
//
// The suite drives the shared-machine model end to end — one machine,
// room-scoped spaces (guest user + shared/disk mounts) with host-path
// translation, exec (run, streamed, piped stdin, tty, signals) and its
// latency, files and archives, ports, pause/resume, stop/start, full
// snapshot/restore, fork, warm pools, delete — and checks the security
// invariants every implementation must hold: requests without the bearer are
// refused, egress is denied by default, a secret value never reaches the
// guest in plaintext, and one space cannot read another space's files.
// Optional capabilities are exercised only when discovery advertises them;
// advertising one and failing it fails the suite. Each step logs its
// server-reported and client-measured timings.
package conformance

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// DefaultImage is the conformance guest image. The checks use only POSIX sh
// plus the busybox applets wget, nc, tar, pgrep, stat, id and tty, which
// this image ships.
const DefaultImage = "alpine:3.20"

type Config struct {
	Endpoint   core.Endpoint
	HTTPClient *http.Client
	// Image overrides DefaultImage (must provide sh and busybox-compatible
	// wget, nc, tar, pgrep, stat, id and tty).
	Image string
	// Timeout bounds the whole suite (default 10 minutes: the first image
	// pull may be slow).
	Timeout time.Duration
	// ShareDir is a host directory the implementation can share into the
	// machine (shares capability). Empty: a temp dir is created when the
	// endpoint is loopback (the implementation shares this test's host);
	// otherwise the share checks are skipped.
	ShareDir string
	// NetworkTests enables the checks that need the host to reach the
	// internet (egress allowlist / public reachability).
	NetworkTests bool
	// AllowHost is the allowlisted host for NetworkTests (default example.com).
	AllowHost string
	// PrivateHost is a DNS name the implementation's host resolves to a
	// private address with an HTTP server on port 80 (egressHosts). Empty:
	// the reach checks of egressHosts are skipped.
	PrivateHost string
	// ExecP95Budget, when set, fails the latency step if the p95 of a
	// space-level `true` run exceeds it.
	ExecP95Budget time.Duration
}

// suite carries the state shared by the ordered steps.
type suite struct {
	t         *testing.T
	cfg       Config
	ctx       context.Context
	c         *protocol.Client
	ep        core.Endpoint
	disc      protocol.Discovery
	main      protocol.Machine
	shareDir  string // host side of the "ws" share ("" = no shares)
	spaceA    protocol.Space
	spaceB    protocol.Space
	secret    string
	snap      protocol.Snapshot
	cleanM    []string
	cleanP    []string
	cleanN    []string
	cleanDirs []string
}

const (
	shareName  = "ws"
	spaceAName = "room-a"
	spaceBName = "room-b"
)

// Run executes the suite as ordered subtests. A failed prerequisite (e.g.
// create) skips the steps that depend on it.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	if cfg.AllowHost == "" {
		cfg.AllowHost = "example.com"
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	t.Cleanup(cancel)
	client := protocol.NewClient(cfg.HTTPClient)
	client.REST.Timeout = cfg.Timeout
	s := &suite{t: t, cfg: cfg, ctx: ctx, c: client, ep: cfg.Endpoint}
	t.Cleanup(s.cleanup)

	steps := []struct {
		name string
		fn   func(*testing.T)
		req  bool // later steps depend on it
	}{
		{"discover", s.discover, true},
		{"auth", s.auth, false},
		{"errorEnvelope", s.errorEnvelope, false},
		{"image", s.image, true},
		{"createMachine", s.createMachine, true},
		{"machineRun", s.machineRun, false},
		{"machineExec", s.machineExec, false},
		{"spaces", s.spaces, false},
		{"spaceExec", s.spaceExec, false},
		{"spaceIsolation", s.spaceIsolation, false},
		{"sharedMountLive", s.sharedMountLive, false},
		{"readOnlyMounts", s.readOnlyMounts, false},
		{"privateMounts", s.privateMounts, false},
		{"execLatency", s.execLatency, false},
		{"stdinPipe", s.stdinPipe, false},
		{"tty", s.tty, false},
		{"signal", s.signal, false},
		{"execTokens", s.execTokens, false},
		{"secrets", s.secrets, false},
		{"egressDenyByDefault", s.egressDeny, false},
		{"egressPolicy", s.egressPolicy, false},
		{"egressHosts", s.egressHosts, false},
		{"ports", s.ports, false},
		{"files", s.files, false},
		{"disks", s.disks, false},
		{"pauseResume", s.pauseResume, false},
		{"stopStart", s.stopStart, false},
		{"snapshotRestore", s.snapshotRestore, false},
		{"fork", s.fork, false},
		{"warmPool", s.warmPool, false},
		{"spaceRestart", s.spaceRestart, false},
		{"durableHomes", s.durableHomes, false},
		{"hostProxy", s.hostProxy, false},
		{"deleteSpace", s.deleteSpace, false},
		{"deleteMachine", s.deleteMachine, false},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.fn) && step.req {
			t.Fatalf("prerequisite step %s failed; skipping the rest", step.name)
		}
	}
}

func (s *suite) has(capability string) bool { return protocol.HasCapability(s.disc, capability) }

func (s *suite) need(t *testing.T, capabilities ...string) {
	t.Helper()
	for _, c := range capabilities {
		if !s.has(c) {
			t.Skipf("capability %q not advertised", c)
		}
	}
}

func (s *suite) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, name := range s.cleanP {
		_ = s.c.DeletePool(ctx, s.ep, name)
	}
	for _, id := range s.cleanM {
		_ = s.c.DeleteMachine(ctx, s.ep, id)
	}
	for _, id := range s.cleanN {
		_ = s.c.DeleteSnapshot(ctx, s.ep, id)
	}
	for _, dir := range s.cleanDirs {
		_ = os.RemoveAll(dir)
	}
}

func (s *suite) track(m protocol.Machine) protocol.Machine {
	s.cleanM = append(s.cleanM, m.ID)
	return m
}

// --- discovery, auth, errors, images ---

func (s *suite) discover(t *testing.T) {
	d, err := s.c.Discover(s.ctx, s.ep)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if d.Name == "" || d.Version == "" {
		t.Fatalf("discovery name/version empty: %+v", d)
	}
	if d.Contract != protocol.ContractV1 {
		t.Fatalf("discovery contract = %q, want %q", d.Contract, protocol.ContractV1)
	}
	if !protocol.HasCapability(d, protocol.CapabilityExec) {
		t.Fatalf("v1 implementations must advertise %q", protocol.CapabilityExec)
	}
	for _, c := range d.Capabilities {
		if !protocol.KnownCapability(c) {
			t.Errorf("unknown capability %q advertised", c)
		}
	}
	switch d.Backend.Isolation {
	case protocol.IsolationMicroVM, protocol.IsolationVM, protocol.IsolationOS:
	default:
		t.Errorf("backend.isolation = %q, want microvm|vm|os", d.Backend.Isolation)
	}
	s.disc = d
	t.Logf("discovered %s %s backend=%s/%s isolation=%s capabilities=%v", d.Name, d.Version, d.Backend.Name, d.Backend.Version, d.Backend.Isolation, d.Capabilities)
}

func (s *suite) auth(t *testing.T) {
	if s.ep.Token == "" {
		t.Skip("no token configured")
	}
	for _, tok := range []string{"", "wrong-" + s.ep.Token} {
		_, err := s.c.Discover(s.ctx, core.Endpoint{BaseURL: s.ep.BaseURL, Token: tok})
		if code := protocol.ErrorCodeOf(err); code != protocol.ErrorUnauthorized {
			t.Fatalf("discover with a missing/wrong token: error code %q (%v), want %q", code, err, protocol.ErrorUnauthorized)
		}
	}
}

func (s *suite) errorEnvelope(t *testing.T) {
	_, err := s.c.GetMachine(s.ctx, s.ep, "__missing_machine__")
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorMachineNotFound {
		t.Fatalf("missing machine: code %q (%v), want %q", code, err, protocol.ErrorMachineNotFound)
	}
	_, err = s.c.CreateMachine(s.ctx, s.ep, protocol.CreateMachineRequest{})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorBadRequest {
		t.Fatalf("create without image: code %q (%v), want %q", code, err, protocol.ErrorBadRequest)
	}
	_, err = s.c.CreateMachine(s.ctx, s.ep, protocol.CreateMachineRequest{Image: "/etc"})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorBadRequest {
		t.Fatalf("create with a host path as image: code %q (%v), want %q", code, err, protocol.ErrorBadRequest)
	}
}

func (s *suite) image(t *testing.T) {
	start := time.Now()
	img, err := s.c.PullImage(s.ctx, s.ep, s.cfg.Image)
	if err != nil {
		t.Fatalf("pull %s: %v", s.cfg.Image, err)
	}
	if img.State != protocol.ImageReady {
		t.Fatalf("pulled image state = %q", img.State)
	}
	t.Logf("pull %s: %s (digest %s)", s.cfg.Image, time.Since(start).Round(time.Millisecond), img.Digest)
	imgs, err := s.c.ListImages(s.ctx, s.ep)
	if err != nil {
		t.Fatalf("list images: %v", err)
	}
	if len(imgs) == 0 {
		t.Fatalf("list images empty after pull")
	}
}

// --- the shared machine ---

func (s *suite) resolveShareDir(t *testing.T) string {
	if !s.has(protocol.CapabilityShares) {
		return ""
	}
	if s.cfg.ShareDir != "" {
		return s.cfg.ShareDir
	}
	host := strings.TrimPrefix(strings.TrimPrefix(s.ep.BaseURL, "http://"), "https://")
	host = strings.SplitN(host, "/", 2)[0]
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return ""
	}
	dir, err := os.MkdirTemp("", "sandbox-conformance-")
	if err != nil {
		t.Fatalf("share temp dir: %v", err)
	}
	s.cleanDirs = append(s.cleanDirs, dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for _, room := range []string{"room-a/src", "room-b"} {
		if err := os.MkdirAll(filepath.Join(dir, room), 0o755); err != nil {
			t.Fatalf("share room dir: %v", err)
		}
	}
	return dir
}

func (s *suite) createMachine(t *testing.T) {
	s.secret = "sk-conformance-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	s.shareDir = s.resolveShareDir(t)
	req := protocol.CreateMachineRequest{
		Name:      "conformance-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Image:     s.cfg.Image,
		CPUs:      1,
		MemoryMiB: 512,
		Env:       map[string]string{"CONFORMANCE_VAR": "hello-env"},
		Labels:    protocol.Labels{Owner: "conformance"},
	}
	if s.has(protocol.CapabilityPorts) {
		req.Ports = []protocol.PortRequest{{Guest: 8080}}
	}
	if s.has(protocol.CapabilityDisks) {
		req.Disks = []protocol.Disk{{Name: "work", SizeMiB: 256, MountPath: "/work"}}
	}
	if s.shareDir != "" {
		req.Shares = []protocol.Share{{Name: shareName, HostPath: s.shareDir}}
	}
	start := time.Now()
	m, err := s.c.CreateMachine(s.ctx, s.ep, req)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	s.track(m)
	if m.ID == "" || m.State != protocol.StateRunning {
		t.Fatalf("create: id %q state %q, want running", m.ID, m.State)
	}
	if m.Egress.Mode != protocol.EgressNone {
		t.Fatalf("create without egress: mode %q, want %q (deny by default)", m.Egress.Mode, protocol.EgressNone)
	}
	if m.Labels != req.Labels || m.Source.Kind != protocol.SourceImage || m.Name != req.Name {
		t.Fatalf("machine = labels %+v source %+v name %q", m.Labels, m.Source, m.Name)
	}
	if s.has(protocol.CapabilityPorts) {
		if len(m.Ports) != 1 || m.Ports[0].Guest != 8080 || m.Ports[0].HostPort <= 0 || m.Ports[0].HostIP == "" {
			t.Fatalf("port bindings = %+v, want one binding for guest 8080", m.Ports)
		}
	}
	if s.shareDir != "" && (len(m.Shares) != 1 || m.Shares[0].GuestPath == "") {
		t.Fatalf("shares = %+v, want the ws share with its guest path", m.Shares)
	}
	byName, err := s.c.GetMachine(s.ctx, s.ep, req.Name)
	if err != nil || byName.ID != m.ID {
		t.Fatalf("get machine by name: %+v %v", byName.ID, err)
	}
	list, err := s.c.ListMachines(s.ctx, s.ep, protocol.Labels{Owner: "conformance"})
	if err != nil || !containsMachine(list, m.ID) {
		t.Fatalf("list by owner label does not contain %s: %v", m.ID, err)
	}
	other, err := s.c.ListMachines(s.ctx, s.ep, protocol.Labels{Owner: "someone-else"})
	if err != nil || containsMachine(other, m.ID) {
		t.Fatalf("list filtered by another owner contains %s: %v", m.ID, err)
	}
	if _, err := s.c.CreateMachine(s.ctx, s.ep, protocol.CreateMachineRequest{Name: req.Name, Image: s.cfg.Image}); protocol.ErrorCodeOf(err) != protocol.ErrorConflict {
		t.Fatalf("duplicate machine name: %v, want conflict", err)
	}
	s.main = m
	t.Logf("create machine: server createMs=%d, client %s", m.Timings.CreateMs, time.Since(start).Round(time.Millisecond))
}

func containsMachine(list []protocol.Machine, id string) bool {
	for _, m := range list {
		if m.ID == id {
			return true
		}
	}
	return false
}

// sh runs a shell script at machine level (root) and returns the result.
func (s *suite) sh(t *testing.T, machine, script string) protocol.RunResult {
	t.Helper()
	res, err := s.c.Run(s.ctx, s.ep, machine, protocol.ExecRequest{Command: []string{"sh", "-c", script}, TimeoutMs: 60_000})
	if err != nil {
		t.Fatalf("run %q: %v", script, err)
	}
	return res
}

// spaceSh runs a shell script in a space (as the space user).
func (s *suite) spaceSh(t *testing.T, space, script string) protocol.RunResult {
	t.Helper()
	res, err := s.c.SpaceRun(s.ctx, s.ep, s.main.ID, space, protocol.ExecRequest{Command: []string{"sh", "-c", script}, TimeoutMs: 60_000})
	if err != nil {
		t.Fatalf("space %s run %q: %v", space, script, err)
	}
	return res
}

func decode(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode base64 output: %v", err)
	}
	return string(raw)
}

func out(t *testing.T, r protocol.RunResult) string { return decode(t, r.StdoutBase64) }

func (s *suite) machineRun(t *testing.T) {
	start := time.Now()
	res := s.sh(t, s.main.ID, "echo hello; echo oops >&2; printf %s \"$CONFORMANCE_VAR\"")
	if res.ExitCode != 0 || out(t, res) != "hello\nhello-env" || decode(t, res.StderrBase64) != "oops\n" {
		t.Fatalf("run output = exit %d stdout %q stderr %q", res.ExitCode, out(t, res), decode(t, res.StderrBase64))
	}
	t.Logf("run (echo): server %dms, client %s", res.DurationMs, time.Since(start).Round(time.Millisecond))
	if res := s.sh(t, s.main.ID, "exit 3"); res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if res := s.sh(t, s.main.ID, "id -u"); strings.TrimSpace(out(t, res)) != "0" {
		t.Fatalf("machine-level exec user = %q, want root", out(t, res))
	}
	stdin, err := s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"cat"}, StdinBase64: base64.StdEncoding.EncodeToString([]byte("from-stdin")), TimeoutMs: 30_000})
	if err != nil || out(t, stdin) != "from-stdin" {
		t.Fatalf("stdin passthrough: %q %v", out(t, stdin), err)
	}
	cwd, err := s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"pwd"}, Workdir: "/tmp", TimeoutMs: 30_000})
	if err != nil || strings.TrimSpace(out(t, cwd)) != "/tmp" {
		t.Fatalf("workdir: %q %v", out(t, cwd), err)
	}
	timeoutStart := time.Now()
	slow, err := s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"sleep", "30"}, TimeoutMs: 1000})
	if err != nil || !slow.TimedOut || time.Since(timeoutStart) > 15*time.Second {
		t.Fatalf("timeout: %+v %v after %s", slow, err, time.Since(timeoutStart))
	}
	_, err = s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorBadRequest {
		t.Fatalf("run without command: code %q (%v), want bad_request", code, err)
	}
}

func (s *suite) machineExec(t *testing.T) {
	var events []protocol.ExecEvent
	start := time.Now()
	var firstOut time.Duration
	final, err := s.c.Exec(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"sh", "-c", "echo out; echo err >&2; sleep 0.2; echo late; exit 7"}}, func(ev protocol.ExecEvent) {
		if ev.Type == protocol.ExecStdout && firstOut == 0 {
			firstOut = time.Since(start)
		}
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if final.Type != protocol.ExecExit || final.ExitCode == nil || *final.ExitCode != 7 {
		t.Fatalf("terminal event = %+v, want exit 7", final)
	}
	if len(events) == 0 || events[0].Type != protocol.ExecStarted || events[0].ExecID == "" {
		t.Fatalf("first event = %+v, want started with an execId", events)
	}
	var stdout, stderr string
	for _, ev := range events {
		switch ev.Type {
		case protocol.ExecStdout:
			stdout += decode(t, ev.DataBase64)
		case protocol.ExecStderr:
			stderr += decode(t, ev.DataBase64)
		}
	}
	if stdout != "out\nlate\n" || stderr != "err\n" {
		t.Fatalf("streamed stdout %q stderr %q", stdout, stderr)
	}
	t.Logf("exec stream: first stdout after %s, total %s", firstOut.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
}

// --- spaces ---

func (s *suite) spaceRequest(room string) protocol.SpaceRequest {
	req := protocol.SpaceRequest{
		Env:          map[string]string{"SPACE_VAR": room},
		EnvAllowlist: []string{"FOO", "LC_*"},
		Labels:       protocol.Labels{Room: room, Core: "conformance-core", Provider: "sh", Owner: "conformance"},
	}
	if s.shareDir != "" {
		req.Mounts = append(req.Mounts, protocol.SpaceMount{Kind: protocol.MountShared, HostPath: filepath.Join(s.shareDir, room), GuestPath: "/rooms/" + room})
	}
	req.Mounts = append(req.Mounts, protocol.SpaceMount{Kind: protocol.MountDisk, GuestPath: "/data/" + room})
	return req
}

func (s *suite) spaces(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces)
	start := time.Now()
	a, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, spaceAName, s.spaceRequest(spaceAName))
	if err != nil {
		t.Fatalf("ensure space %s: %v", spaceAName, err)
	}
	t.Logf("ensure space (create): %s", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	again, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, spaceAName, s.spaceRequest(spaceAName))
	if err != nil || again.UID != a.UID || again.User != a.User {
		t.Fatalf("ensure space twice: %+v %v, want the same user/uid", again, err)
	}
	t.Logf("ensure space (idempotent): %s", time.Since(start).Round(time.Millisecond))
	b, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, spaceBName, s.spaceRequest(spaceBName))
	if err != nil {
		t.Fatalf("ensure space %s: %v", spaceBName, err)
	}
	if a.User == "" || a.UID == 0 || a.UID == b.UID || a.User == b.User {
		t.Fatalf("spaces must get distinct non-root users: a=%s/%d b=%s/%d", a.User, a.UID, b.User, b.UID)
	}
	wantWorkdir := "/data/" + spaceAName
	if s.shareDir != "" {
		wantWorkdir = "/rooms/" + spaceAName
		if a.Mounts[0].Share != shareName {
			t.Fatalf("shared mount resolved to share %q, want %q", a.Mounts[0].Share, shareName)
		}
	}
	if a.Workdir != wantWorkdir || a.MachineID != s.main.ID || a.Labels.Room != spaceAName {
		t.Fatalf("space = workdir %q machine %q labels %+v", a.Workdir, a.MachineID, a.Labels)
	}
	list, err := s.c.ListSpaces(s.ctx, s.ep, s.main.ID, protocol.Labels{Room: spaceBName})
	if err != nil || len(list) != 1 || list[0].Name != spaceBName {
		t.Fatalf("list spaces by room = %+v %v", list, err)
	}
	m, err := s.c.GetMachine(s.ctx, s.ep, s.main.ID)
	if err != nil || m.Spaces != 2 {
		t.Fatalf("machine spaces = %d %v, want 2", m.Spaces, err)
	}
	if s.shareDir != "" {
		bad := s.spaceRequest("room-x")
		bad.Mounts = []protocol.SpaceMount{{Kind: protocol.MountShared, HostPath: "/etc", GuestPath: "/rooms/x"}}
		if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, "room-x", bad); protocol.ErrorCodeOf(err) != protocol.ErrorPathNotMapped {
			t.Fatalf("shared mount outside every share: %v, want path_not_mapped", err)
		}
	}
	if !s.has(protocol.CapabilitySpaceEgress) {
		eg := s.spaceRequest("room-y")
		eg.Egress = &protocol.Egress{Mode: protocol.EgressPublic}
		if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, "room-y", eg); protocol.ErrorCodeOf(err) != protocol.ErrorCapabilityMissing {
			t.Fatalf("space egress without spaceEgress: %v, want capability_missing", err)
		}
	}
	if _, err := s.c.GetSpace(s.ctx, s.ep, s.main.ID, "__missing__"); protocol.ErrorCodeOf(err) != protocol.ErrorSpaceNotFound {
		t.Fatalf("missing space: %v, want space_not_found", err)
	}
	s.spaceA, s.spaceB = a, b
}

func (s *suite) spaceExec(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces)
	if s.spaceA.Name == "" {
		t.Skip("spaces step did not run")
	}
	res := s.spaceSh(t, spaceAName, `id -un; id -u; pwd; printf '%s\n' "$SPACE_VAR"; touch probe && echo writable`)
	want := s.spaceA.User + "\n" + strconv.Itoa(s.spaceA.UID) + "\n" + s.spaceA.Workdir + "\n" + spaceAName + "\nwritable\n"
	if out(t, res) != want {
		t.Fatalf("space exec = %q (stderr %q), want %q", out(t, res), decode(t, res.StderrBase64), want)
	}
	env, err := s.c.SpaceRun(s.ctx, s.ep, s.main.ID, spaceAName, protocol.ExecRequest{
		Command: []string{"sh", "-c", `printf '%s|%s|%s' "$FOO" "$BAR" "$LC_ALL"`},
		Env:     map[string]string{"FOO": "1", "BAR": "2", "LC_ALL": "C"},
	})
	if err != nil || out(t, env) != "1||C" {
		t.Fatalf("env allowlist: %q %v, want FOO and LC_ALL only", out(t, env), err)
	}
	if _, err := s.c.SpaceRun(s.ctx, s.ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"true"}, User: "root"}); protocol.ErrorCodeOf(err) != protocol.ErrorBadRequest {
		t.Fatalf("space exec as root: %v, want bad_request", err)
	}
	if s.shareDir == "" {
		return
	}
	host := filepath.Join(s.shareDir, spaceAName, "src")
	pwd, err := s.c.SpaceRun(s.ctx, s.ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"pwd"}, HostWorkdir: host})
	if err != nil || strings.TrimSpace(out(t, pwd)) != "/rooms/"+spaceAName+"/src" {
		t.Fatalf("hostWorkdir translation: %q %v", out(t, pwd), err)
	}
	_, err = s.c.SpaceRun(s.ctx, s.ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"pwd"}, HostWorkdir: filepath.Join(s.shareDir, spaceBName)})
	if protocol.ErrorCodeOf(err) != protocol.ErrorPathNotMapped {
		t.Fatalf("hostWorkdir of another space: %v, want path_not_mapped", err)
	}
	if err := s.c.PutSpaceFile(s.ctx, s.ep, protocol.SpaceHostFilesPath(s.main.ID, spaceAName, filepath.Join(host, "via-api.txt")), []byte("api"), 0); err != nil {
		t.Fatalf("put space file by host path: %v", err)
	}
	owner := s.spaceSh(t, spaceAName, "stat -c %U src/via-api.txt; cat src/via-api.txt")
	if out(t, owner) != s.spaceA.User+"\napi" {
		t.Fatalf("space file owner/content = %q, want %s/api", out(t, owner), s.spaceA.User)
	}
}

func (s *suite) spaceIsolation(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces)
	if s.spaceA.Name == "" {
		t.Skip("spaces step did not run")
	}
	s.spaceSh(t, spaceAName, "echo private-a > /data/"+spaceAName+"/secret.txt")
	targets := []string{"/data/" + spaceAName + "/secret.txt"}
	if s.shareDir != "" {
		s.spaceSh(t, spaceAName, "echo private-a > /rooms/"+spaceAName+"/secret.txt")
		targets = append(targets, "/rooms/"+spaceAName+"/secret.txt")
		if res := s.spaceSh(t, spaceBName, "ls "+s.main.Shares[0].GuestPath+" >/dev/null 2>&1 && echo LISTED || echo DENIED"); out(t, res) != "DENIED\n" {
			t.Fatalf("space B can list the raw share: %q", out(t, res))
		}
	}
	for _, target := range targets {
		res := s.spaceSh(t, spaceBName, "cat "+target+" 2>/dev/null && echo READ || echo DENIED")
		if strings.Contains(out(t, res), "private-a") || out(t, res) != "DENIED\n" {
			t.Fatalf("space B read space A's %s: %q", target, out(t, res))
		}
	}
	if res := s.spaceSh(t, spaceBName, "id -u"); strings.TrimSpace(out(t, res)) == "0" {
		t.Fatalf("space exec runs as root")
	}
}

func (s *suite) sharedMountLive(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces, protocol.CapabilityShares)
	if s.shareDir == "" || s.spaceA.Name == "" {
		t.Skip("no host share dir")
	}
	hostFile := filepath.Join(s.shareDir, spaceAName, "from-host.txt")
	if err := os.WriteFile(hostFile, []byte("host-wrote"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := s.spaceSh(t, spaceAName, "cat from-host.txt; echo guest-wrote > from-guest.txt"); out(t, res) != "host-wrote" {
		t.Fatalf("guest does not see the host write: %q", out(t, res))
	}
	raw, err := os.ReadFile(filepath.Join(s.shareDir, spaceAName, "from-guest.txt"))
	if err != nil || string(raw) != "guest-wrote\n" {
		t.Fatalf("host does not see the guest write: %q %v", raw, err)
	}
	info, err := os.Stat(filepath.Join(s.shareDir, spaceAName))
	if err != nil || info.Mode().Perm()&0o700 != 0o700 {
		t.Fatalf("guest ownership changes leaked to the host dir mode: %v %v", info.Mode(), err)
	}
}

// readOnlyMounts: a shared mount marked readOnly refuses writes, creates and
// deletes in the guest, and the host folder stays as it was (capability
// readOnlyMounts).
func (s *suite) readOnlyMounts(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces, protocol.CapabilityShares, protocol.CapabilityReadOnlyMounts)
	if s.shareDir == "" {
		t.Skip("no host share dir")
	}
	src := filepath.Join(s.shareDir, "ro-source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(src, "ro.txt")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	const guest = "/mnt/readonly-source"
	req := s.spaceRequest("room-ro")
	req.Mounts = append(req.Mounts, protocol.SpaceMount{Kind: protocol.MountShared, HostPath: src, GuestPath: guest, ReadOnly: true})
	if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, "room-ro", req); err != nil {
		t.Fatalf("ensure space with a read-only mount: %v", err)
	}
	t.Cleanup(func() { _ = s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, "room-ro", true) })
	script := "cat " + guest + "/ro.txt; echo; " +
		"(echo changed > " + guest + "/ro.txt) 2>/dev/null && echo WROTE; " +
		"(echo more >> " + guest + "/ro.txt) 2>/dev/null && echo APPENDED; " +
		"(touch " + guest + "/new.txt) 2>/dev/null && echo CREATED; " +
		"(rm " + guest + "/ro.txt) 2>/dev/null && echo REMOVED; true"
	if got := out(t, s.spaceSh(t, "room-ro", script)); got != "original\n" {
		t.Fatalf("a read-only mount accepted a change, or was not readable: %q", got)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != "original" {
		t.Fatalf("the host file changed through a read-only mount: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(src, "new.txt")); err == nil {
		t.Fatal("a file was created on the host through a read-only mount")
	}
}

// privateMounts: a read-only shared mount is private to its space. Two spaces
// mount different folders at one guest path and each reads its own, a space
// with no such mount sees neither, the mount stays read-only, and a writable
// mount cannot take a path another space holds privately.
func (s *suite) privateMounts(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces, protocol.CapabilityShares, protocol.CapabilityPrivateMounts)
	if s.shareDir == "" {
		t.Skip("no host share dir")
	}
	const guest = "/mnt/private-view"
	rooms := []string{"room-pv1", "room-pv2", "room-pv3"}
	for i, room := range rooms[:2] {
		src := filepath.Join(s.shareDir, "pv-source-"+room)
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "who"), []byte(room), 0o644); err != nil {
			t.Fatal(err)
		}
		req := s.spaceRequest(room)
		req.Mounts = append(req.Mounts, protocol.SpaceMount{Kind: protocol.MountShared, HostPath: src, GuestPath: guest, ReadOnly: true})
		if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, room, req); err != nil {
			t.Fatalf("space %d (%s) asking for the same private path: %v", i+1, room, err)
		}
		t.Cleanup(func() { _ = s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, room, true) })
	}
	if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, rooms[2], s.spaceRequest(rooms[2])); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, rooms[2], true) })
	for _, room := range rooms[:2] {
		if got := out(t, s.spaceSh(t, room, "cat "+guest+"/who; (touch "+guest+"/w) 2>/dev/null && echo WROTE; true")); got != room {
			t.Fatalf("%s sees %q at %s, want its own folder and a read-only view", room, got, guest)
		}
	}
	if got := out(t, s.spaceSh(t, rooms[2], "cat "+guest+"/who 2>/dev/null; true")); got != "" {
		t.Fatalf("a space that did not ask for the mount sees %q", got)
	}
	// The same path writable would be machine-wide: refused while another space holds it.
	req := s.spaceRequest("room-pv4")
	req.Mounts = append(req.Mounts, protocol.SpaceMount{Kind: protocol.MountShared, HostPath: filepath.Join(s.shareDir, "room-pv4"), GuestPath: guest})
	if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, "room-pv4", req); protocol.ErrorCodeOf(err) != protocol.ErrorConflict {
		_ = s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, "room-pv4", true)
		t.Fatalf("a writable mount over a private path: %v, want conflict", err)
	}
}

// execLatency measures the per-command overhead of the hybrid exec path.
func (s *suite) execLatency(t *testing.T) {
	run := func() error {
		if s.spaceA.Name == "" {
			_, err := s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"true"}})
			return err
		}
		_, err := s.c.SpaceRun(s.ctx, s.ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"true"}})
		return err
	}
	for i := 0; i < 5; i++ {
		if err := run(); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
	}
	const n = 100
	ds := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		if err := run(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		ds = append(ds, time.Since(start))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p50, p95 := ds[n/2], ds[n*95/100]
	t.Logf("exec `true` x%d (space run, client round trip): p50=%s p95=%s max=%s", n, p50.Round(10*time.Microsecond), p95.Round(10*time.Microsecond), ds[n-1].Round(10*time.Microsecond))
	if s.cfg.ExecP95Budget > 0 && p95 > s.cfg.ExecP95Budget {
		t.Fatalf("exec p95 %s exceeds the budget %s", p95, s.cfg.ExecP95Budget)
	}
}

func (s *suite) stdinPipe(t *testing.T) {
	var once sync.Once
	var mu sync.Mutex
	var writeErr error
	var stdout string
	final, err := s.c.Exec(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"sh", "-c", "cat; echo done"}, StdinPipe: true, TimeoutMs: 30_000}, func(ev protocol.ExecEvent) {
		switch ev.Type {
		case protocol.ExecStarted:
			once.Do(func() {
				go func() {
					err := s.c.WriteStdin(s.ctx, s.ep, s.main.ID, ev.ExecID, []byte("line-1\n"), false)
					if err == nil {
						err = s.c.WriteStdin(s.ctx, s.ep, s.main.ID, ev.ExecID, []byte("line-2\n"), true)
					}
					mu.Lock()
					writeErr = err
					mu.Unlock()
				}()
			})
		case protocol.ExecStdout:
			stdout += decode(t, ev.DataBase64)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if err != nil || writeErr != nil {
		t.Fatalf("piped stdin exec: %v / write %v", err, writeErr)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 || stdout != "line-1\nline-2\ndone\n" {
		t.Fatalf("piped stdin: exit %+v stdout %q", final.ExitCode, stdout)
	}
	if err := s.c.WriteStdin(s.ctx, s.ep, s.main.ID, "__missing_exec__", []byte("x"), true); protocol.ErrorCodeOf(err) != protocol.ErrorExecNotFound {
		t.Fatalf("stdin to a missing exec: %v, want exec_not_found", err)
	}
}

func (s *suite) tty(t *testing.T) {
	s.need(t, protocol.CapabilityTTY)
	var stdout string
	final, err := s.c.Exec(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"sh", "-c", "tty; stty size"}, TTY: true, Rows: 30, Cols: 100, TimeoutMs: 30_000}, func(ev protocol.ExecEvent) {
		if ev.Type == protocol.ExecStdout {
			stdout += decode(t, ev.DataBase64)
		}
	})
	if err != nil || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("tty exec: %+v %v (output %q)", final, err, stdout)
	}
	if !strings.Contains(stdout, "/dev/pts/") || !strings.Contains(stdout, "30 100") {
		t.Fatalf("tty output = %q, want a pts and size 30 100", stdout)
	}
}

func (s *suite) signal(t *testing.T) {
	var once sync.Once
	start := time.Now()
	final, err := s.c.Exec(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"sleep", "30"}}, func(ev protocol.ExecEvent) {
		if ev.Type == protocol.ExecStarted {
			once.Do(func() {
				go func() { _ = s.c.Signal(s.ctx, s.ep, s.main.ID, ev.ExecID, 15) }()
			})
		}
	})
	if err != nil || final.Type != protocol.ExecExit || final.ExitCode == nil || *final.ExitCode == 0 || time.Since(start) > 10*time.Second {
		t.Fatalf("SIGTERM: %+v %v after %s", final, err, time.Since(start))
	}
}

// --- security ---

// execTokens checks space-scoped exec tokens: a minted token runs commands in
// its own space only (run, streamed exec, and the control endpoints of the
// execs of its space — a renewed token controls an exec an earlier one
// started), every other route is forbidden, and expiry / space deletion
// revoke it.
func (s *suite) execTokens(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces, protocol.CapabilityExecTokens)
	if s.spaceA.Name == "" {
		t.Skip("spaces step did not run")
	}
	start := time.Now()
	tok, err := s.c.MintExecToken(s.ctx, s.ep, s.main.ID, spaceAName, 300)
	if err != nil || tok.Token == "" || tok.Token == s.ep.Token || tok.ExpiresAtMs <= time.Now().UnixMilli() {
		t.Fatalf("mint exec token: %+v %v", tok.ExpiresAtMs, err)
	}
	t.Logf("mint exec token: %s", time.Since(start).Round(time.Millisecond))
	ep := core.Endpoint{BaseURL: s.ep.BaseURL, Token: tok.Token}
	forbidden := func(what string, err error) {
		t.Helper()
		if code := protocol.ErrorCodeOf(err); code != protocol.ErrorForbidden {
			t.Fatalf("exec token %s: code %q (%v), want forbidden", what, code, err)
		}
	}

	// Allowed: run and streamed exec in its own space, as the space user, by
	// machine id and by name.
	for _, machine := range []string{s.main.ID, s.main.Name} {
		if machine == "" {
			continue
		}
		res, err := s.c.SpaceRun(s.ctx, ep, machine, spaceAName, protocol.ExecRequest{Command: []string{"id", "-un"}})
		if err != nil || strings.TrimSpace(out(t, res)) != s.spaceA.User {
			t.Fatalf("exec token run in its space (machine %q): %q %v, want user %s", machine, out(t, res), err, s.spaceA.User)
		}
	}
	var once sync.Once
	var mu sync.Mutex
	var writeErr error
	var stdout string
	final, err := s.c.SpaceExec(s.ctx, ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"sh", "-c", "cat; echo done"}, StdinPipe: true, TimeoutMs: 30_000}, func(ev protocol.ExecEvent) {
		switch ev.Type {
		case protocol.ExecStarted:
			once.Do(func() {
				go func() {
					err := s.c.WriteStdin(s.ctx, ep, s.main.ID, ev.ExecID, []byte("via-token\n"), true)
					mu.Lock()
					writeErr = err
					mu.Unlock()
				}()
			})
		case protocol.ExecStdout:
			mu.Lock()
			stdout += decode(t, ev.DataBase64)
			mu.Unlock()
		}
	})
	mu.Lock()
	if err != nil || writeErr != nil || final.ExitCode == nil || *final.ExitCode != 0 || stdout != "via-token\ndone\n" {
		mu.Unlock()
		t.Fatalf("exec token streamed exec + own stdin: %+v %v / write %v, stdout %q", final, err, writeErr, stdout)
	}
	mu.Unlock()
	var sigOnce sync.Once
	sigStart := time.Now()
	final, err = s.c.SpaceExec(s.ctx, ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"sleep", "30"}}, func(ev protocol.ExecEvent) {
		if ev.Type == protocol.ExecStarted {
			sigOnce.Do(func() { go func() { _ = s.c.Signal(s.ctx, ep, s.main.ID, ev.ExecID, 15) }() })
		}
	})
	if err != nil || final.ExitCode == nil || *final.ExitCode == 0 || time.Since(sigStart) > 10*time.Second {
		t.Fatalf("exec token signal to its own exec: %+v %v after %s", final, err, time.Since(sigStart))
	}

	// A renewed token of the same space controls an exec an earlier token
	// started (a long-lived exec outlives its first token).
	renewed, err := s.c.MintExecToken(s.ctx, s.ep, s.main.ID, spaceAName, 300)
	if err != nil {
		t.Fatalf("mint a renewed token: %v", err)
	}
	renewedEP := core.Endpoint{BaseURL: s.ep.BaseURL, Token: renewed.Token}
	var renewOnce sync.Once
	var renewErr error
	stdout = ""
	final, err = s.c.SpaceExec(s.ctx, ep, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"sh", "-c", "cat; echo done"}, StdinPipe: true, TimeoutMs: 30_000}, func(ev protocol.ExecEvent) {
		switch ev.Type {
		case protocol.ExecStarted:
			renewOnce.Do(func() {
				go func() {
					err := s.c.WriteStdin(s.ctx, renewedEP, s.main.ID, ev.ExecID, []byte("via-renewed\n"), true)
					mu.Lock()
					renewErr = err
					mu.Unlock()
					if err != nil {
						_ = s.c.WriteStdin(s.ctx, ep, s.main.ID, ev.ExecID, nil, true)
					}
				}()
			})
		case protocol.ExecStdout:
			mu.Lock()
			stdout += decode(t, ev.DataBase64)
			mu.Unlock()
		}
	})
	mu.Lock()
	if err != nil || renewErr != nil || final.ExitCode == nil || *final.ExitCode != 0 || stdout != "via-renewed\ndone\n" {
		mu.Unlock()
		t.Fatalf("a renewed token's stdin to an exec of its space: %+v %v / write %v, stdout %q", final, err, renewErr, stdout)
	}
	mu.Unlock()

	// Forbidden: everything else.
	_, err = s.c.SpaceRun(s.ctx, ep, s.main.ID, spaceBName, protocol.ExecRequest{Command: []string{"true"}})
	forbidden("run in another space", err)
	_, err = s.c.Run(s.ctx, ep, s.main.ID, protocol.ExecRequest{Command: []string{"true"}})
	forbidden("machine-level run", err)
	_, err = s.c.Discover(s.ctx, ep)
	forbidden("discover", err)
	_, err = s.c.ListMachines(s.ctx, ep, protocol.Labels{})
	forbidden("list machines", err)
	_, err = s.c.GetSpace(s.ctx, ep, s.main.ID, spaceAName)
	forbidden("get its own space", err)
	_, err = s.c.EnsureSpace(s.ctx, ep, s.main.ID, spaceAName, s.spaceRequest(spaceAName))
	forbidden("ensure its own space", err)
	_, err = s.c.MintExecToken(s.ctx, ep, s.main.ID, spaceAName, 60)
	forbidden("mint", err)
	_, err = s.c.GetFileAt(s.ctx, ep, protocol.SpaceFilesPath(s.main.ID, spaceAName, "/etc/hostname"))
	forbidden("space files", err)
	var foreignOnce sync.Once
	var foreignErr error
	_, err = s.c.Exec(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"cat"}, StdinPipe: true, TimeoutMs: 30_000}, func(ev protocol.ExecEvent) {
		if ev.Type == protocol.ExecStarted {
			foreignOnce.Do(func() {
				go func() {
					err := s.c.WriteStdin(s.ctx, ep, s.main.ID, ev.ExecID, []byte("x"), true)
					mu.Lock()
					foreignErr = err
					mu.Unlock()
					_ = s.c.WriteStdin(s.ctx, s.ep, s.main.ID, ev.ExecID, nil, true)
				}()
			})
		}
	})
	mu.Lock()
	fe := foreignErr
	mu.Unlock()
	if err != nil {
		t.Fatalf("admin exec: %v", err)
	}
	forbidden("stdin of an exec it did not start", fe)

	// Unauthorized: unknown and expired tokens.
	_, err = s.c.SpaceRun(s.ctx, core.Endpoint{BaseURL: s.ep.BaseURL, Token: "x" + tok.Token}, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"true"}})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorUnauthorized {
		t.Fatalf("unknown exec token: code %q (%v), want unauthorized", code, err)
	}
	short, err := s.c.MintExecToken(s.ctx, s.ep, s.main.ID, spaceAName, 1)
	if err != nil {
		t.Fatalf("mint short token: %v", err)
	}
	time.Sleep(time.Until(time.UnixMilli(short.ExpiresAtMs)) + 500*time.Millisecond)
	_, err = s.c.SpaceRun(s.ctx, core.Endpoint{BaseURL: s.ep.BaseURL, Token: short.Token}, s.main.ID, spaceAName, protocol.ExecRequest{Command: []string{"true"}})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorUnauthorized {
		t.Fatalf("expired exec token: code %q (%v), want unauthorized", code, err)
	}

	// Revocation: deleting the space revokes its tokens, even if a space of
	// the same name is ensured again.
	const tmp = "room-token"
	if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, tmp, protocol.SpaceRequest{Labels: protocol.Labels{Room: tmp}}); err != nil {
		t.Fatalf("ensure %s: %v", tmp, err)
	}
	tmpTok, err := s.c.MintExecToken(s.ctx, s.ep, s.main.ID, tmp, 300)
	if err != nil {
		t.Fatalf("mint for %s: %v", tmp, err)
	}
	if err := s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, tmp, true); err != nil {
		t.Fatalf("delete %s: %v", tmp, err)
	}
	if _, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, tmp, protocol.SpaceRequest{Labels: protocol.Labels{Room: tmp}}); err != nil {
		t.Fatalf("re-ensure %s: %v", tmp, err)
	}
	_, err = s.c.SpaceRun(s.ctx, core.Endpoint{BaseURL: s.ep.BaseURL, Token: tmpTok.Token}, s.main.ID, tmp, protocol.ExecRequest{Command: []string{"true"}})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorUnauthorized {
		t.Fatalf("token of a deleted space: code %q (%v), want unauthorized", code, err)
	}
	_ = s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, tmp, true)
}

// secrets runs in its own machine so the lifecycle steps on the main
// machine do not depend on secret support.
func (s *suite) secrets(t *testing.T) {
	s.need(t, protocol.CapabilitySecrets)
	m, err := s.c.CreateMachine(s.ctx, s.ep, protocol.CreateMachineRequest{
		Image: s.cfg.Image, MemoryMiB: 256,
		Secrets: []protocol.SecretRequest{{Name: "CONFORMANCE_SECRET", Value: s.secret, Hosts: []string{s.cfg.AllowHost}}},
	})
	if err != nil {
		t.Fatalf("create machine with a secret: %v", err)
	}
	s.track(m)
	s.checkSecretHidden(t, m.ID)
	if len(m.Secrets) != 1 || m.Secrets[0].Name != "CONFORMANCE_SECRET" {
		t.Fatalf("secret binding not reported: %+v", m.Secrets)
	}
	if s.has(protocol.CapabilitySnapshots) {
		// An implementation either restores secret bindings from a snapshot
		// (still never in plaintext) or refuses the snapshot up front with
		// capability_missing; it must not produce an unrestorable snapshot.
		snap, err := s.c.CreateSnapshot(s.ctx, s.ep, m.ID, protocol.CreateSnapshotRequest{})
		switch code := protocol.ErrorCodeOf(err); {
		case err == nil:
			s.cleanN = append(s.cleanN, snap.ID)
			restored, err := s.c.RestoreSnapshot(s.ctx, s.ep, snap.ID, protocol.RestoreRequest{})
			if err != nil {
				t.Fatalf("restore a snapshot with secret bindings: %v", err)
			}
			s.track(restored)
			s.checkSecretHidden(t, restored.ID)
			_ = s.c.DeleteMachine(s.ctx, s.ep, restored.ID)
		case code == protocol.ErrorCapabilityMissing:
			t.Logf("implementation refuses snapshots of secret-bearing machines: %v", err)
		default:
			t.Fatalf("snapshot of a secret-bearing machine: %v (want success or capability_missing)", err)
		}
	}
	_ = s.c.DeleteMachine(s.ctx, s.ep, m.ID)
}

// checkSecretHidden asserts the guest sees a placeholder, never the value,
// and that no endpoint echoes the value.
func (s *suite) checkSecretHidden(t *testing.T, machine string) {
	t.Helper()
	res := s.sh(t, machine, "printf %s \"$CONFORMANCE_SECRET\"; echo; env; cat /proc/self/environ 2>/dev/null | tr '\\0' '\\n'")
	text := out(t, res)
	placeholder := strings.SplitN(text, "\n", 2)[0]
	if placeholder == "" {
		t.Fatalf("secret env var CONFORMANCE_SECRET not set in guest (want a placeholder)")
	}
	if strings.Contains(text, s.secret) {
		t.Fatalf("secret value visible in guest environment")
	}
	for _, path := range []string{protocol.MachinePath(machine), protocol.PathMachines} {
		raw, err := s.c.REST.Get(s.ctx, s.ep, path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if bytes.Contains(raw, []byte(s.secret)) {
			t.Fatalf("secret value returned by GET %s", path)
		}
	}
	t.Logf("guest sees placeholder %q", placeholder)
}

// netProbe reports whether the guest can open an outbound connection.
// It uses wget, or curl on an image that has no wget.
const netProbe = `if command -v wget >/dev/null; then wget -q -T 4 -O /dev/null "$1" 2>/dev/null; else curl -sS -m 4 -o /dev/null "$1" 2>/dev/null; fi && echo REACHED || echo BLOCKED`

// tlsProbe is the same over HTTPS. It prints NOCURL when the image has no curl:
// a busybox wget cannot do TLS, so the guest cannot tell a refusal from a
// missing feature and the HTTPS check is skipped.
const tlsProbe = `command -v curl >/dev/null || { echo NOCURL; exit 0; }
if curl -sS -m 8 -o /dev/null "$1" 2>/dev/null; then echo REACHED; else echo BLOCKED; fi`

func (s *suite) probe(t *testing.T, machine, target string) string {
	t.Helper()
	res, err := s.c.Run(s.ctx, s.ep, machine, protocol.ExecRequest{Command: []string{"sh", "-c", netProbe, "probe", target}, TimeoutMs: 30_000})
	if err != nil {
		t.Fatalf("network probe: %v", err)
	}
	return strings.TrimSpace(out(t, res))
}

func (s *suite) tlsProbe(t *testing.T, machine, target string) string {
	t.Helper()
	res, err := s.c.Run(s.ctx, s.ep, machine, protocol.ExecRequest{Command: []string{"sh", "-c", tlsProbe, "probe", target}, TimeoutMs: 30_000})
	if err != nil {
		t.Fatalf("https probe: %v", err)
	}
	return strings.TrimSpace(out(t, res))
}

func (s *suite) egressDeny(t *testing.T) {
	for _, target := range []string{"http://1.1.1.1/", "http://example.com/"} {
		if got := s.probe(t, s.main.ID, target); got != "BLOCKED" {
			t.Fatalf("default egress reached %s (%s); egress must be denied by default", target, got)
		}
	}
}

func (s *suite) egressPolicy(t *testing.T) {
	s.need(t, protocol.CapabilityEgressPolicy)
	if !s.cfg.NetworkTests {
		t.Skip("network tests disabled (set Config.NetworkTests)")
	}
	m, err := s.c.CreateMachine(s.ctx, s.ep, protocol.CreateMachineRequest{
		Image: s.cfg.Image, MemoryMiB: 256,
		Egress: &protocol.Egress{Mode: protocol.EgressAllowlist, Rules: []protocol.EgressRule{{Host: s.cfg.AllowHost, Port: 80}, {Host: s.cfg.AllowHost, Port: 443}}},
	})
	if err != nil {
		t.Fatalf("create allowlist machine: %v", err)
	}
	s.track(m)
	if got := s.probe(t, m.ID, "http://"+s.cfg.AllowHost+"/"); got != "REACHED" {
		t.Fatalf("allowlisted %s not reachable (%s)", s.cfg.AllowHost, got)
	}
	if got := s.probe(t, m.ID, "http://1.1.1.1/"); got != "BLOCKED" {
		t.Fatalf("non-allowlisted 1.1.1.1 reachable under allowlist (%s)", got)
	}
	// A name rule over HTTPS must work without any secret on the machine: the
	// listed host is reachable and an unlisted name is not (2026-10-26: a
	// provider that only inspected TLS when secrets existed refused both).
	if got := s.tlsProbe(t, m.ID, "https://"+s.cfg.AllowHost+"/"); got != "NOCURL" {
		if got != "REACHED" {
			t.Fatalf("allowlisted %s not reachable over HTTPS (%s)", s.cfg.AllowHost, got)
		}
		if got := s.tlsProbe(t, m.ID, "https://api.github.com/"); got != "BLOCKED" {
			t.Fatalf("non-allowlisted api.github.com reachable over HTTPS under allowlist (%s)", got)
		}
	}
	_ = s.c.DeleteMachine(s.ctx, s.ep, m.ID)
}

// egressHosts: public with rules opens the listed hosts beside the public
// internet, private ones included, and nothing else private; without the
// capability public refuses rules.
func (s *suite) egressHosts(t *testing.T) {
	s.need(t, protocol.CapabilityEgressPolicy)
	host := s.cfg.PrivateHost
	if host == "" {
		host = "private.example"
	}
	req := protocol.CreateMachineRequest{
		Image: s.cfg.Image, MemoryMiB: 256,
		Egress: &protocol.Egress{Mode: protocol.EgressPublic, Rules: []protocol.EgressRule{{Host: host, Port: 80}}},
	}
	if !s.has(protocol.CapabilityEgressHosts) {
		if _, err := s.c.CreateMachine(s.ctx, s.ep, req); protocol.ErrorCodeOf(err) != protocol.ErrorCapabilityMissing {
			t.Fatalf("public egress with rules without egressHosts: %v, want capability_missing", err)
		}
		return
	}
	if !s.cfg.NetworkTests || s.cfg.PrivateHost == "" {
		t.Skip("needs NetworkTests and PrivateHost")
	}
	m, err := s.c.CreateMachine(s.ctx, s.ep, req)
	if err != nil {
		t.Fatalf("create public machine with a private host: %v", err)
	}
	s.track(m)
	if got := s.probe(t, m.ID, "http://"+host+"/"); got != "REACHED" {
		t.Fatalf("listed private host %s not reachable (%s)", host, got)
	}
	if got := s.probe(t, m.ID, "http://"+s.cfg.AllowHost+"/"); got != "REACHED" {
		t.Fatalf("public %s not reachable beside a listed host (%s)", s.cfg.AllowHost, got)
	}
	// Another private address, and the host itself, stay closed.
	for _, target := range []string{"http://10.255.255.254/", "http://192.168.255.254/"} {
		if got := s.probe(t, m.ID, target); got != "BLOCKED" {
			t.Fatalf("unlisted private %s reachable (%s)", target, got)
		}
	}
	_ = s.c.DeleteMachine(s.ctx, s.ep, m.ID)
}

// --- machine resources ---

func (s *suite) ports(t *testing.T) {
	s.need(t, protocol.CapabilityPorts)
	if len(s.main.Ports) != 1 {
		t.Fatalf("no port binding on the main machine")
	}
	// A one-response-per-connection HTTP responder on guest :8080 (busybox nc,
	// else python3: a Debian image has no netcat). Background processes must not
	// hold the exec's stdout/stderr open.
	s.sh(t, s.main.ID, `if command -v nc >/dev/null 2>&1; then
  (while true; do printf 'HTTP/1.0 200 OK\r\nContent-Length: 8\r\n\r\nport-ok\n' | nc -l -p 8080; done </dev/null >/dev/null 2>&1 &)
elif command -v python3 >/dev/null 2>&1; then
  (python3 -c "
import socket
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(('0.0.0.0', 8080)); s.listen(8)
while True:
    c, _ = s.accept(); c.recv(1024); c.sendall(b'HTTP/1.0 200 OK\r\nContent-Length: 8\r\n\r\nport-ok\n'); c.close()
" </dev/null >/dev/null 2>&1 &)
else
  echo NORESPONDER; exit 3
fi; sleep 0.5`)
	b := s.main.Ports[0]
	url := "http://" + net.JoinHostPort(b.HostIP, strconv.Itoa(b.HostPort)) + "/"
	var body string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(raw)
			if strings.TrimSpace(body) == "port-ok" {
				t.Logf("guest :8080 reachable at %s", url)
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("published port %s did not serve the guest listener (last body %q)", url, body)
}

func (s *suite) files(t *testing.T) {
	content := []byte("file-content\x00\x01binary\n")
	if err := s.c.PutFile(s.ctx, s.ep, s.main.ID, "/tmp/conformance/file.bin", content, 0o600); err != nil {
		t.Fatalf("put file: %v", err)
	}
	got, err := s.c.GetFile(s.ctx, s.ep, s.main.ID, "/tmp/conformance/file.bin")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("get file = %q %v", got, err)
	}
	if mode := strings.TrimSpace(out(t, s.sh(t, s.main.ID, "stat -c %a /tmp/conformance/file.bin"))); mode != "600" {
		t.Fatalf("file mode = %s, want 600", mode)
	}
	_, err = s.c.GetFile(s.ctx, s.ep, s.main.ID, "/tmp/conformance/missing")
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorFileNotFound {
		t.Fatalf("missing file: code %q (%v), want file_not_found", code, err)
	}
	_, err = s.c.GetFile(s.ctx, s.ep, s.main.ID, "relative/path")
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorBadRequest {
		t.Fatalf("relative path: code %q (%v), want bad_request", code, err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range map[string]string{"a.txt": "alpha", "dir/b.txt": "beta"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	archive := protocol.MachineArchivePath(s.main.ID, "/tmp/arch")
	if s.spaceA.Name != "" {
		// Through the space: extracted files belong to the space user.
		archive = protocol.SpaceArchivePath(s.main.ID, spaceAName, "/data/"+spaceAName+"/arch")
	}
	if err := s.c.PutArchive(s.ctx, s.ep, archive, &buf); err != nil {
		t.Fatalf("put archive: %v", err)
	}
	rc, err := s.c.GetArchive(s.ctx, s.ep, archive)
	if err != nil {
		t.Fatalf("get archive: %v", err)
	}
	defer rc.Close()
	found := map[string]string{}
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if h.Typeflag == tar.TypeReg {
			raw, _ := io.ReadAll(tr)
			found[strings.TrimPrefix(h.Name, "./")] = string(raw)
		}
	}
	if found["a.txt"] != "alpha" || found["dir/b.txt"] != "beta" {
		t.Fatalf("archive entries = %v", found)
	}
	if s.spaceA.Name != "" {
		if owner := strings.TrimSpace(out(t, s.spaceSh(t, spaceAName, "stat -c %U /data/"+spaceAName+"/arch/dir/b.txt"))); owner != s.spaceA.User {
			t.Fatalf("archive extracted through a space owned by %q, want %q", owner, s.spaceA.User)
		}
	}
}

func (s *suite) disks(t *testing.T) {
	s.need(t, protocol.CapabilityDisks)
	res := s.sh(t, s.main.ID, "echo disk-ok > /work/probe && cat /work/probe && awk '$2==\"/work\"{print $3}' /proc/mounts")
	text := out(t, res)
	if !strings.HasPrefix(text, "disk-ok\n") || strings.TrimSpace(strings.TrimPrefix(text, "disk-ok\n")) == "" {
		t.Fatalf("disk at /work: %q (exit %d, stderr %q)", text, res.ExitCode, decode(t, res.StderrBase64))
	}
	t.Logf("/work filesystem: %s", strings.TrimSpace(strings.TrimPrefix(text, "disk-ok\n")))
}

func (s *suite) pauseResume(t *testing.T) {
	s.need(t, protocol.CapabilityPause)
	start := time.Now()
	m, err := s.c.Lifecycle(s.ctx, s.ep, s.main.ID, protocol.ActionPause)
	if err != nil || m.State != protocol.StatePaused {
		t.Fatalf("pause: %+v %v", m.State, err)
	}
	pause := time.Since(start)
	_, err = s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"true"}, TimeoutMs: 5000})
	if code := protocol.ErrorCodeOf(err); code != protocol.ErrorInvalidState {
		t.Fatalf("run while paused: code %q (%v), want invalid_state", code, err)
	}
	start = time.Now()
	m, err = s.c.Lifecycle(s.ctx, s.ep, s.main.ID, protocol.ActionResume)
	if err != nil || m.State != protocol.StateRunning {
		t.Fatalf("resume: %+v %v", m.State, err)
	}
	resume := time.Since(start)
	if res := s.sh(t, s.main.ID, "echo alive"); out(t, res) != "alive\n" {
		t.Fatalf("run after resume: %q", out(t, res))
	}
	t.Logf("pause %s, resume %s", pause.Round(time.Millisecond), resume.Round(time.Millisecond))
}

// stopStart checks a cold restart keeps the machine's spaces working (users
// and mounts are re-established on the fresh boot).
func (s *suite) stopStart(t *testing.T) {
	start := time.Now()
	m, err := s.c.Lifecycle(s.ctx, s.ep, s.main.ID, protocol.ActionStop)
	if err != nil || m.State != protocol.StateStopped {
		t.Fatalf("stop: %+v %v", m.State, err)
	}
	stop := time.Since(start)
	if _, err := s.c.Run(s.ctx, s.ep, s.main.ID, protocol.ExecRequest{Command: []string{"true"}}); protocol.ErrorCodeOf(err) != protocol.ErrorInvalidState {
		t.Fatalf("run while stopped: %v, want invalid_state", err)
	}
	start = time.Now()
	m, err = s.c.Lifecycle(s.ctx, s.ep, s.main.ID, protocol.ActionStart)
	if err != nil || m.State != protocol.StateRunning {
		t.Fatalf("start: %+v %v", m.State, err)
	}
	t.Logf("stop %s, start %s", stop.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	if s.spaceA.Name == "" {
		return
	}
	script := "id -un; cat /data/" + spaceAName + "/secret.txt"
	want := s.spaceA.User + "\nprivate-a\n"
	if s.shareDir != "" {
		script += "; cat /rooms/" + spaceAName + "/from-host.txt"
		want += "host-wrote"
	}
	if res := s.spaceSh(t, spaceAName, script); out(t, res) != want {
		t.Fatalf("space after restart = %q (stderr %q), want %q", out(t, res), decode(t, res.StderrBase64), want)
	}
}

// snapshotRestore checks a full snapshot: disk state (a marker file),
// execution state (a process started before the capture) and the spaces
// survive a restore into a new machine.
func (s *suite) snapshotRestore(t *testing.T) {
	s.need(t, protocol.CapabilitySnapshots)
	s.sh(t, s.main.ID, "echo snap-marker > /root/marker; (sleep 3600 </dev/null >/dev/null 2>&1 &) ; sync")
	start := time.Now()
	snap, err := s.c.CreateSnapshot(s.ctx, s.ep, s.main.ID, protocol.CreateSnapshotRequest{Name: "conformance-" + strconv.FormatInt(time.Now().UnixNano(), 36)})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	s.cleanN = append(s.cleanN, snap.ID)
	s.snap = snap
	if snap.ID == "" || snap.Kind != protocol.SnapshotFull {
		t.Fatalf("snapshot = %+v", snap)
	}
	t.Logf("snapshot: server %dms, client %s, size %d bytes", snap.DurationMs, time.Since(start).Round(time.Millisecond), snap.SizeBytes)
	src, err := s.c.GetMachine(s.ctx, s.ep, s.main.ID)
	if err != nil || src.State != protocol.StateRunning {
		t.Fatalf("source after snapshot: state %q %v, want running", src.State, err)
	}
	snaps, err := s.c.ListSnapshots(s.ctx, s.ep)
	if err != nil || !containsSnapshot(snaps, snap.ID) {
		t.Fatalf("list snapshots missing %s: %v", snap.ID, err)
	}

	req := protocol.RestoreRequest{Labels: protocol.Labels{Owner: "conformance"}}
	if s.has(protocol.CapabilityPorts) {
		req.Ports = []protocol.PortRequest{{Guest: 8080}}
	}
	start = time.Now()
	restored, err := s.c.RestoreSnapshot(s.ctx, s.ep, snap.ID, req)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	s.track(restored)
	client := time.Since(start)
	if restored.State != protocol.StateRunning || restored.Source.Kind != protocol.SourceSnapshot || restored.Source.SnapshotID != snap.ID || restored.ID == s.main.ID {
		t.Fatalf("restored machine = id %s state %q source %+v", restored.ID, restored.State, restored.Source)
	}
	if s.has(protocol.CapabilityPorts) && (len(restored.Ports) != 1 || restored.Ports[0].HostPort == s.main.Ports[0].HostPort) {
		t.Fatalf("restored ports = %+v, want a fresh binding", restored.Ports)
	}
	if text := out(t, s.sh(t, restored.ID, "cat /root/marker; pgrep -f 'sleep 3600' >/dev/null && echo proc-alive")); text != "snap-marker\nproc-alive\n" {
		t.Fatalf("restored state = %q, want disk marker and the pre-snapshot process", text)
	}
	if s.spaceA.Name != "" {
		spaces, err := s.c.ListSpaces(s.ctx, s.ep, restored.ID, protocol.Labels{})
		if err != nil || len(spaces) != 2 {
			t.Fatalf("restored spaces = %+v %v, want 2", spaces, err)
		}
		res, err := s.c.SpaceRun(s.ctx, s.ep, restored.ID, spaceAName, protocol.ExecRequest{Command: []string{"id", "-un"}})
		if err != nil || strings.TrimSpace(out(t, res)) != s.spaceA.User {
			t.Fatalf("space exec in the restored machine: %q %v", out(t, res), err)
		}
	}
	t.Logf("restore (cow memory): server createMs=%d restoreMs=%d, client %s", restored.Timings.CreateMs, restored.Timings.RestoreMs, client.Round(time.Millisecond))
	if err := s.c.DeleteMachine(s.ctx, s.ep, restored.ID); err != nil {
		t.Fatalf("delete restored: %v", err)
	}
}

func containsSnapshot(list []protocol.Snapshot, id string) bool {
	for _, sn := range list {
		if sn.ID == id {
			return true
		}
	}
	return false
}

func (s *suite) fork(t *testing.T) {
	s.need(t, protocol.CapabilityFork)
	s.sh(t, s.main.ID, "echo fork-marker > /root/fork-marker; sync")
	start := time.Now()
	child, err := s.c.Fork(s.ctx, s.ep, s.main.ID, protocol.ForkRequest{})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	s.track(child)
	if child.State != protocol.StateRunning || child.Source.Kind != protocol.SourceFork || child.Source.MachineID != s.main.ID {
		t.Fatalf("fork = state %q source %+v", child.State, child.Source)
	}
	if text := out(t, s.sh(t, child.ID, "cat /root/fork-marker")); text != "fork-marker\n" {
		t.Fatalf("fork state = %q", text)
	}
	s.sh(t, child.ID, "echo child-only > /root/child-only")
	if res := s.sh(t, s.main.ID, "test -e /root/child-only && echo leaked || echo isolated"); out(t, res) != "isolated\n" {
		t.Fatalf("fork writes leaked into the source")
	}
	t.Logf("fork: server createMs=%d, client %s", child.Timings.CreateMs, time.Since(start).Round(time.Millisecond))
	_ = s.c.DeleteMachine(s.ctx, s.ep, child.ID)
}

func (s *suite) warmPool(t *testing.T) {
	s.need(t, protocol.CapabilityWarmPool)
	if s.snap.ID == "" {
		t.Skip("no snapshot (snapshotRestore did not run)")
	}
	name := "conformance-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	req := protocol.CreatePoolRequest{Name: name, SnapshotID: s.snap.ID, Size: 2}
	if s.has(protocol.CapabilityPorts) {
		req.Ports = []protocol.PortRequest{{Guest: 8080}}
	}
	start := time.Now()
	pool, err := s.c.CreatePool(s.ctx, s.ep, req)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	s.cleanP = append(s.cleanP, name)
	if pool.Size != 2 {
		t.Fatalf("pool = %+v", pool)
	}
	s.waitReady(t, name, 2, 2*time.Minute)
	t.Logf("pool prewarm of 2: %s", time.Since(start).Round(time.Millisecond))

	start = time.Now()
	m, err := s.c.Claim(s.ctx, s.ep, name, protocol.ClaimRequest{Labels: protocol.Labels{Owner: "conformance-claim"}})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	s.track(m)
	client := time.Since(start)
	if m.State != protocol.StateRunning || m.Source.Kind != protocol.SourcePool || m.Source.Pool != name || !m.Source.Warm {
		t.Fatalf("claimed = state %q source %+v, want running warm pool member", m.State, m.Source)
	}
	if m.Labels.Owner != "conformance-claim" {
		t.Fatalf("claimed labels = %+v", m.Labels)
	}
	if text := out(t, s.sh(t, m.ID, "cat /root/marker")); text != "snap-marker\n" {
		t.Fatalf("claimed machine state = %q", text)
	}
	t.Logf("claim (warm): server claimMs=%d, client %s", m.Timings.ClaimMs, client.Round(time.Millisecond))
	s.waitReady(t, name, 2, 2*time.Minute)

	if _, err := s.c.Claim(s.ctx, s.ep, "__missing_pool__", protocol.ClaimRequest{}); protocol.ErrorCodeOf(err) != protocol.ErrorPoolNotFound {
		t.Fatalf("claim missing pool: %v, want pool_not_found", err)
	}
	if err := s.c.DeleteSnapshot(s.ctx, s.ep, s.snap.ID); protocol.ErrorCodeOf(err) != protocol.ErrorConflict {
		t.Fatalf("delete snapshot used by a pool: %v, want conflict", err)
	}
	if err := s.c.DeletePool(s.ctx, s.ep, name); err != nil {
		t.Fatalf("delete pool: %v", err)
	}
	if _, err := s.c.GetPool(s.ctx, s.ep, name); protocol.ErrorCodeOf(err) != protocol.ErrorPoolNotFound {
		t.Fatalf("get deleted pool: %v", err)
	}
	if got, err := s.c.GetMachine(s.ctx, s.ep, m.ID); err != nil || got.State != protocol.StateRunning {
		t.Fatalf("claimed machine must outlive its pool: %+v %v", got.State, err)
	}
	_ = s.c.DeleteMachine(s.ctx, s.ep, m.ID)
}

func (s *suite) waitReady(t *testing.T, name string, ready int, within time.Duration) protocol.Pool {
	t.Helper()
	deadline := time.Now().Add(within)
	var pool protocol.Pool
	var err error
	for time.Now().Before(deadline) {
		pool, err = s.c.GetPool(s.ctx, s.ep, name)
		if err == nil && pool.Ready >= ready {
			return pool
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("pool %s not ready (%d) within %s: %+v %v", name, ready, within, pool, err)
	return pool
}

// spaceRestart: deleting a space without purge stops every one of its
// processes, and ensuring it again brings back the same user (uid) over its
// kept home. The bridge relies on both after every sandboxed turn: no guest
// process outlives the turn that started it, and the next turn finds the
// space as it left it.
func (s *suite) spaceRestart(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces)
	const name = "room-restart"
	first, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, name, protocol.SpaceRequest{Labels: protocol.Labels{Room: name}})
	if err != nil {
		t.Fatalf("ensure %s: %v", name, err)
	}
	defer func() { _ = s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, name, true) }()
	pid := strings.TrimSpace(out(t, s.spaceSh(t, name, "echo kept > \"$HOME/.restart-marker\"; sleep 3600 </dev/null >/dev/null 2>&1 & echo $!")))
	if pid == "" {
		t.Fatal("no background process started")
	}
	if err := s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, name, false); err != nil {
		t.Fatalf("delete %s without purge: %v", name, err)
	}
	if res := s.sh(t, s.main.ID, "test -d /proc/"+pid+" && echo alive; echo checked"); out(t, res) != "checked\n" {
		t.Fatalf("a process of the deleted space survived: %q", out(t, res))
	}
	again, err := s.c.EnsureSpace(s.ctx, s.ep, s.main.ID, name, protocol.SpaceRequest{Labels: protocol.Labels{Room: name}})
	if err != nil || again.UID != first.UID || again.User != first.User {
		t.Fatalf("re-ensure after delete: %+v %v, want user %s/%d back", again, err, first.User, first.UID)
	}
	if got := out(t, s.spaceSh(t, name, "cat \"$HOME/.restart-marker\"")); got != "kept\n" {
		t.Fatalf("home after a delete without purge = %q, want it kept", got)
	}
}

// durableHomes: what a space installs in its home is there on another
// machine (the first deleted), found by name; a purge empties it.
func (s *suite) durableHomes(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces, protocol.CapabilityDurableHomes)
	const name = "room-home"
	machine := func(tag string) protocol.Machine {
		t.Helper()
		m, err := s.c.CreateMachine(s.ctx, s.ep, protocol.CreateMachineRequest{
			Name: "conformance-home-" + tag + "-" + strconv.FormatInt(time.Now().UnixNano(), 36), Image: s.cfg.Image, CPUs: 1, MemoryMiB: 512,
		})
		if err != nil {
			t.Fatalf("create machine %s: %v", tag, err)
		}
		return s.track(m)
	}
	ensure := func(m protocol.Machine) {
		t.Helper()
		if _, err := s.c.EnsureSpace(s.ctx, s.ep, m.ID, name, protocol.SpaceRequest{Labels: protocol.Labels{Room: name}}); err != nil {
			t.Fatalf("ensure %s on %s: %v", name, m.ID, err)
		}
	}
	run := func(m protocol.Machine, script string) string {
		t.Helper()
		res, err := s.c.SpaceRun(s.ctx, s.ep, m.ID, name, protocol.ExecRequest{Command: []string{"sh", "-c", script}, TimeoutMs: 60_000})
		if err != nil {
			t.Fatalf("run in %s: %v", name, err)
		}
		return out(t, res)
	}
	first := machine("a")
	ensure(first)
	if got := run(first, `mkdir -p "$HOME/.local/bin" && printf '#!/bin/sh\necho tool-ran\n' > "$HOME/.local/bin/conformance-tool" && chmod +x "$HOME/.local/bin/conformance-tool" && echo kept > "$HOME/.home-marker" && test "$NPM_CONFIG_PREFIX" = "$HOME/.local" && echo prefix-ok`); got != "prefix-ok\n" {
		t.Fatalf("installing into the home: %q; want NPM_CONFIG_PREFIX at the home's .local", got)
	}
	if err := s.c.DeleteMachine(s.ctx, s.ep, first.ID); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	second := machine("b")
	ensure(second)
	if got := run(second, `cat "$HOME/.home-marker"; conformance-tool`); got != "kept\ntool-ran\n" {
		t.Fatalf("on another machine the home holds %q; want the marker and the tool found by name", got)
	}
	if err := s.c.DeleteSpace(s.ctx, s.ep, second.ID, name, true); err != nil {
		t.Fatalf("purge %s: %v", name, err)
	}
	ensure(second)
	if got := run(second, `test -e "$HOME/.home-marker" && echo left; echo checked`); got != "checked\n" {
		t.Fatalf("after a purge the home holds %q; want it empty", got)
	}
	_ = s.c.DeleteSpace(s.ctx, s.ep, second.ID, name, true)
}

// hostProxy: a machine created with HostPorts reaches that host loopback
// port at Discovery.HostAlias, under the default (deny) egress, and no other
// host port; a request for HostPorts without the capability is refused.
func (s *suite) hostProxy(t *testing.T) {
	serve := func() (net.Listener, int) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\nContent-Length: 8\r\n\r\nhost-ok\n"))
				_ = c.Close()
			}
		}()
		return ln, ln.Addr().(*net.TCPAddr).Port
	}
	open, openPort := serve()
	defer open.Close()
	closed, closedPort := serve()
	defer closed.Close()
	req := protocol.CreateMachineRequest{Image: s.cfg.Image, MemoryMiB: 256, HostPorts: []int{openPort}}
	if !s.has(protocol.CapabilityHostProxy) {
		if _, err := s.c.CreateMachine(s.ctx, s.ep, req); protocol.ErrorCodeOf(err) != protocol.ErrorCapabilityMissing {
			t.Fatalf("hostPorts without hostProxy: %v, want capability_missing", err)
		}
		return
	}
	if s.disc.HostAlias == "" {
		t.Fatal("hostProxy without a hostAlias in discovery")
	}
	m, err := s.c.CreateMachine(s.ctx, s.ep, req)
	if err != nil {
		t.Fatalf("create machine with a host port: %v", err)
	}
	s.track(m)
	if len(m.HostPorts) != 1 || m.HostPorts[0] != openPort {
		t.Fatalf("machine host ports = %v, want [%d]", m.HostPorts, openPort)
	}
	if got := s.probe(t, m.ID, "http://"+net.JoinHostPort(s.disc.HostAlias, strconv.Itoa(openPort))+"/"); got != "REACHED" {
		t.Fatalf("the host port is not reachable at %s (%s)", s.disc.HostAlias, got)
	}
	if got := s.probe(t, m.ID, "http://"+net.JoinHostPort(s.disc.HostAlias, strconv.Itoa(closedPort))+"/"); got != "BLOCKED" {
		t.Fatalf("another host port is reachable (%s)", got)
	}
	_ = s.c.DeleteMachine(s.ctx, s.ep, m.ID)
}

func (s *suite) deleteSpace(t *testing.T) {
	s.need(t, protocol.CapabilitySpaces)
	if s.spaceB.Name == "" {
		t.Skip("spaces step did not run")
	}
	s.spaceSh(t, spaceBName, "(sleep 3600 </dev/null >/dev/null 2>&1 &)")
	if err := s.c.DeleteSpace(s.ctx, s.ep, s.main.ID, spaceBName, true); err != nil {
		t.Fatalf("delete space: %v", err)
	}
	if _, err := s.c.GetSpace(s.ctx, s.ep, s.main.ID, spaceBName); protocol.ErrorCodeOf(err) != protocol.ErrorSpaceNotFound {
		t.Fatalf("get deleted space: %v, want space_not_found", err)
	}
	res := s.sh(t, s.main.ID, "id "+s.spaceB.User+" >/dev/null 2>&1 && echo user-left; pgrep -u "+strconv.Itoa(s.spaceB.UID)+" >/dev/null && echo procs-left; test -e /data/"+spaceBName+" && echo dir-left; echo checked")
	if out(t, res) != "checked\n" {
		t.Fatalf("deleted space left %q", out(t, res))
	}
	if s.shareDir != "" {
		if _, err := os.Stat(filepath.Join(s.shareDir, spaceBName)); err != nil {
			t.Fatalf("deleting a space must never delete host share content: %v", err)
		}
	}
}

func (s *suite) deleteMachine(t *testing.T) {
	if s.main.ID == "" {
		t.Skip("no machine")
	}
	start := time.Now()
	if err := s.c.DeleteMachine(s.ctx, s.ep, s.main.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t.Logf("delete machine: %s", time.Since(start).Round(time.Millisecond))
	if _, err := s.c.GetMachine(s.ctx, s.ep, s.main.ID); protocol.ErrorCodeOf(err) != protocol.ErrorMachineNotFound {
		t.Fatalf("get after delete: %v, want machine_not_found", err)
	}
	if s.snap.ID != "" {
		if err := s.c.DeleteSnapshot(s.ctx, s.ep, s.snap.ID); err != nil {
			t.Fatalf("delete snapshot: %v", err)
		}
		if err := s.c.DeleteSnapshot(s.ctx, s.ep, s.snap.ID); protocol.ErrorCodeOf(err) != protocol.ErrorSnapshotNotFound {
			t.Fatalf("delete deleted snapshot: %v, want snapshot_not_found", err)
		}
	}
}
