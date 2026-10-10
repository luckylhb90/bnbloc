package gateway

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	sandboxprotocol "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

func sandboxPackageBody(t *testing.T, name string, exe, vmm pkgmeta.Artifact) []byte {
	t.Helper()
	body := map[string]any{
		"kind":     pkgmeta.KindSandboxPackageV1,
		"name":     name,
		"version":  "0.1.0",
		"protocol": map[string]any{"min": sandboxprotocol.SupportedMinVersion, "max": sandboxprotocol.SupportedMaxVersion},
		"runtime":  "sidecar",
		"contract": pkgmeta.SandboxContractV1,
		"artifacts": map[string]any{
			"darwin-arm64":     map[string]string{"url": exe.URL, "sha256": exe.SHA256},
			"darwin-arm64/msb": map[string]string{"url": vmm.URL, "sha256": vmm.SHA256},
		},
		"sidecar": map[string]any{
			"localUrl": "http://127.0.0.1:7311", "listenEnv": "SBX_LISTEN", "supervisedEnv": "SBX_SUPERVISED",
			"tokenEnv": "SBX_TOKEN", "dataDirEnv": "SBX_DATA_DIR",
			"files": []map[string]any{{"name": "msb", "env": "SBX_MSB_PATH", "executable": true}},
		},
		"capabilities": []string{sandboxprotocol.CapabilityExec, sandboxprotocol.CapabilitySnapshots},
		"publisher":    "Acme Corp",
		"description":  "local microVM sandboxes",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func installSandboxComponent(t *testing.T, baseURL, adminToken, name string) (exe, vmm pkgmeta.Artifact) {
	t.Helper()
	exe = uploadComponentArtifact(t, baseURL, adminToken, []byte("sandbox sidecar bytes "+name))
	vmm = uploadComponentArtifact(t, baseURL, adminToken, []byte("vmm helper bytes "+name))
	if raw, status := postComponent(t, baseURL, adminToken, sandboxPackageBody(t, name, exe, vmm), ""); status != http.StatusOK {
		t.Fatalf("install status=%d body=%s, want 200", status, raw)
	}
	return exe, vmm
}

func TestComponentsMemberCatalog_listsInstalledSandboxWithLaunchMetadata(t *testing.T) {
	// Given an installed sidecar sandbox component with an extra VMM file.
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	_, vmm := installSandboxComponent(t, srv.URL, adminToken, "sandboxd")

	// When the member's desktop reads its catalog.
	entry := rawComponentsForMember(t, srv.URL, memberToken)["sandboxd"]

	// Then it carries everything the desktop needs to supervise it.
	if entry["kind"] != "sandbox" || entry["runtime"] != "sidecar" || entry["contract"] != pkgmeta.SandboxContractV1 || entry["requiresDesktop"] != true {
		t.Fatalf("sandbox entry = %+v", entry)
	}
	sidecar, _ := entry["sidecar"].(map[string]any)
	files, _ := sidecar["files"].([]any)
	if sidecar["tokenEnv"] != "SBX_TOKEN" || sidecar["dataDirEnv"] != "SBX_DATA_DIR" || len(files) != 1 {
		t.Fatalf("sidecar = %+v", sidecar)
	}
	artifacts, _ := entry["artifacts"].(map[string]any)
	if file, _ := artifacts["darwin-arm64/msb"].(map[string]any); file["sha256"] != vmm.SHA256 {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	caps, _ := entry["capabilities"].([]any)
	proto, _ := entry["protocol"].(map[string]any)
	if len(caps) != 2 || proto["max"] != sandboxprotocol.SupportedMaxVersion {
		t.Fatalf("capabilities=%v protocol=%v", caps, proto)
	}

	// And the extra file is downloadable by the entitled member.
	if got := getArtifactStatus(t, srv, memberToken, vmm); got != http.StatusOK {
		t.Fatalf("member download of the sandbox VMM file = %d, want 200", got)
	}
}

func TestComponentsMemberCatalog_gatesSandboxByRolloutAndTenant(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	_, vmm := installSandboxComponent(t, srv.URL, adminToken, "gated-sandbox")

	// Opt-in: listed without launch metadata, files not downloadable.
	if status := setRollout(t, srv, adminToken, "gated-sandbox", "optin"); status != http.StatusNoContent {
		t.Fatalf("set optin = %d", status)
	}
	entry := rawComponentsForMember(t, srv.URL, memberToken)["gated-sandbox"]
	if entry == nil || entry["installed"] != false || entry["sidecar"] != nil || entry["artifacts"] != nil {
		t.Fatalf("opt-in sandbox before install = %+v", entry)
	}
	if got := getArtifactStatus(t, srv, memberToken, vmm); got != http.StatusNotFound {
		t.Fatalf("download before opt-in = %d, want 404", got)
	}
	if status := doJSON(t, srv, http.MethodPost, "/v1/components/gated-sandbox/install?kind=sandbox", memberToken, ""); status != http.StatusNoContent {
		t.Fatalf("member opt-in = %d", status)
	}
	if entry := rawComponentsForMember(t, srv.URL, memberToken)["gated-sandbox"]; entry["installed"] != true || entry["sidecar"] == nil {
		t.Fatalf("opt-in sandbox after install = %+v", entry)
	}

	// Tenant scope outside the member's tenant: hidden.
	if status := doJSON(t, srv, http.MethodPut, "/v1/platform/components/gated-sandbox/tenants?kind=sandbox", adminToken, `{"enabledTenants":["elsewhere"]}`); status != http.StatusNoContent {
		t.Fatalf("set tenants = %d", status)
	}
	if entry := rawComponentsForMember(t, srv.URL, memberToken)["gated-sandbox"]; entry != nil {
		t.Fatalf("out-of-tenant sandbox leaked: %+v", entry)
	}
	if got := getArtifactStatus(t, srv, memberToken, vmm); got != http.StatusNotFound {
		t.Fatalf("out-of-tenant download = %d, want 404", got)
	}

	// Off: hidden; uninstall by kind removes it.
	if status := doJSON(t, srv, http.MethodPut, "/v1/platform/components/gated-sandbox/tenants", adminToken, `{"allTenants":true}`); status != http.StatusNoContent {
		t.Fatalf("clear tenants = %d", status)
	}
	if status := setRollout(t, srv, adminToken, "gated-sandbox", "off"); status != http.StatusNoContent {
		t.Fatalf("set off = %d", status)
	}
	if entry := rawComponentsForMember(t, srv.URL, memberToken)["gated-sandbox"]; entry != nil {
		t.Fatalf("off sandbox leaked: %+v", entry)
	}
	if status := doJSON(t, srv, http.MethodDelete, "/v1/platform/components/gated-sandbox?kind=sandbox", adminToken, ""); status != http.StatusNoContent {
		t.Fatalf("uninstall = %d", status)
	}
}

func TestSandboxPackageInstall_rejectsMissingBearerEnv(t *testing.T) {
	// Given a sandbox package without sidecar.tokenEnv.
	_, srv, adminToken, _ := rolloutTestGateway(t)
	exe := uploadComponentArtifact(t, srv.URL, adminToken, []byte("tokenless sidecar"))
	vmm := uploadComponentArtifact(t, srv.URL, adminToken, []byte("tokenless vmm"))
	var pkg map[string]any
	_ = json.Unmarshal(sandboxPackageBody(t, "tokenless", exe, vmm), &pkg)
	delete(pkg["sidecar"].(map[string]any), "tokenEnv")
	body, _ := json.Marshal(pkg)

	// When
	raw, status := postComponent(t, srv.URL, adminToken, body, "")

	// Then
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", status, raw)
	}
}
