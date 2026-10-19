// Package mcptool is the MCP tool component kind
// (bnbloc.mcp.package/v1, docs/prd/2026-10-12-experts-and-teams.md §6): its
// extpipe lifecycle and the resolution of the MCP tools in effect for a
// job (their settings' values are internal/componentsettings').
//
// Nothing runs server-side. A stdio tool's binaries are materialized by the
// member's desktop (like sidecar artifacts, never supervised) and spawned by
// the agent per session; an http tool is an endpoint component reached by
// the agent. The gateway owns the install record, the member catalog and
// the per-job resolution the bridge builds the session's MCP servers from.
package mcptool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// initializeProtocolVersion is the MCP protocol revision the install probe
// offers in initialize. It is a constant of the MCP protocol (owned by its
// specification), not deployment data: a server answers with the revision
// it speaks, and the probe only needs an answer.
const initializeProtocolVersion = "2025-06-18"

// maxProbeBody bounds what the probe reads of an initialize answer.
const maxProbeBody = 64 << 10

// endpointKind is the shared endpoint-component rule set (runtime, contract,
// base URL, permissions.network) with MCP's own discover: initialize.
var endpointKind = endpointpkg.Kind{
	Kind:        extreg.KindMCP,
	PackageKind: pkgmeta.KindMCPPackageV1,
	Label:       "mcp",
	Contract:    pkgmeta.MCPContractV1,
	Discover:    probeInitialize,
}

// Installer returns the MCP kind's lifecycle vtable.
func Installer() extpipe.KindInstaller {
	return extpipe.KindInstaller{
		Kind:         extreg.KindMCP,
		PackageKinds: []string{pkgmeta.KindMCPPackageV1},
		Decode:       DecodePackage,
		Probe:        ProbeRecord,
		// Activate registers nothing: nothing runs or is dialed
		// server-side. Rehydrating at boot never probes, so an endpoint
		// briefly down when the gateway starts stays installed.
		Activate: func(context.Context, extpipe.Record, extgov.Guard) (any, error) { return nil, nil },
	}
}

// DecodePackage projects a verified MCP package onto its persisted config.
func DecodePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		return nil, "", err
	}
	return raw, extreg.BackendSidecar, nil
}

// ValidateRecord re-validates an MCP record exactly as the install gate
// validates a package and returns its config.
func ValidateRecord(rec extpipe.Record) (extpipe.PackageConfig, error) {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return extpipe.PackageConfig{}, err
	}
	if err := pkgmeta.ValidatePackage(cfg.Package(pkgmeta.KindMCPPackageV1, rec)); err != nil {
		return extpipe.PackageConfig{}, fmt.Errorf("mcp record invalid: %w", err)
	}
	return cfg, nil
}

// ProbeRecord validates the record and, for an http tool, applies the
// endpoint rules under its network guard and probes it with initialize.
func ProbeRecord(ctx context.Context, rec extpipe.Record, guard extgov.Guard) error {
	cfg, err := ValidateRecord(rec)
	if err != nil {
		return err
	}
	if cfg.Runtime != pkgmeta.RuntimeEndpoint {
		return nil
	}
	return endpointKind.Probe(ctx, rec, guard)
}

// probeInitialize posts MCP's initialize to the server. A JSON-RPC answer
// (JSON or an SSE stream) proves an MCP server; 401/403 proves one that
// wants the credential it declares, which the probe does not send.
func probeInitialize(ctx context.Context, ep endpointpkg.Endpoint) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": initializeProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "bnbloc-install-probe", "version": "1"},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.BaseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mcp initialize request invalid: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := ep.Client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp initialize probe failed: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("mcp initialize probe status %d", resp.StatusCode)
	}
	if !jsonRPCAnswer(io.LimitReader(resp.Body, maxProbeBody), resp.Header.Get("Content-Type")) {
		return fmt.Errorf("mcp initialize probe: the endpoint did not answer with JSON-RPC")
	}
	return nil
}

// jsonRPCAnswer reports whether a body is a JSON-RPC answer, plain or as
// the data of an SSE event.
func jsonRPCAnswer(r io.Reader, contentType string) bool {
	isRPC := func(raw []byte) bool {
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		return json.Unmarshal(raw, &msg) == nil && msg.JSONRPC == "2.0" && (len(msg.Result) > 0 || len(msg.Error) > 0)
	}
	if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 4096), maxProbeBody)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok && isRPC([]byte(strings.TrimSpace(data))) {
				return true
			}
		}
		return false
	}
	raw, err := io.ReadAll(r)
	return err == nil && isRPC(raw)
}

// Transport is a config's MCP transport on the wire.
func Transport(cfg extpipe.PackageConfig) string {
	if cfg.Runtime == pkgmeta.RuntimeEndpoint {
		return wire.MCPTransportHTTP
	}
	return wire.MCPTransportStdio
}

// Host is an http tool's host (with a port when the URL names one), as the
// content permission mcp.http:<host> names it; "" for stdio.
func Host(cfg extpipe.PackageConfig) string {
	if cfg.Runtime != pkgmeta.RuntimeEndpoint || cfg.Endpoint == nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint.BaseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// SettingKeys lists a config's declared setting keys.
func SettingKeys(cfg extpipe.PackageConfig) []string {
	out := make([]string, 0, len(cfg.Settings))
	for _, s := range cfg.Settings {
		out = append(out, s.Key)
	}
	return out
}
