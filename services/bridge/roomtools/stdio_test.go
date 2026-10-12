package roomtools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The stdio shim relays newline-delimited JSON-RPC to the server: legacy
// initialize, a notification (no output), a modern request (headers
// mirrored from the body), and a streamed tool call whose progress and
// result each come out as one line.
func TestStdioShimRelays(t *testing.T) {
	f := newAskFixture(1)
	srv, token, sess := startServer(t, f.gw)
	sess.Bind(Job{JobID: "job_parent", ChannelID: "ch_general"})

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunStdio(context.Background(), srv.URL(), token, inR, outW, http.DefaultClient)
		_ = outW.Close()
	}()
	lines := bufio.NewScanner(outR)
	send := func(v any) {
		buf, _ := json.Marshal(v)
		if _, err := inW.Write(append(buf, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		t.Helper()
		got := make(chan map[string]any, 1)
		go func() {
			m := map[string]any{}
			if lines.Scan() {
				_ = json.Unmarshal(lines.Bytes(), &m)
			}
			got <- m
		}()
		select {
		case m := <-got:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no line from the shim")
			return nil
		}
	}

	send(rpc(1, "initialize", map[string]any{"protocolVersion": "2025-11-25"}))
	if m := next(); m["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize via stdio = %v", m)
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	send(rpc(2, "tools/list", map[string]any{"_meta": modernMeta(ModernVersion)}))
	if m := next(); m["id"] != float64(2) || m["result"].(map[string]any)["resultType"] != "complete" {
		t.Fatalf("modern tools/list via stdio = %v (the shim must mirror the headers)", m)
	}
	send(rpc(3, "tools/call", map[string]any{"name": "ask_agent", "arguments": map[string]any{"handle": "bob/claude", "task": "x"}, "_meta": map[string]any{"progressToken": 7}}))
	first := next()
	if first["method"] != "notifications/progress" {
		t.Fatalf("first streamed line = %v; want progress", first)
	}
	var final map[string]any
	for final == nil {
		if m := next(); m["id"] == float64(3) {
			final = m
		}
	}
	if sc := final["result"].(map[string]any)["structuredContent"].(map[string]any); sc["status"] != "replied" {
		t.Fatalf("ask_agent via stdio = %v", final)
	}

	// A revoked bearer turns into a JSON-RPC error line, not silence.
	srv.Revoke(sess)
	send(rpc(4, "tools/list", nil))
	if m := next(); m["error"] == nil || !strings.Contains(m["error"].(map[string]any)["message"].(string), "room tools unreachable") {
		t.Fatalf("revoked via stdio = %v", m)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("shim exit: %v", err)
	}
}
