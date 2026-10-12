package sealbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTripAndBinding(t *testing.T) {
	b := testBox(t)
	sealed, err := b.Seal([]byte("s3cr3t"), "row-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "s3cr3t") || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("sealed = %q", sealed)
	}
	got, err := b.Open(sealed, "row-a")
	if err != nil || !bytes.Equal(got, []byte("s3cr3t")) {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := b.Open(sealed, "row-b"); !errors.Is(err, ErrOpen) {
		t.Fatalf("a value moved to another row must not open: %v", err)
	}
	if _, err := testBox(t).Open(sealed, "row-a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("another key must not open it: %v", err)
	}
	// Flip one bit of the authentication tag: a textual edit of the encoding
	// (say, replacing the last two characters) is a no-op whenever the
	// value already ends that way.
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01
	tampered := "v1:" + base64.RawStdEncoding.EncodeToString(raw)
	if tampered == sealed {
		t.Fatal("the tampered value equals the sealed one")
	}
	if _, err := b.Open(tampered, "row-a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("tampered value must not open: %v", err)
	}
	again, _ := b.Seal([]byte("s3cr3t"), "row-a")
	if again == sealed {
		t.Fatal("every seal uses a fresh nonce")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if b, err := FromEnv(); b != nil || err != nil {
		t.Fatalf("unset key = %v, %v; want off", b, err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	for _, raw := range []string{base64.StdEncoding.EncodeToString(key), hex.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key)} {
		t.Setenv(KeyEnv, raw)
		if b, err := FromEnv(); b == nil || err != nil {
			t.Fatalf("FromEnv(%q) = %v, %v", raw, b, err)
		}
	}
	t.Setenv(KeyEnv, "short")
	if _, err := FromEnv(); err == nil {
		t.Fatal("a malformed key must fail")
	}
}
