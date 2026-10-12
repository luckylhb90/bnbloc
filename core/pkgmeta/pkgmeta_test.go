package pkgmeta

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/protocol"
)

func TestVerifySignedPackage_passes_whenTrustedMinisignKeySignsBody(t *testing.T) {
	// Given
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	body := validEndpointPackageBody(t, protocol.SupportedMinVersion, protocol.SupportedMaxVersion)
	trusted := []TrustedKey{{KeyID: "0102030405060708", PublicKey: minisignPublicKey("0102030405060708", pub)}}

	// When
	res, err := VerifySignedPackage(body, testMinisignSignature("0102030405060708", ed25519.Sign(priv, body)), trusted)

	// Then
	if err != nil {
		t.Fatalf("verify signed package: %v", err)
	}
	if res.Package.Name != "acme-agents" || res.KeyID != "0102030405060708" || res.ContentHash == "" {
		t.Fatalf("result = %+v, want parsed package, key id, and content hash", res)
	}
}

func TestVerifySignedPackage_refuses_whenSignatureMissing(t *testing.T) {
	// Given
	body := validEndpointPackageBody(t, protocol.SupportedMinVersion, protocol.SupportedMaxVersion)

	// When
	_, err := VerifySignedPackage(body, nil, nil)

	// Then
	if err == nil || !strings.Contains(err.Error(), "signature missing") {
		t.Fatalf("error = %v, want signature missing", err)
	}
}

func TestVerifySignedPackage_refuses_whenSignerIsUntrusted(t *testing.T) {
	// Given
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	body := validEndpointPackageBody(t, protocol.SupportedMinVersion, protocol.SupportedMaxVersion)

	// When
	_, err = VerifySignedPackage(body, testMinisignSignature("0102030405060708", ed25519.Sign(priv, body)), nil)

	// Then
	if err == nil || !strings.Contains(err.Error(), "untrusted publisher key") {
		t.Fatalf("error = %v, want untrusted publisher key", err)
	}
}

func TestVerifySignedPackage_refuses_whenBodyIsTampered(t *testing.T) {
	// Given
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	body := validEndpointPackageBody(t, protocol.SupportedMinVersion, protocol.SupportedMaxVersion)
	tampered := []byte(strings.Replace(string(body), "acme-agents", "other-agents", 1))
	trusted := []TrustedKey{{KeyID: "0102030405060708", PublicKey: minisignPublicKey("0102030405060708", pub)}}

	// When
	_, err = VerifySignedPackage(tampered, testMinisignSignature("0102030405060708", ed25519.Sign(priv, body)), trusted)

	// Then
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("error = %v, want signature verification failed", err)
	}
}

func TestParsePackage_refuses_whenProtocolRangeDoesNotOverlap(t *testing.T) {
	// Given
	body := validEndpointPackageBody(t, "2.0", "2.0")

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "bnbloc supports") || !strings.Contains(err.Error(), "core supports") {
		t.Fatalf("error = %v, want both local and package protocol ranges", err)
	}
}

func TestParsePackage_refuses_whenRuntimeRequiredFieldMissing(t *testing.T) {
	// Given
	body := validEndpointPackageBody(t, protocol.SupportedMinVersion, protocol.SupportedMaxVersion)
	body = []byte(strings.Replace(string(body), `"endpoint":{"baseUrl":"https://agents.internal.example"},`, "", 1))

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), `runtime "endpoint" requires endpoint.baseUrl`) {
		t.Fatalf("error = %v, want missing endpoint error", err)
	}
}

// A manifest declaring the session-file and workdir ops passes the strict
// package decoder an install runs, and a half-declared session-file
// capability is refused before anything persists.
func TestParsePackage_acceptsSessionFileManifest(t *testing.T) {
	manifest := func() map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(`{
			"name":"acme",
			"createWorkspace":{"method":"POST","path":"/v1/workspaces","extract":{"workspaceId":"workspace.id"}},
			"createSession":{"method":"POST","path":"/v1/workspaces/{workspaceId}/sessions"},
			"sendInput":{"method":"POST","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/input"},
			"listTargets":{"method":"GET","path":"/v1/targets","targets":{"list":"targets","id":"id","provider":"provider"}},
			"messages":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/messages","items":{"list":"messages","fields":{"text":"text"}}},
			"pollTurn":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}"},
			"listSessionFiles":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/files","items":{"list":"files","fields":{"path":"path"}}},
			"readSessionFile":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/file?path={relPath}","extract":{"content":"content"}},
			"sessionWorkdir":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}","extract":{"workdir":"session.cwd"}}
		}`), &m); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		return m
	}
	m := manifest()
	body := validManifestPackageBody(t, func(b map[string]any) { b["name"] = "acme"; b["manifest"] = m })
	pkg, err := ParsePackage(body)
	if err != nil {
		t.Fatalf("ParsePackage(acme): %v", err)
	}
	if pkg.Manifest == nil || pkg.Manifest.ListSessionFiles.Path == "" || pkg.Manifest.ReadSessionFile.Path == "" || pkg.Manifest.SessionWorkdir.Path == "" {
		t.Fatalf("package lost its session-file ops: %+v", pkg.Manifest)
	}

	m = manifest()
	delete(m, "readSessionFile")
	body = validManifestPackageBody(t, func(b map[string]any) { b["name"] = "acme"; b["manifest"] = m })
	if _, err := ParsePackage(body); err == nil || !strings.Contains(err.Error(), "declared together") {
		t.Fatalf("half-declared session-file ops: err = %v, want declared-together refusal", err)
	}
}

func validEndpointPackageBody(t *testing.T, minVersion, maxVersion string) []byte {
	t.Helper()
	body := map[string]any{
		"kind":         "bnbloc.core.package/v1",
		"name":         "acme-agents",
		"version":      "1.4.2",
		"protocol":     map[string]string{"min": minVersion, "max": maxVersion},
		"runtime":      "endpoint",
		"endpoint":     map[string]string{"baseUrl": "https://agents.internal.example"},
		"capabilities": []string{"interrupt", "writeFile"},
		"permissions":  map[string]any{"network": []string{"agents.internal.example"}},
		"publisher":    "Acme Corp",
		"description":  "test core",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}
	return raw
}

func minisignPublicKey(keyID string, pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(append(minisignPrefix(keyID), pub...))
}

func testMinisignSignature(keyID string, sig []byte) []byte {
	return []byte("untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(append(minisignPrefix(keyID), sig...)) + "\n")
}

func minisignPrefix(keyID string) []byte {
	decoded, err := hex.DecodeString(keyID)
	if err != nil {
		panic(err)
	}
	return append([]byte{'E', 'd'}, decoded...)
}
