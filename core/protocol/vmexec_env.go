package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// Environment variables through which a sandboxExec core hands one session's
// SandboxExecution to the bnbloc-vmexec helper (and to every process it
// spawns: the CLI, its shell-prefix router, its command-rewriting hooks).
// VMExecEnv produces them and SandboxFromEnv, HostConfineFromEnv and
// PassthroughFromEnv parse them: the one encoding both sides share, and the
// only place these names are spelled (TestVMExecEnvNamesOnlyInProtocol).
// BNBLOC_VMEXEC_TOKEN is the space-scoped exec token (a secret); it can only
// run commands in the session's own space. BNBLOC_VMEXEC_TOKEN_FILE, when a
// core sets it, names a file (readable only by the member) holding the
// current token: the core rewrites it when the session's execution is
// refreshed (capability executionRefresh), and vmexec reads it at every
// command, so a long turn keeps working past its first token's expiry.
const (
	EnvVMExecURL         = "BNBLOC_VMEXEC_URL"
	EnvVMExecToken       = "BNBLOC_VMEXEC_TOKEN"
	EnvVMExecTokenFile   = "BNBLOC_VMEXEC_TOKEN_FILE"
	EnvVMExecMachine     = "BNBLOC_VMEXEC_MACHINE"
	EnvVMExecSpace       = "BNBLOC_VMEXEC_SPACE"
	EnvVMExecPath        = "BNBLOC_VMEXEC_PATH"
	EnvVMExecWorkdir     = "BNBLOC_VMEXEC_WORKDIR"
	EnvVMExecMounts      = "BNBLOC_VMEXEC_MOUNTS"      // JSON [{"host":…,"guest":…}]
	EnvVMExecPassthrough = "BNBLOC_VMEXEC_PASSTHROUGH" // comma-separated command names
	EnvVMExecEnv         = "BNBLOC_VMEXEC_ENV"         // comma-separated env names / PREFIX* forwarded beyond VMExecForwardEnv
	EnvConfineWorkspace  = "BNBLOC_CONFINE_WORKSPACE"
	EnvConfineAllowRead  = "BNBLOC_CONFINE_ALLOW_READ"  // JSON array of host paths
	EnvConfineAllowWrite = "BNBLOC_CONFINE_ALLOW_WRITE" // JSON array of host paths
	EnvConfineDenyWrite  = "BNBLOC_CONFINE_DENY_WRITE"  // JSON array of host paths
)

// VMExecEnv returns the KEY=VALUE environment entries for s. A core appends
// them to the CLI process environment for a sandboxed session.
func VMExecEnv(s SandboxExecution) []string {
	mounts := s.Mounts
	if mounts == nil {
		mounts = []PathMount{}
	}
	mountsJSON, _ := json.Marshal(mounts)
	env := []string{
		EnvVMExecURL + "=" + s.URL,
		EnvVMExecToken + "=" + s.ExecToken,
		EnvVMExecMachine + "=" + s.Machine,
		EnvVMExecSpace + "=" + s.Space,
		EnvVMExecPath + "=" + s.VMExec,
		EnvVMExecWorkdir + "=" + s.Workdir,
		EnvVMExecMounts + "=" + string(mountsJSON),
		EnvVMExecPassthrough + "=" + strings.Join(s.Passthrough, ","),
		EnvVMExecEnv + "=" + strings.Join(s.Env, ","),
	}
	if c := s.HostConfine; c != nil {
		read, _ := json.Marshal(nonNil(c.AllowRead))
		write, _ := json.Marshal(nonNil(c.AllowWrite))
		deny, _ := json.Marshal(nonNil(c.DenyWrite))
		env = append(env,
			EnvConfineWorkspace+"="+c.Workspace,
			EnvConfineAllowRead+"="+string(read),
			EnvConfineAllowWrite+"="+string(write),
			EnvConfineDenyWrite+"="+string(deny),
		)
	}
	return env
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// SandboxFromEnv parses the vmexec environment written by VMExecEnv. URL,
// token, machine, space and at least one mount are required; HostConfine is
// HostConfineFromEnv's.
func SandboxFromEnv(getenv func(string) string) (SandboxExecution, error) {
	s := SandboxExecution{
		URL:       strings.TrimSpace(getenv(EnvVMExecURL)),
		ExecToken: CurrentExecToken(getenv),
		Machine:   strings.TrimSpace(getenv(EnvVMExecMachine)),
		Space:     strings.TrimSpace(getenv(EnvVMExecSpace)),
		VMExec:    strings.TrimSpace(getenv(EnvVMExecPath)),
		Workdir:   strings.TrimSpace(getenv(EnvVMExecWorkdir)),
	}
	var missing []string
	for name, v := range map[string]string{EnvVMExecURL: s.URL, EnvVMExecToken: s.ExecToken, EnvVMExecMachine: s.Machine, EnvVMExecSpace: s.Space} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return SandboxExecution{}, fmt.Errorf("sandbox environment incomplete: %s unset", strings.Join(sorted(missing), ", "))
	}
	if raw := strings.TrimSpace(getenv(EnvVMExecMounts)); raw != "" {
		if err := json.Unmarshal([]byte(raw), &s.Mounts); err != nil {
			return SandboxExecution{}, fmt.Errorf("%s: %w", EnvVMExecMounts, err)
		}
	}
	if len(s.Mounts) == 0 {
		return SandboxExecution{}, errors.New(EnvVMExecMounts + " lists no mounts")
	}
	for _, m := range s.Mounts {
		if !strings.HasPrefix(m.Host, "/") || !strings.HasPrefix(m.Guest, "/") {
			return SandboxExecution{}, fmt.Errorf("%s: mount %+v is not an absolute host→guest pair", EnvVMExecMounts, m)
		}
	}
	s.Passthrough = PassthroughFromEnv(getenv)
	s.Env = listFromEnv(getenv, EnvVMExecEnv)
	c, err := HostConfineFromEnv(getenv)
	if err != nil {
		return SandboxExecution{}, err
	}
	s.HostConfine = c
	return s, nil
}

