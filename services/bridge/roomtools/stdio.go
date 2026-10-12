package roomtools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// The stdio shim lets an agent that speaks only stdio MCP reach the room
// tools server: it reads newline-delimited JSON-RPC from stdin, POSTs each
// message to the server with the session bearer, and writes every JSON-RPC
// message of the answer (a JSON body, or each event of an SSE stream) to
// stdout as one line. It holds no state of its own beyond in-flight
// requests, so the server stays the one implementation of the protocol.
//
// It runs as `bnbloc-bridge roomtools-stdio --url <endpoint>` with the
// bearer in BNBLOC_ROOMTOOLS_TOKEN (env, never argv).

// maxStdioLine bounds one inbound JSON-RPC line.
const maxStdioLine = maxRequestBytes

// RunStdioMain is the `roomtools-stdio` subcommand: it parses args, reads
// the bearer from the environment and runs the shim on stdin/stdout until
// stdin closes. It returns the process exit code.
func RunStdioMain(args []string) int {
	endpoint := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--url" && i+1 < len(args):
			endpoint = args[i+1]
			i++
		case strings.HasPrefix(a, "--url="):
			endpoint = strings.TrimPrefix(a, "--url=")
		default:
			fmt.Fprintf(os.Stderr, "roomtools-stdio: unknown argument %q (usage: roomtools-stdio --url <endpoint>, bearer in %s)\n", a, TokenEnv)
			return 2
		}
	}
	token := strings.TrimSpace(os.Getenv(TokenEnv))
	if endpoint == "" || token == "" {
		fmt.Fprintf(os.Stderr, "roomtools-stdio: --url and %s are required\n", TokenEnv)
		return 2
	}
	if err := RunStdio(context.Background(), endpoint, token, os.Stdin, os.Stdout, http.DefaultClient); err != nil {
		fmt.Fprintf(os.Stderr, "roomtools-stdio: %v\n", err)
		return 1
	}
	return 0
}

// RunStdio relays stdin to the server at endpoint until in closes or ctx
// ends. Requests run concurrently (a long ask_agent does not block a
// listing); a stdio notifications/cancelled aborts the matching request.
func RunStdio(ctx context.Context, endpoint, token string, in io.Reader, out io.Writer, client *http.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var outMu sync.Mutex
	emit := func(line []byte) {
		outMu.Lock()
		defer outMu.Unlock()
		_, _ = out.Write(append(bytes.TrimSpace(line), '\n'))
	}
	var flight sync.Map // request id (raw JSON) -> context.CancelFunc
	var wg sync.WaitGroup
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxStdioLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := append([]byte(nil), line...)
		var head struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				RequestID json.RawMessage `json:"requestId"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &head); err != nil {
			emit([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`))
			continue
		}
		if head.Method == "notifications/cancelled" {
			if c, ok := flight.LoadAndDelete(string(head.Params.RequestID)); ok {
				c.(context.CancelFunc)()
			}
			continue
		}
		reqCtx, reqCancel := context.WithCancel(ctx)
		if len(head.ID) > 0 {
			flight.Store(string(head.ID), reqCancel)
		}
		wg.Add(1)
		go func(id json.RawMessage) {
			defer wg.Done()
			defer reqCancel()
			defer flight.Delete(string(id))
			if err := relay(reqCtx, client, endpoint, token, msg, emit); err != nil && len(id) > 0 && reqCtx.Err() == nil {
				failure, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "room tools unreachable: " + err.Error()}})
				emit(failure)
			}
		}(head.ID)
	}
	err := sc.Err()
	// stdin closed: let in-flight answers finish (the agent may still read
	// them), then stop.
	wg.Wait()
	return err
}

// relay POSTs one message and emits every JSON-RPC message of the answer.
func relay(ctx context.Context, client *http.Client, endpoint, token string, msg []byte, emit func([]byte)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	mirrorHeaders(req, msg)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil // a notification
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relaySSE(resp.Body, emit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if !json.Valid(body) {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	emit(body)
	return nil
}

// mirrorHeaders sets the modern request-metadata headers from the body (a
// stdio client sends none; the HTTP binding requires them).
func mirrorHeaders(req *http.Request, msg []byte) {
	var body struct {
		Method string `json:"method"`
		Params struct {
			Name string                     `json:"name"`
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(msg, &body) != nil {
		return
	}
	var version string
	if raw, ok := body.Params.Meta[metaProtocolVersion]; ok && json.Unmarshal(raw, &version) == nil && version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
		req.Header.Set("Mcp-Method", body.Method)
		if body.Params.Name != "" {
			req.Header.Set("Mcp-Name", encodeHeaderValue(body.Params.Name))
		}
	}
}

// relaySSE emits the data of every SSE event (one JSON-RPC message each).
func relaySSE(r io.Reader, emit func([]byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var data bytes.Buffer
	flush := func() {
		if data.Len() > 0 {
			emit(data.Bytes())
			data.Reset()
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
