package pulse

import (
	"errors"
	"testing"
)

func TestMemberHashIsKeyedAndStable(t *testing.T) {
	key := []byte(strings32())
	a, err := MemberHash(key, "alice")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MemberHash(key, "alice")
	if a != b || !isHash(a) {
		t.Fatalf("hash not stable or not a digest: %q %q", a, b)
	}
	other, _ := MemberHash([]byte(strings32()+"x"), "alice")
	if other == a {
		t.Fatal("a different key must give a different digest")
	}
	if c, _ := MemberHash(key, "bob"); c == a {
		t.Fatal("different users must differ")
	}
}

func TestMemberHashRefusesShortKey(t *testing.T) {
	if _, err := MemberHash([]byte("short"), "alice"); !errors.Is(err, ErrShortKey) {
		t.Fatalf("err = %v, want ErrShortKey", err)
	}
}

func strings32() string { return "0123456789abcdef0123456789abcdef" }
