package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Environment variables through which a sandboxExec core hands one session's
// SandboxExecution to the bnbloc-vmexec helper (and to every process it
// spawns: the CLI, its shell-prefix router, its command-rewriting hooks).
// VMExecEnv produces them and SandboxFromEnv parses them: the one encoding
// both sides share. BNBLOC_VMEXEC_TOKEN is the space-scoped exec token (a
// secret); it can only run commands in the session's own space.
const (
	EnvVMExecURL         = "BNBLOC_VMEXEC_URL"
	EnvVMExecToken       = "BNBLOC_VMEXEC_TOKEN"
	EnvVMExecMachine     = "BNBLOC_VMEXEC_MACHINE"
	EnvVMExecSpace       = "BNBLOC_VMEXEC_SPACE"
	EnvVMExecPath        = "BNBLOC_VMEXEC_PATH"
	EnvVMExecWorkdir     = "BNBLOC_VMEXEC_WORKDIR"
	EnvVMExecMounts      = "BNBLOC_VMEXEC_MOUNTS"      // JSON [{"host":…,"guest":…}]
	EnvVMExecPassthrough = "BNBLOC_VMEXEC_PASSTHROUGH" // comma-separated command names
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
// set when BNBLOC_CONFINE_WORKSPACE is.
func SandboxFromEnv(getenv func(string) string) (SandboxExecution, error) {
	s := SandboxExecution{
		URL:       strings.TrimSpace(getenv(EnvVMExecURL)),
		ExecToken: strings.TrimSpace(getenv(EnvVMExecToken)),
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
	for _, name := range strings.Split(getenv(EnvVMExecPassthrough), ",") {
		if name = strings.TrimSpace(name); name != "" {
			s.Passthrough = append(s.Passthrough, name)
		}
	}
	if ws := strings.TrimSpace(getenv(EnvConfineWorkspace)); ws != "" {
		c := &HostConfine{Workspace: ws}
		for name, dst := range map[string]*[]string{EnvConfineAllowRead: &c.AllowRead, EnvConfineAllowWrite: &c.AllowWrite, EnvConfineDenyWrite: &c.DenyWrite} {
			if raw := strings.TrimSpace(getenv(name)); raw != "" {
				if err := json.Unmarshal([]byte(raw), dst); err != nil {
					return SandboxExecution{}, fmt.Errorf("%s: %w", name, err)
				}
				if len(*dst) == 0 {
					*dst = nil
				}
			}
		}
		s.HostConfine = c
	}
	return s, nil
}

func sorted(v []string) []string {
	sort.Strings(v)
	return v
}

// VMExecForwardEnv is the host environment vmexec forwards into the guest
// (terminal, locale, git identity — never credentials): exact names, or a
// prefix ending in "*". The bridge uses the same list as the space
// envAllowlist, so both ends agree on what may cross.
var VMExecForwardEnv = []string{
	"TERM", "COLORTERM", "LANG", "LC_*", "TZ", "CI", "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "COLUMNS", "LINES",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
}

// ForwardedEnv picks the VMExecForwardEnv entries out of a KEY=VALUE list.
func ForwardedEnv(environ []string) map[string]string {
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, pat := range VMExecForwardEnv {
			if k == pat || (strings.HasSuffix(pat, "*") && strings.HasPrefix(k, strings.TrimSuffix(pat, "*"))) {
				out[k] = v
				break
			}
		}
	}
	return out
}
