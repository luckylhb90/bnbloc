package protocol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

func writeErr(w http.ResponseWriter, status int, code ErrorCode) {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ProtocolError{Code: code, Message: string(code)}})
}

func TestClient_execStreamsNDJSONUntilTerminalEvent(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			writeErr(w, http.StatusUnauthorized, ErrorUnauthorized)
			return
		}
		var req ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/sandbox/v1/machines/s1/exec" || len(req.Command) != 1 {
			writeErr(w, http.StatusBadRequest, ErrorBadRequest)
			return
		}
		w.Header().Set("Content-Type", ContentTypeNDJSON)
		enc := json.NewEncoder(w)
		code := 7
		_ = enc.Encode(ExecEvent{Type: ExecStarted, Pid: 42})
		_ = enc.Encode(ExecEvent{Type: ExecStdout, DataBase64: base64.StdEncoding.EncodeToString([]byte("hi\n"))})
		_ = enc.Encode(ExecEvent{Type: ExecExit, ExitCode: &code, DurationMs: 5})
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	var seen []ExecEventType

	// When
	final, err := c.Exec(context.Background(), core.Endpoint{BaseURL: srv.URL, Token: "tok"}, "s1", ExecRequest{Command: []string{"true"}}, func(ev ExecEvent) { seen = append(seen, ev.Type) })

	// Then
	if err != nil || final.Type != ExecExit || *final.ExitCode != 7 {
		t.Fatalf("exec = %+v %v", final, err)
	}
	if len(seen) != 3 || seen[0] != ExecStarted || seen[1] != ExecStdout {
		t.Fatalf("events = %v", seen)
	}
	_, err = c.Exec(context.Background(), core.Endpoint{BaseURL: srv.URL}, "s1", ExecRequest{Command: []string{"true"}}, nil)
	if ErrorCodeOf(err) != ErrorUnauthorized {
		t.Fatalf("unauthenticated exec error = %v", err)
	}
}

func TestClient_mapsErrorEnvelopesAndRawBodies(t *testing.T) {
	// Given
	var gotMode, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sandbox/v1/machines/missing":
			writeErr(w, http.StatusNotFound, ErrorMachineNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/sandbox/v1/machines/s1/files":
			gotMode = r.URL.Query().Get("mode")
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/sandbox/v1/machines/s1/files":
			if r.URL.Query().Get("path") != "/etc/x" {
				writeErr(w, http.StatusNotFound, ErrorFileNotFound)
				return
			}
			_, _ = w.Write([]byte("content"))
		case r.Method == http.MethodGet && r.URL.Path == PathDiscover:
			_ = json.NewEncoder(w).Encode(Discovery{Name: "x", Version: "1", Contract: ContractV1, Protocol: ProtocolRange{Min: "2.0", Max: "2.0"}})
		default:
			writeErr(w, http.StatusNotFound, ErrorNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	ep := core.Endpoint{BaseURL: srv.URL}
	ctx := context.Background()

	// When / Then
	if _, err := c.GetMachine(ctx, ep, "missing"); ErrorCodeOf(err) != ErrorMachineNotFound {
		t.Fatalf("get missing = %v", err)
	}
	if err := c.PutFile(ctx, ep, "s1", "/etc/x", []byte("abc"), 0o600); err != nil || gotMode != "600" || gotBody != "abc" {
		t.Fatalf("put file: err %v mode %q body %q", err, gotMode, gotBody)
	}
	if b, err := c.GetFile(ctx, ep, "s1", "/etc/x"); err != nil || string(b) != "content" {
		t.Fatalf("get file = %q %v", b, err)
	}
	if _, err := c.GetFile(ctx, ep, "s1", "/nope"); ErrorCodeOf(err) != ErrorFileNotFound {
		t.Fatalf("get missing file = %v", err)
	}
	if _, err := c.Discover(ctx, ep); err == nil {
		t.Fatalf("incompatible major accepted")
	}
}

func TestClient_mintExecToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ExecTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Method != http.MethodPost || r.URL.Path != "/sandbox/v1/machines/bnbloc/spaces/room%2F1/exec-tokens" && r.URL.RawPath != "/sandbox/v1/machines/bnbloc/spaces/room%2F1/exec-tokens" || req.TTLSeconds != 90 {
			writeErr(w, http.StatusBadRequest, ErrorBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(ExecToken{Token: "space-tok", ExpiresAtMs: 123})
	}))
	defer srv.Close()
	tok, err := NewClient(srv.Client()).MintExecToken(context.Background(), core.Endpoint{BaseURL: srv.URL, Token: "admin"}, "bnbloc", "room/1", 90)
	if err != nil || tok.Token != "space-tok" || tok.ExpiresAtMs != 123 {
		t.Fatalf("mint = %+v %v", tok, err)
	}
	if !KnownCapability(CapabilityExecTokens) {
		t.Fatal("execTokens is not a known capability")
	}
}
