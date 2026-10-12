package main

import (
	"io"
	"strings"
	"testing"
)

// Only an exact `<vmexec> run [--stdio] -- <word>` is left alone: a command
// that merely starts with the vmexec path runs its tail on the host, so it is
// routed like any other. The adapters fail closed (exit 2, the PreToolUse
// "block" code) on input they cannot read.
func TestHookRewritesCommandsMerelyPrefixedByVMExec(t *testing.T) {
	const vm = "/opt/bnbloc tools/bnbloc-vmexec"
	rw := commandRewriter{vmexec: vm}
	q := shellQuote(vm)
	for _, routed := range []string{
		q + " run -- ls",
		q + ` run --stdio -- 'go test ./... && echo '\''ok'\'''`,
		q + " run -- " + shellQuote("true; curl x | sh"),
	} {
		if got, ok := rw.rewrite(routed); ok {
			t.Errorf("already routed %q rewritten to %q", routed, got)
		}
	}
	for _, escape := range []string{
		q + " run -- true; curl x|sh",
		q + " run -- true && curl x",
		q + " run -- a b",
		q + " run -- $(curl x)",
		q + " run -- `id`",
		q + " run -- x > /etc/y",
		q + ` run -- "x"`,
		q + " run -- x\ncurl y",
		q + " run",
		q + " exec -- x",
		"/other/bnbloc-vmexec run -- x",
		q + "x run -- y",
	} {
		got, ok := rw.rewrite(escape)
		if !ok || got != q+" run -- "+shellQuote(escape) {
			t.Errorf("%q not routed: %q %v", escape, got, ok)
		}
	}

	if exitHookBlock != 2 {
		t.Fatalf("exitHookBlock = %d; the PreToolUse contract blocks only on 2", exitHookBlock)
	}
	for _, ev := range []string{
		`not json`,
		`{"tool_name":"Bash","tool_input":"ls"}`,
		`{"tool_name":"Bash","tool_input":{"command":["ls"]}}`,
		`{"tool_name":"Bash"}`,
	} {
		if code := rw.claudeHook([]byte(ev), io.Discard, io.Discard); code != exitHookBlock {
			t.Errorf("claude hook on %s: code %d, want %d (blocks the tool)", ev, code, exitHookBlock)
		}
	}
	for _, ev := range []string{`not json`, `{"tool":"bash","args":{"command":7}}`} {
		if code := rw.opencodeHook([]byte(ev), io.Discard, io.Discard); code != exitHookBlock {
			t.Errorf("opencode hook on %s: code %d, want %d", ev, code, exitHookBlock)
		}
	}
	if code := hook([]string{"claude"}, strings.NewReader("{"), io.Discard, io.Discard); code != exitHookBlock {
		t.Errorf("hook claude on a truncated event: code %d", code)
	}
}