// readTokenFile reads the token file (a variable for tests).
var readTokenFile = os.ReadFile

// CurrentExecToken is the session's current exec token: the token file's
// (BNBLOC_VMEXEC_TOKEN_FILE, renewed by the core) when it is set and holds
// one, else BNBLOC_VMEXEC_TOKEN's. A long-lived vmexec reads it again for
// every request it sends, so a renewed token reaches a running turn.
func CurrentExecToken(getenv func(string) string) string {
	if path := strings.TrimSpace(getenv(EnvVMExecTokenFile)); path != "" {
		if raw, err := readTokenFile(path); err == nil {
			if tok := strings.TrimSpace(string(raw)); tok != "" {
				return tok
			}
		}
	}
	return strings.TrimSpace(getenv(EnvVMExecToken))
}

// PassthroughFromEnv parses BNBLOC_VMEXEC_PASSTHROUGH: the command names
// that run on the host.
func PassthroughFromEnv(getenv func(string) string) []string {
	return listFromEnv(getenv, EnvVMExecPassthrough)
}

// listFromEnv parses one comma-separated list variable, dropping blanks.
func listFromEnv(getenv func(string) string, variable string) []string {
	var out []string
	for _, name := range strings.Split(getenv(variable), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// HostConfineFromEnv parses the BNBLOC_CONFINE_* half of the environment:
// nil when none is set. Each list is honored whether or not the workspace
// variable is set (a confine invocation may name the workspace by flag), so
// a deny list in the environment is never dropped; an unparsable list is an
// error, never an empty one.
func HostConfineFromEnv(getenv func(string) string) (*HostConfine, error) {
	c := &HostConfine{Workspace: strings.TrimSpace(getenv(EnvConfineWorkspace))}
	set := c.Workspace != ""
	for _, l := range []struct {
		name string
		dst  *[]string
	}{{EnvConfineAllowRead, &c.AllowRead}, {EnvConfineAllowWrite, &c.AllowWrite}, {EnvConfineDenyWrite, &c.DenyWrite}} {
		raw := strings.TrimSpace(getenv(l.name))
		if raw == "" {
			continue
		}
		set = true
		if err := json.Unmarshal([]byte(raw), l.dst); err != nil {
			return nil, fmt.Errorf("%s: %w", l.name, err)
		}
		if len(*l.dst) == 0 {
			*l.dst = nil
		}
	}
	if !set {
		return nil, nil
	}
	return c, nil
}

func sorted(v []string) []string {
	sort.Strings(v)
	return v
}

// VMExecForwardEnv is the host environment vmexec forwards into the guest
// by default (terminal, locale, git identity — never credentials): exact
// names, or a prefix ending in "*".
var VMExecForwardEnv = []string{
	"TERM", "COLORTERM", "LANG", "LC_*", "TZ", "CI", "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "COLUMNS", "LINES",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
}

// ForwardEnvAllowlist is what one sandboxed run lets cross into the guest:
// VMExecForwardEnv plus extra, the patterns its owner allowed for their own
// runs (SandboxExecution.Env; pkgmeta.ValidForwardEnv, checked by the
// bridge; empty for any run on someone else's behalf). Both ends use it:
// the bridge as the space envAllowlist it ensures every turn, vmexec to
// pick what it forwards (ForwardedEnv), so they agree on what may cross.
// Only names cross here; each value is read from the agent's own host
// environment when a command runs.
func ForwardEnvAllowlist(extra []string) []string {
	out := append([]string(nil), VMExecForwardEnv...)
	for _, p := range extra {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// ForwardedEnv picks the entries of a KEY=VALUE list that allow admits
// (exact names, or a prefix ending in "*"; ForwardEnvAllowlist).
func ForwardedEnv(environ, allow []string) map[string]string {
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, pat := range allow {
			if k == pat || (strings.HasSuffix(pat, "*") && strings.HasPrefix(k, strings.TrimSuffix(pat, "*"))) {
				out[k] = v
				break
			}
		}
	}
	return out
}
