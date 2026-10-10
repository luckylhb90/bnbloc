package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/luckylhb90/bnbloc/core"
	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// runner executes one command in the sandbox space described by the
// environment. Its seams (cwd, host exec, signals) are fields so tests drive
// it without touching the real process.
type runner struct {
	cfg     coreprotocol.SandboxExecution
	client  *sandbox.Client
	environ []string
	getwd   func() (string, error)
	// hostExec replaces the process with argv on the host (passthrough).
	hostExec func(argv, env []string) error
	signals  <-chan os.Signal
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
}

func newRunner(stdin io.Reader, stdout, stderr io.Writer) (*runner, error) {
	cfg, err := coreprotocol.SandboxFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	return &runner{
		cfg:      cfg,
		client:   sandbox.NewClient(&http.Client{}),
		environ:  os.Environ(),
		getwd:    os.Getwd,
		hostExec: execHost,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
	}, nil
}

func (r *runner) endpoint() core.Endpoint {
	return core.Endpoint{BaseURL: r.cfg.URL, Token: r.cfg.ExecToken}
}

// runCommand is `vmexec run [--stdio] -- <command…>`.
func runCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	stdio, cmd, err := parseRunArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec run: %v\n", err)
		return 2
	}
	r, err := newRunner(stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec: %v\n", err)
		return exitFailure
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	r.signals = sig
	return r.run(context.Background(), cmd, stdio || !isDevNull(stdin))
}

// parseRunArgs splits `[--stdio] [--] command…`.
func parseRunArgs(args []string) (stdio bool, cmd []string, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			cmd = args[i+1:]
			i = len(args)
		case a == "--stdio":
			stdio = true
		case strings.HasPrefix(a, "-") && a != "-":
			return false, nil, fmt.Errorf("unknown flag %q", a)
		default:
			cmd = args[i:]
			i = len(args)
		}
	}
	if len(cmd) == 0 || (len(cmd) == 1 && strings.TrimSpace(cmd[0]) == "") {
		return false, nil, errors.New("no command (usage: run [--stdio] -- <command string> | <argv...>)")
	}
	return stdio, cmd, nil
}

// run executes cmd: on the host when it is an allowed passthrough command,
// otherwise in the space at the guest path of the current directory.
func (r *runner) run(ctx context.Context, cmd []string, pipeStdin bool) int {
	if isPassthrough(cmd, r.cfg.Passthrough) {
		argv := cmd
		if len(cmd) == 1 {
			argv = []string{"/bin/sh", "-c", cmd[0]}
		}
		if err := r.hostExec(argv, r.environ); err != nil {
			fmt.Fprintf(r.stderr, "bnbloc-vmexec: passthrough %s: %v\n", argv[0], err)
			return exitFailure
		}
		return 0
	}
	cwd, err := r.getwd()
	if err != nil {
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: current directory: %v\n", err)
		return exitFailure
	}
	guestCwd, ok := mapHostPath(r.cfg.Mounts, cwd)
	if !ok {
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: the current directory %s is outside the sandbox workspace (%s); refusing to run %q\n",
			cwd, strings.Join(mountHosts(r.cfg.Mounts), ", "), strings.Join(cmd, " "))
		return exitRefused
	}
	code, _ := r.exec(ctx, guestCommand(cmd), guestCwd, pipeStdin)
	return code
}

// guestCommand turns one command string into a shell invocation (bash when
// the guest has it, else POSIX sh) and keeps an argv as it is.
func guestCommand(cmd []string) []string {
	if len(cmd) == 1 {
		return shellCommand(cmd[0])
	}
	return cmd
}

func shellCommand(script string) []string {
	return []string{"sh", "-c", `if command -v bash >/dev/null 2>&1; then exec bash -c "$1"; fi; exec sh -c "$1"`, "bnbloc-vmexec", script}
}

// exec streams one command in the space: stdout/stderr byte-exact, stdin
// piped when asked, host signals forwarded. It returns the process exit code
// mapped to a shell status, and the guest-side failure (if any).
func (r *runner) exec(ctx context.Context, command []string, guestCwd string, pipeStdin bool) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ep := r.endpoint()
	req := sandbox.ExecRequest{
		Command:   command,
		Env:       coreprotocol.ForwardedEnv(r.environ),
		Workdir:   guestCwd,
		StdinPipe: pipeStdin,
	}
	var (
		mu     sync.Mutex
		execID string
	)
	started := make(chan struct{})
	var startOnce sync.Once
	go r.forwardSignals(ctx, started, &mu, &execID, cancel)
	final, err := r.client.SpaceExec(ctx, ep, r.cfg.Machine, r.cfg.Space, req, func(ev sandbox.ExecEvent) {
		switch ev.Type {
		case sandbox.ExecStarted:
			mu.Lock()
			execID = ev.ExecID
			mu.Unlock()
			startOnce.Do(func() { close(started) })
			if pipeStdin {
				go r.pumpStdin(ctx, ev.ExecID)
			}
		case sandbox.ExecStdout, sandbox.ExecStderr:
			data, derr := base64.StdEncoding.DecodeString(ev.DataBase64)
			if derr != nil {
				return
			}
			if ev.Type == sandbox.ExecStdout {
				_, _ = r.stdout.Write(data)
			} else {
				_, _ = r.stderr.Write(data)
			}
		}
	})
	if err != nil {
		return r.reportError(err), err
	}
	if final.Type == sandbox.ExecError {
		perr := final.Error
		if perr == nil {
			perr = &sandbox.ProtocolError{Code: sandbox.ErrorInternal, Message: "exec failed"}
		}
		return r.reportError(perr), perr
	}
	return exitStatus(final), nil
}

