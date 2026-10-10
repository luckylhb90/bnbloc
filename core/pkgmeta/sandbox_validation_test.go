package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func sandboxArtifact(b byte) map[string]any {
	return map[string]any{"url": "https://cdn.example/" + string(b), "sha256": strings.Repeat(string(b), 64)}
}

func validSandboxSidecarBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":     KindSandboxPackageV1,
		"name":     "sandboxd",
		"version":  "0.1.0",
		"protocol": map[string]any{"min": "1.0", "max": "1.0"},
		"runtime":  "sidecar",
		"contract": SandboxContractV1,
		"artifacts": map[string]any{
			"darwin-arm64":           sandboxArtifact('a'),
			"darwin-arm64/msb":       sandboxArtifact('b'),
			"darwin-arm64/libkrunfw": sandboxArtifact('c'),
		},
		"sidecar": map[string]any{
			"localUrl": "http://127.0.0.1:7311", "listenEnv": "SBX_LISTEN", "supervisedEnv": "SBX_SUPERVISED",
			"tokenEnv": "SBX_TOKEN", "dataDirEnv": "SBX_DATA_DIR",
			"files": []map[string]any{
				{"name": "msb", "env": "SBX_MSB_PATH", "executable": true},
				{"name": "libkrunfw", "env": "SBX_LIBKRUNFW_PATH"},
			},
		},
		"capabilities": []string{"exec", "ports", "disks", "snapshots", "pause", "egressPolicy", "secrets", "fork", "warmPool"},
		"publisher":    "Acme Corp",
		"description":  "local microVM sandboxes",
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validSandboxEndpointBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	return validSandboxSidecarBody(t, func(b map[string]any) {
		b["runtime"] = "endpoint"
		delete(b, "artifacts")
		delete(b, "sidecar")
		b["endpoint"] = map[string]any{"baseUrl": "https://sandboxes.example.com/api"}
		b["permissions"] = map[string]any{"network": []string{"sandboxes.example.com"}}
		if mutate != nil {
			mutate(b)
		}
	})
}

func sidecarField(b map[string]any) map[string]any { return b["sidecar"].(map[string]any) }

func TestParsePackage_parses_whenSandboxSidecarPackageIsValid(t *testing.T) {
	// Given a sidecar sandbox package with two extra platform files.
	body := validSandboxSidecarBody(t, nil)

	// When
	pkg, err := ParsePackage(body)

	// Then
	if err != nil {
		t.Fatalf("parse sandbox package: %v", err)
	}
	if pkg.Sidecar.TokenEnv != "SBX_TOKEN" || pkg.Sidecar.DataDirEnv != "SBX_DATA_DIR" || len(pkg.Sidecar.Files) != 2 || !pkg.Sidecar.Files[0].Executable {
		t.Fatalf("sidecar = %+v", pkg.Sidecar)
	}
	if _, ok := pkg.Artifacts[SidecarFileArtifactKey("darwin-arm64", "libkrunfw")]; !ok {
		t.Fatalf("artifacts = %+v", pkg.Artifacts)
	}
}

func TestParsePackage_keepsTheSandboxGuestProfile(t *testing.T) {
	body := validSandboxSidecarBody(t, func(b map[string]any) {
		b["guest"] = map[string]any{"image": "registry.example/toolbox:1", "cpus": 2, "memoryMiB": 2048}
	})
	pkg, err := ParsePackage(body)
	if err != nil {
		t.Fatalf("parse sandbox package with guest: %v", err)
	}
	if pkg.Guest == nil || pkg.Guest.Image != "registry.example/toolbox:1" || pkg.Guest.CPUs != 2 || pkg.Guest.MemoryMiB != 2048 {
		t.Fatalf("guest = %+v", pkg.Guest)
	}
}

