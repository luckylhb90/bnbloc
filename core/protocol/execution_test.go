package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

func sampleExecution() *core.Execution {
	return &core.Execution{Sandbox: core.SandboxExecution{
		URL: "http://127.0.0.1:7311", ExecToken: "tok", ExpiresAtMs: 42, Machine: "bnbloc", Space: "r1-c-p",
		VMExec: "/opt/vmexec", Workdir: "/h/bnbloc-workspaces/r1-c-p",
		Mounts:      []core.PathMount{{Host: "/h/bnbloc-workspaces/r1-c-p", Guest: "/h/bnbloc-workspaces/r1-c-p"}},
		Passthrough: []string{"gh"},
		Env:         []string{"EXAMPLE_API_KEY", "EXAMPLE_*"},
		HostConfine: core.HostConfine{Workspace: "/h/bnbloc-workspaces/r1-c-p", AllowRead: []string{"/opt/tools"}, DenyWrite: []string{"/h/bnbloc-workspaces/r1-c-p/.routing"}, DenyRead: []string{"/h/.config/acme/mail"}},
	}}
}

func TestExecutionWireShape(t *testing.T) {
	req := WireCreateSession(core.SessionRequest{WorkspaceID: "w", Prompt: "hi", Options: core.SessionOptions{Execution: sampleExecution()}})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"execution":{"mode":"sandbox"`, `"execToken":"tok"`, `"vmexec":"/opt/vmexec"`, `"mounts":[{"host":`, `"hostConfine":{"workspace":`, `"allowRead":["/opt/tools"]`, `"denyRead":["/h/.config/acme/mail"]`, `"machine":"bnbloc"`, `"workdir":`, `"env":["EXAMPLE_API_KEY","EXAMPLE_*"]`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("wire %s lacks %s", raw, key)
		}
	}
	var back CreateSessionRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got, err := back.Core("w")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Options.Execution, sampleExecution()) {
		t.Fatalf("round trip = %+v", got.Options.Execution)
	}
	// Absent execution stays absent on the wire (host execution).
	plain, _ := json.Marshal(WireCreateSession(core.SessionRequest{Prompt: "hi"}))
	if strings.Contains(string(plain), "execution") {
		t.Fatalf("host request carries execution: %s", plain)
	}
}

func TestExecutionRejectsUnknownMode(t *testing.T) {
	for _, e := range []*Execution{{Mode: "vm"}, {Mode: ExecutionModeSandbox}} {
		if _, err := (CreateSessionRequest{Execution: e}).Core("w"); err == nil {
			t.Fatalf("execution %+v accepted", e)
		}
		if _, err := (SendInputRequest{Execution: e}).CoreOptions(); err == nil {
			t.Fatalf("input execution %+v accepted", e)
		}
	}
}

func TestVMExecEnvRoundTrip(t *testing.T) {
	want := *WireExecution(sampleExecution()).Sandbox
	env := map[string]string{}
	for _, kv := range VMExecEnv(want) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	got, err := SandboxFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want.ExpiresAtMs = 0 // not carried in the environment
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env round trip:\n got %+v\nwant %+v", got, want)
	}
	if _, err := SandboxFromEnv(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), EnvVMExecToken) {
		t.Fatalf("empty env: %v", err)
	}
}

// A token file (BNBLOC_VMEXEC_TOKEN_FILE) wins over the token variable: a
// core rewrites it when the session's execution is refreshed, and vmexec
// reads it at every command; an unreadable or empty file falls back.
func TestExecTokenFromTokenFile(t *testing.T) {
	env := map[string]string{}
	for _, kv := range VMExecEnv(*WireExecution(sampleExecution()).Sandbox) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	file := filepath.Join(t.TempDir(), "token")
	env[EnvVMExecTokenFile] = file
	get := func(k string) string { return env[k] }

	if got, err := SandboxFromEnv(get); err != nil || got.ExecToken != "tok" {
		t.Fatalf("missing file: %q %v; want the variable's token", got.ExecToken, err)
	}
	if err := os.WriteFile(file, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := SandboxFromEnv(get); got.ExecToken != "tok" {
		t.Fatalf("empty file: %q; want the variable's token", got.ExecToken)
	}
	if err := os.WriteFile(file, []byte("renewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := SandboxFromEnv(get); got.ExecToken != "renewed" {
		t.Fatalf("token file: %q; want renewed", got.ExecToken)
	}
	delete(env, EnvVMExecToken)
	if got, err := SandboxFromEnv(get); err != nil || got.ExecToken != "renewed" {
		t.Fatalf("file alone: %q %v", got.ExecToken, err)
	}
}
