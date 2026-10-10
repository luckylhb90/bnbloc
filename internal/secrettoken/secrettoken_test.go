package secrettoken

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	f := Format{Prefix: "bsh_", IDLen: 32}
	id, token, hash, err := f.New()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 || !strings.HasPrefix(token, "bsh_"+id+"_") || len(hash) != 64 {
		t.Fatalf("id=%q token=%q hash=%q", id, token, hash)
	}
	gotID, secret, ok := f.Parse(token)
	if !ok || gotID != id || !Verify(secret, hash) {
		t.Fatalf("parse/verify failed: %v %q", ok, gotID)
	}
	if Verify(secret+"x", hash) || Verify(secret, strings.Repeat("0", 64)) {
		t.Fatal("a wrong secret or hash verified")
	}
	if strings.Contains(hash, secret) {
		t.Fatal("hash leaks the secret")
	}
}

func TestParseRejects(t *testing.T) {
	f := Format{Prefix: "bsh_", IDLen: 32}
	hexID := strings.Repeat("a", 32)
	for _, raw := range []string{
		"",
		"bsh_",
		"bnbloc_at_" + hexID + "_s",
		"bsh_" + hexID,
		"bsh_" + hexID + "_",
		"bsh_" + strings.Repeat("z", 32) + "_s",
		"bsh_" + hexID + "xs",
	} {
		if _, _, ok := f.Parse(raw); ok {
			t.Errorf("Parse(%q) accepted", raw)
		}
	}
	if _, _, ok := f.Parse("  bsh_" + hexID + "_secret  "); !ok {
		t.Error("surrounding whitespace should be tolerated")
	}
}