// pumpStdin copies the host stdin into the running exec, chunk by chunk, and
// closes the guest stdin at EOF.
func (r *runner) pumpStdin(ctx context.Context, execID string) {
	ep := r.endpoint()
	buf := make([]byte, 64*1024)
	for {
		n, err := r.stdin.Read(buf)
		if n > 0 {
			if werr := r.client.WriteStdin(ctx, ep, r.cfg.Machine, execID, buf[:n], false); werr != nil {
				return
			}
		}
		if err != nil {
			_ = r.client.WriteStdin(ctx, ep, r.cfg.Machine, execID, nil, true)
			return
		}
	}
}

// forwardSignals relays SIGINT/SIGTERM/SIGHUP to the guest process. A third
// signal abandons the exec (closing the stream kills it).
func (r *runner) forwardSignals(ctx context.Context, started <-chan struct{}, mu *sync.Mutex, execID *string, cancel context.CancelFunc) {
	if r.signals == nil {
		return
	}
	count := 0
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-r.signals:
			count++
			if count >= 3 {
				cancel()
				return
			}
			select {
			case <-started:
			case <-ctx.Done():
				return
			}
			mu.Lock()
			id := *execID
			mu.Unlock()
			num := 15
			if sig, ok := s.(syscall.Signal); ok {
				num = int(sig)
			}
			_ = r.client.Signal(ctx, r.endpoint(), r.cfg.Machine, id, num)
		}
	}
}

// reportError explains a sandbox failure on stderr and picks the exit code.
func (r *runner) reportError(err error) int {
	switch sandbox.ErrorCodeOf(err) {
	case sandbox.ErrorPathNotMapped:
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: the sandbox refused the working directory: %v\n", err)
		return exitRefused
	case sandbox.ErrorUnauthorized:
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: the sandbox exec token was rejected (expired or revoked): %v\n", err)
		return exitFailure
	case sandbox.ErrorForbidden:
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: the sandbox refused the command: %v\n", err)
		return exitRefused
	}
	fmt.Fprintf(r.stderr, "bnbloc-vmexec: sandbox exec failed: %v\n", err)
	return exitFailure
}

// exitStatus maps the guest exit to a shell status: a timeout is 124, a
// negative (signal) code n is 128+|n|, anything above 255 is 255.
func exitStatus(ev sandbox.ExecEvent) int {
	if ev.TimedOut {
		return 124
	}
	if ev.ExitCode == nil {
		return exitFailure
	}
	code := *ev.ExitCode
	switch {
	case code < 0:
		return 128 - code
	case code > 255:
		return 255
	}
	return code
}

// mapHostPath translates a host path through the space mounts with the
// Sandbox Protocol rule, trying the path as given and then its resolved form
// (macOS /tmp → /private/tmp).
func mapHostPath(mounts []coreprotocol.PathMount, hostPath string) (string, bool) {
	sm := make([]sandbox.SpaceMount, 0, len(mounts))
	for _, m := range mounts {
		sm = append(sm, sandbox.SpaceMount{HostPath: m.Host, GuestPath: m.Guest})
	}
	if g, ok := sandbox.MapHostPath(sm, hostPath); ok {
		return g, true
	}
	if resolved, err := filepath.EvalSymlinks(hostPath); err == nil && resolved != hostPath {
		return sandbox.MapHostPath(sm, resolved)
	}
	return "", false
}

// mapGuestPath is the reverse translation (guest → host), longest guest
// mount first.
func mapGuestPath(mounts []coreprotocol.PathMount, guestPath string) (string, bool) {
	rev := make([]sandbox.SpaceMount, 0, len(mounts))
	for _, m := range mounts {
		rev = append(rev, sandbox.SpaceMount{HostPath: m.Guest, GuestPath: m.Host})
	}
	return sandbox.MapHostPath(rev, guestPath)
}

func mountHosts(mounts []coreprotocol.PathMount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, m.Host)
	}
	return out
}

// shellMeta are the characters that make a command string more than one
// simple command; such a string never passes through to the host.
const shellMeta = ";&|<>()$`\\\n\r{}*?[]!~#'\"="

// isPassthrough reports whether cmd may run on the host: its program is a
// bare name (no path) listed in allowed, and a command string is a single
// simple command without shell syntax.
func isPassthrough(cmd []string, allowed []string) bool {
	if len(allowed) == 0 || len(cmd) == 0 {
		return false
	}
	var name string
	if len(cmd) == 1 {
		if strings.ContainsAny(cmd[0], shellMeta) {
			return false
		}
		fields := strings.Fields(cmd[0])
		if len(fields) == 0 {
			return false
		}
		name = fields[0]
	} else {
		name = cmd[0]
	}
	if strings.ContainsRune(name, '/') {
		return false
	}
	for _, a := range allowed {
		if a == name {
			return true
		}
	}
	return false
}

// isDevNull reports whether the reader is the null device (nothing to pipe).
func isDevNull(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return true
	}
	null, err := os.Stat(os.DevNull)
	return err == nil && os.SameFile(fi, null)
}
