package execbridge

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The bridge forwards each WebSocket text frame to the spawned exec-server as one JSON line,
// and each line it prints back as one frame; the spawned command is the one the caller built.
func TestBridgeForwardsFramesBothWays(t *testing.T) {
	var spawned []string
	br, err := Start(func(ctx context.Context, args ...string) *exec.Cmd {
		spawned = args
		// A stand-in exec-server: answers every request line with a result line.
		return exec.CommandContext(ctx, "sh", "-c", `while IFS= read -r l; do echo '{"jsonrpc":"2.0","id":1,"result":"ok"}'; done`)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, br.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	// A pretty-printed message with a raw newline must still be one line to the exec-server.
	if err := c.Write(ctx, websocket.MessageText, []byte("{\"jsonrpc\":\"2.0\",\n\"id\":1,\"method\":\"initialize\"}")); err != nil {
		t.Fatal(err)
	}
	_, got, err := c.Read(ctx)
	if err != nil || !strings.Contains(string(got), `"result":"ok"`) {
		t.Fatalf("read %q, %v", got, err)
	}
	if strings.Join(spawned, " ") != strings.Join(ExecServerArgv, " ") {
		t.Fatalf("spawned %v, want %v", spawned, ExecServerArgv)
	}
}

// The path is the capability: another path is a 404, and Close ends what the bridge started.
func TestBridgeRefusesAnotherPathAndCloseEndsChildren(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	br, err := Start(func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo $$ > "+pidFile+"; exec sleep 30")
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wrong := strings.Replace(br.URL(), br.path, "/not-the-path", 1)
	if _, resp, err := websocket.Dial(ctx, wrong, nil); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("a wrong path must be refused with 404 (%v, %v)", resp, err)
	}
	c, _, err := websocket.Dial(ctx, br.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	var pid int
	for i := 0; i < 50 && pid == 0; i++ {
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the exec-server never started")
	}
	br.Close()
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d still runs after Close", pid)
}
