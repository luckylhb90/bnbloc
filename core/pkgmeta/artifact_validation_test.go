package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/protocol"
)

func TestParsePackage_accepts_whenPluginArtifactIsExplicitUploadPlaceholder(t *testing.T) {
	// Given
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": "", "sha256": ""}}
	})

	// When
	pkg, err := ParsePackage(body)

	// Then
	if err != nil {
		t.Fatalf("parse plugin package with upload placeholder: %v", err)
	}
	if got := pkg.Artifacts["darwin-arm64"]; got.URL != "" || got.SHA256 != "" {
		t.Fatalf("placeholder = %+v, want empty url and sha256", got)
	}
}

func TestParsePackage_refuses_whenPluginArtifactIsPartialPlaceholder(t *testing.T) {
	// Given
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": ""}}
	})

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "artifacts.darwin-arm64 requires url and sha256 strings") {
		t.Fatalf("error = %v, want partial artifact placeholder rejection", err)
	}
}

func TestParsePackage_refuses_whenJsonOnlyRuntimeDeclaresArtifacts(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "endpoint",
			body: withEndpointMutation(t, validEndpointPackageBody(t, "1.0", "1.0"), func(b map[string]any) {
				b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": "", "sha256": ""}}
			}),
			want: `runtime "endpoint" must not declare artifacts`,
		},
		{
			name: "manifest",
			body: validManifestPackageBody(t, func(b map[string]any) {
				b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": "", "sha256": ""}}
			}),
			want: `runtime "manifest" must not declare artifacts`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := ParsePackage(tt.body)

			// Then
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateGatewayArtifactURLs_refusesExternalArtifactURL(t *testing.T) {
	// Given
	pkg, err := ParsePackage(validPluginPackageBody(t, nil))
	if err != nil {
		t.Fatalf("parse plugin package: %v", err)
	}

	// When
	err = ValidateGatewayArtifactURLs(pkg, "http://gateway.test")

	// Then
	if err == nil || !strings.Contains(err.Error(), "artifact URL must reference a gateway-uploaded artifact") {
		t.Fatalf("error = %v, want gateway artifact provenance rejection", err)
	}
}

func TestValidateGatewayArtifactURLs_acceptsGatewayUploadedArtifactURL(t *testing.T) {
	// Given
	sha := strings.Repeat("a", 64)
	pkg, err := ParsePackage(validPluginPackageBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": "http://gateway.test/v1/artifacts/sha256/" + sha, "sha256": sha}}
	}))
	if err != nil {
		t.Fatalf("parse plugin package: %v", err)
	}

	// When
	err = ValidateGatewayArtifactURLs(pkg, "http://gateway.test")

	// Then
	if err != nil {
		t.Fatalf("validate gateway artifact URL: %v", err)
	}
}

func withEndpointMutation(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal endpoint package: %v", err)
	}
	mutate(body)
	next, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal endpoint package: %v", err)
	}
	return next
}

func validManifestPackageBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":      "bnbloc.core.package/v1",
		"name":      "manifest-core",
		"version":   "1.0.0",
		"protocol":  map[string]string{"min": protocol.SupportedMinVersion, "max": protocol.SupportedMaxVersion},
		"runtime":   "manifest",
		"manifest":  minimalPackageManifest("manifest-core"),
		"publisher": "Acme Corp",
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal manifest package: %v", err)
	}
	return raw
}

func minimalPackageManifest(name string) map[string]any {
	verb := func(path string) map[string]any { return map[string]any{"method": "POST", "path": path} }
	getVerb := func(path string) map[string]any { return map[string]any{"method": "GET", "path": path} }
	return map[string]any{
		"name":            name,
		"createWorkspace": verb("/w"),
		"createSession":   verb("/s"),
		"sendInput":       verb("/i"),
		"listTargets":     getVerb("/t"),
		"messages":        map[string]any{"method": "GET", "path": "/m", "items": map[string]any{"list": "messages", "fields": map[string]any{"text": "text"}}},
		"pollTurn":        getVerb("/turn"),
	}
}
