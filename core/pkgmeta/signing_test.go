package pkgmeta

import (
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/core/protocol"
)

func TestSignPackageVerifyRoundTrip(t *testing.T) {
	pubMinisign, keyID, priv, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	pkg := Package{
		Kind:      KindCorePackageV1,
		Name:      "acme-agent",
		Version:   "0.1.0",
		Runtime:   RuntimeEndpoint,
		Endpoint:  &Endpoint{BaseURL: "https://agent.acme.test"},
		Protocol:  protocol.ProtocolRange{Min: "1.0", Max: "1.0"},
		Publisher: "acme",
	}
	body, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sig, err := SignPackage(body, priv)
	if err != nil {
		t.Fatalf("SignPackage: %v", err)
	}
	trusted := []TrustedKey{{KeyID: keyID, PublicKey: pubMinisign}}
	v, err := VerifySignedPackage(body, sig, trusted)
	if err != nil {
		t.Fatalf("VerifySignedPackage: %v", err)
	}
	if v.Package.Name != "acme-agent" || v.KeyID != keyID {
		t.Fatalf("verified = %+v keyID=%s; want acme-agent + %s", v.Package, v.KeyID, keyID)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, _, priv, _ := GenerateSigningKey()
	otherPub, otherID, _, _ := GenerateSigningKey()
	pkg := Package{Kind: KindCorePackageV1, Name: "x", Version: "0.1.0", Runtime: RuntimeEndpoint, Endpoint: &Endpoint{BaseURL: "https://x.test"}, Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}, Publisher: "p"}
	body, _ := json.Marshal(pkg)
	sig, _ := SignPackage(body, priv) // signed by priv, but only otherPub is trusted
	trusted := []TrustedKey{{KeyID: otherID, PublicKey: otherPub}}
	if _, err := VerifySignedPackage(body, sig, trusted); err == nil {
		t.Fatal("verification must fail when the signing key is not trusted")
	}
}

func TestEncodeDecodePublicKeyRoundTrip(t *testing.T) {
	pubMinisign, keyID, _, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	gotID, pub, err := DecodeMinisignPublicKey(pubMinisign)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotID != keyID || len(pub) != 32 {
		t.Fatalf("decoded id=%s (want %s), pub len=%d", gotID, keyID, len(pub))
	}
}
