package roomctx

import (
	"strings"
	"testing"
)

func TestValidateID(t *testing.T) {
	ok := []string{"room-1", "chat-team", "events2026", "产品讨论", "my room", strings.Repeat("a", MaxIDBytes)}
	for _, id := range ok {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want ok", id, err)
		}
	}
	bad := []string{"", " lead", "trail ", "a/b", `a\b`, "a?b", "a#b", "a%2Fb", "a\x00b", "a\nb", strings.Repeat("a", MaxIDBytes+1), "\xff"}
	for _, id := range bad {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}