func TestValidatePackage_refusesGuestOnOtherKinds(t *testing.T) {
	pkg, err := ParsePackage(validSandboxSidecarBody(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	pkg.Kind = KindCorePackageV1
	pkg.Guest = &Guest{Image: "alpine:3.20"}
	if err := ValidatePackage(pkg); err == nil || !strings.Contains(err.Error(), "guest is only valid") {
		t.Fatalf("error = %v", err)
	}
}

func TestParsePackage_parses_whenSandboxEndpointPackageIsValid(t *testing.T) {
	if _, err := ParsePackage(validSandboxEndpointBody(t, nil)); err != nil {
		t.Fatalf("parse sandbox endpoint package: %v", err)
	}
}

func TestParsePackage_refuses_invalidSandboxPackages(t *testing.T) {
	cases := map[string]struct {
		body []byte
		want string
	}{
		"missing tokenEnv":      {validSandboxSidecarBody(t, func(b map[string]any) { delete(sidecarField(b), "tokenEnv") }), "requires sidecar.tokenEnv"},
		"missing exec":          {validSandboxSidecarBody(t, func(b map[string]any) { b["capabilities"] = []string{"ports"} }), `must declare the "exec" capability`},
		"unknown capability":    {validSandboxSidecarBody(t, func(b map[string]any) { b["capabilities"] = []string{"exec", "teleport"} }), `unknown sandbox capability "teleport"`},
		"bad contract":          {validSandboxSidecarBody(t, func(b map[string]any) { b["contract"] = "bnbloc.plugin.contract/v1" }), "unknown sandbox contract"},
		"bad protocol major":    {validSandboxSidecarBody(t, func(b map[string]any) { b["protocol"] = map[string]any{"min": "2.0", "max": "2.0"} }), "sandbox protocol version mismatch"},
		"manifest runtime":      {validSandboxSidecarBody(t, func(b map[string]any) { b["runtime"] = "manifest" }), `sandbox runtime must be "sidecar" or "endpoint"`},
		"non-loopback localUrl": {validSandboxSidecarBody(t, func(b map[string]any) { sidecarField(b)["localUrl"] = "http://10.0.0.1:7311" }), "loopback"},
		"no artifacts":          {validSandboxSidecarBody(t, func(b map[string]any) { delete(b, "artifacts"); delete(sidecarField(b), "files") }), `requires artifacts`},
		"endpoint host not in network": {validSandboxEndpointBody(t, func(b map[string]any) {
			b["permissions"] = map[string]any{"network": []string{"elsewhere.example.com"}}
		}), "not in permissions.network"},
		"endpoint without network": {validSandboxEndpointBody(t, func(b map[string]any) { delete(b, "permissions") }), "requires permissions.network"},
		"endpoint with artifacts": {validSandboxEndpointBody(t, func(b map[string]any) {
			b["artifacts"] = map[string]any{"darwin-arm64": sandboxArtifact('a')}
		}), "must not declare artifacts"},
		"declared file missing for platform": {validSandboxSidecarBody(t, func(b map[string]any) {
			delete(b["artifacts"].(map[string]any), "darwin-arm64/msb")
		}), `requires sidecar file "msb"`},
		"undeclared file artifact": {validSandboxSidecarBody(t, func(b map[string]any) {
			b["artifacts"].(map[string]any)["darwin-arm64/extra"] = sandboxArtifact('d')
		}), `"extra" is not a declared sidecar file`},
		"file artifact without main platform": {validSandboxSidecarBody(t, func(b map[string]any) {
			b["artifacts"].(map[string]any)["linux-arm64/msb"] = sandboxArtifact('d')
		}), `platform "linux-arm64" has no main artifact`},
		"env collision": {validSandboxSidecarBody(t, func(b map[string]any) { sidecarField(b)["tokenEnv"] = "SBX_LISTEN" }), "collides with sidecar.listenEnv"},
		"file env collision": {validSandboxSidecarBody(t, func(b map[string]any) {
			sidecarField(b)["files"] = []map[string]any{{"name": "msb", "env": "SBX_TOKEN"}, {"name": "libkrunfw", "env": "SBX_LIBKRUNFW_PATH"}}
		}), "collides with sidecar.tokenEnv"},
		"lowercase env": {validSandboxSidecarBody(t, func(b map[string]any) { sidecarField(b)["dataDirEnv"] = "sbx_data" }), "upper-case identifier"},
		"bad file name": {validSandboxSidecarBody(t, func(b map[string]any) {
			sidecarField(b)["files"] = []map[string]any{{"name": "../msb", "env": "SBX_MSB_PATH"}, {"name": "libkrunfw", "env": "SBX_LIBKRUNFW_PATH"}}
		}), "must use lowercase letters"},
		"guest without image": {validSandboxSidecarBody(t, func(b map[string]any) { b["guest"] = map[string]any{"cpus": 2} }), "guest.image required"},
		"guest host path":     {validSandboxSidecarBody(t, func(b map[string]any) { b["guest"] = map[string]any{"image": "/tmp/rootfs"} }), "OCI image reference"},
		"guest url":           {validSandboxSidecarBody(t, func(b map[string]any) { b["guest"] = map[string]any{"image": "https://x/y"} }), "OCI image reference"},
		"guest negative size": {validSandboxSidecarBody(t, func(b map[string]any) { b["guest"] = map[string]any{"image": "alpine:3.20", "memoryMiB": -1} }), "must not be negative"},
		"duplicate file": {validSandboxSidecarBody(t, func(b map[string]any) {
			sidecarField(b)["files"] = []map[string]any{{"name": "msb", "env": "SBX_A"}, {"name": "msb", "env": "SBX_B"}}
		}), "duplicate name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePackage(tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestParsePackage_refuses_fileArtifactKeysOnKindsWithoutSidecarFiles(t *testing.T) {
	// Given a plugin package with a "<platform>/<name>" key it never declared.
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{"darwin-arm64": sandboxArtifact('a'), "darwin-arm64/helper": sandboxArtifact('b')}
	})

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "not a declared sidecar file") {
		t.Fatalf("error = %v", err)
	}
}

func TestSandboxFileArtifacts_flowThroughSharedArtifactHelpers(t *testing.T) {
	// Given a sandbox package whose files are gateway-uploaded artifacts.
	shaA, shaB, shaC := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	gw := "http://gateway.test"
	body := validSandboxSidecarBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{
			"darwin-arm64":           map[string]any{"url": gw + "/v1/artifacts/sha256/" + shaA, "sha256": shaA},
			"darwin-arm64/msb":       map[string]any{"url": gw + "/v1/artifacts/sha256/" + shaB, "sha256": shaB},
			"darwin-arm64/libkrunfw": map[string]any{"url": "https://cdn.example/x", "sha256": shaC},
		}
	})
	pkg, err := ParsePackage(body)
	if err != nil {
		t.Fatal(err)
	}

	// When / Then: the external file URL is refused like an external executable.
	if err := ValidateGatewayArtifactURLs(pkg, gw); err == nil || !strings.Contains(err.Error(), "artifacts.darwin-arm64/libkrunfw.url") {
		t.Fatalf("gateway URL check = %v", err)
	}

	// And an un-uploaded file placeholder blocks publish like the executable.
	pkg.Artifacts["darwin-arm64/libkrunfw"] = Artifact{}
	if err := ValidateCompleteArtifacts(pkg); err == nil || !strings.Contains(err.Error(), "darwin-arm64/libkrunfw must be uploaded") {
		t.Fatalf("complete check = %v", err)
	}
}

func TestParsePackage_acceptsUploadPlaceholdersForSandboxFiles(t *testing.T) {
	body := validSandboxSidecarBody(t, func(b map[string]any) {
		b["artifacts"].(map[string]any)["darwin-arm64/msb"] = map[string]any{"url": "", "sha256": ""}
	})
	if _, err := ParsePackage(body); err != nil {
		t.Fatalf("parse draft sandbox package: %v", err)
	}
}
