package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

func TestSidecarPackageInstall_acceptsUnsignedDevPackageWithUploadedArtifact(t *testing.T) {
	// Given
	const name = "sidecar-uploaded-dev"
	svc, srv, token, _ := signedPackageGatewayConfigured(t, true, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("sidecar binary bytes"))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), core.KindCore, name, "test") })

	// When
	respBody, status := postComponent(t, srv.URL, token, body, "")

	// Then
	if status != http.StatusOK {
		t.Fatalf("install status = %d body=%s, want unsigned dev sidecar accepted", status, respBody)
	}
	components := listComponentsForAdmin(t, srv.URL, token)
	for _, component := range components {
		if component.Name != name {
			continue
		}
		got := component.Artifacts["darwin-arm64"]
		if got.URL != artifact.URL || got.SHA256 != artifact.SHA256 {
			t.Fatalf("artifact = %+v, want uploaded %+v", got, artifact)
		}
		return
	}
	t.Fatalf("component %q missing from admin list", name)
}

func TestSidecarPackageInstall_rejectsExternalArtifactURL(t *testing.T) {
	// Given
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	body := sidecarPackageBody(t, "sidecar-external-artifact")

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "artifact URL must reference a gateway-uploaded artifact") {
		t.Fatalf("status=%d body=%s, want gateway artifact provenance refusal", status, respBody)
	}
}

func TestSidecarPackageInstall_rejectsMissingUploadedArtifact(t *testing.T) {
	// Given
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	missing := pkgmeta.Artifact{URL: srv.URL + "/v1/artifacts/sha256/" + missingHex, SHA256: missingHex}
	body := sidecarPackageBodyWithArtifact(t, "sidecar-missing-upload", missing)

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "artifact not found in gateway artifact store") {
		t.Fatalf("status=%d body=%s, want missing gateway artifact refusal", status, respBody)
	}
}

func TestSidecarPackageInstall_rejectsUploadedArtifactHashMismatch(t *testing.T) {
	// Given
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("real sidecar bytes"))
	artifact.SHA256 = strings.Repeat("a", 64)
	body := sidecarPackageBodyWithArtifact(t, "sidecar-hash-mismatch", artifact)

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "url key must match sha256") {
		t.Fatalf("status=%d body=%s, want artifact URL/hash mismatch refusal", status, respBody)
	}
}

func TestSidecarPackageInstall_rejectsUploadedArtifactBytesHashMismatch(t *testing.T) {
	// Given
	const name = "sidecar-corrupt-upload"
	blobDir := t.TempDir()
	blobs, err := blobstore.NewDiskStore(blobDir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	rt := &realtimehttp.Service{Blobs: blobs}
	rtSrv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), rt.Handler()))
	t.Cleanup(rtSrv.Close)
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, false)
	svc.Backends.Realtime = rtSrv.URL
	svc.TenantResolver = func(auth.Identity, string) (string, string) { return "home", string(rbac.RoleSuperAdmin) }
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("declared sidecar bytes"))
	artifactKey := strings.TrimPrefix(artifact.URL, srv.URL+"/v1/artifacts/sha256/")
	if err := os.WriteFile(filepath.Join(blobDir, artifactKey[:2], artifactKey), []byte("corrupted sidecar bytes"), 0o644); err != nil {
		t.Fatalf("corrupt uploaded artifact: %v", err)
	}
	body := sidecarPackageBodyWithArtifact(t, name, artifact)

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "artifact bytes do not match sha256") {
		t.Fatalf("status=%d body=%s, want uploaded artifact bytes/hash mismatch refusal", status, respBody)
	}
	components := listComponentsForAdmin(t, srv.URL, token)
	for _, component := range components {
		if component.Name == name {
			t.Fatalf("component %q persisted after artifact probe failure", name)
		}
	}
}

func TestSidecarPackageInstall_rejectsSignatureOverPreUploadBody(t *testing.T) {
	// Given
	const name = "sidecar-signed-final-body"
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	placeholder := sidecarPackageBodyWithArtifact(t, name, pkgmeta.Artifact{URL: "", SHA256: ""})
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("signed sidecar bytes"))
	finalBody := sidecarPackageBodyWithArtifact(t, name, artifact)

	// When
	respBody, status := postComponent(t, srv.URL, token, finalBody, signPackage(key.private, placeholder))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "signature verification failed") {
		t.Fatalf("status=%d body=%s, want signature over placeholder body rejected", status, respBody)
	}
}

func TestSidecarPackageInstall_acceptsSignatureOverFinalUploadedBody(t *testing.T) {
	// Given
	const name = "sidecar-signed-final-uploaded"
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("final signed sidecar bytes"))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), core.KindCore, name, "test") })

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusOK {
		t.Fatalf("install status = %d body=%s, want signature over final uploaded body accepted", status, respBody)
	}
}
