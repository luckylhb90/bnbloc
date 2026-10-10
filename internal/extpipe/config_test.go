package extpipe

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// One projection serves every kind: a package survives encode → record →
// decode → Package() with its payload intact (BR-21).
func TestPackageConfig_roundTripsEveryKindPayload(t *testing.T) {
	pkg := pkgmeta.Package{
		Kind:         pkgmeta.KindPluginPackageV1,
		Name:         "auditor",
		Version:      "1.2.3",
		Protocol:     protocol.ProtocolRange{Min: "1.0", Max: "1.0"},
		Runtime:      pkgmeta.RuntimeSidecar,
		Endpoint:     &pkgmeta.Endpoint{BaseURL: "http://127.0.0.1:1"},
		Sidecar:      &pkgmeta.Sidecar{LocalURL: "http://127.0.0.1:7191", ListenEnv: "L", SupervisedEnv: "S"},
		Artifacts:    map[string]pkgmeta.Artifact{"darwin-arm64": {URL: "https://gw/v1/artifacts/sha256/x", SHA256: "x"}},
		Capabilities: []string{"interrupt"},
		Permissions:  pkgmeta.Permissions{Network: []string{"127.0.0.1"}},
		Publisher:    "Acme",
		Description:  "d",
		Contract:     pkgmeta.PluginContractV1,
		Hooks:        []string{"chat_message.v1"},
	}
	raw, err := EncodePackageConfig(pkg)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{Kind: "plugin", Name: pkg.Name, Version: pkg.Version, Publisher: pkg.Publisher, Config: raw, Network: pkg.Permissions.Network}
	cfg, err := DecodeConfig(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Package(pkg.Kind, rec); !reflect.DeepEqual(got, pkg) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, pkg)
	}
}

func TestPackageConfig_permissionsFallBackToRecordNetwork(t *testing.T) {
	raw, _ := json.Marshal(PackageConfig{Runtime: pkgmeta.RuntimeEndpoint})
	rec := Record{Name: "x", Config: raw, Network: []string{"idp.example"}}
	cfg, err := DecodeConfig(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EffectivePermissions(rec).Network; len(got) != 1 || got[0] != "idp.example" {
		t.Fatalf("effective network = %v; want the record's", got)
	}
	if cfg, err := DecodeConfig(Record{}); err != nil || cfg.Runtime != "" {
		t.Fatalf("empty config = %+v, %v; want zero value", cfg, err)
	}
}
