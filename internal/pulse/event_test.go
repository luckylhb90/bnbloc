package pulse

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const goodHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func claimed() Event {
	return Event{ID: "j1:claimed", Tenant: "t1", Kind: KindJobClaimed, At: 1}
}

func TestKnownAttrsKeptUnknownDropped(t *testing.T) {
	ev := claimed().With(Enum("core", "codex")).With(Enum("phase", "claimed")).With(Enum("text", "hello"))
	out, dropped, r := check(ev)
	if r != reasonOK || dropped != 1 {
		t.Fatalf("check = (%v, dropped %d), want ok with one attribute dropped", r, dropped)
	}
	if n := len(out.Attrs()); n != 2 {
		t.Fatalf("kept %d attributes, want 2: %+v", n, out.Attrs())
	}
}

func TestRefusesUnknownKindTenantAndID(t *testing.T) {
	cases := map[string]struct {
		ev   Event
		want reason
	}{
		"unknown kind":   {Event{ID: "x", Tenant: "t", Kind: "job.teleported"}, reasonKind},
		"no tenant":      {Event{ID: "x", Kind: KindJobClaimed, At: 1}.With(Enum("core", "codex")), reasonTenant},
		"tenant too big": {Event{ID: "x", Tenant: strings.Repeat("t", maxTenantLen+1), Kind: KindJobClaimed, At: 1}, reasonTenant},
		"no id":          {Event{Tenant: "t", Kind: KindJobClaimed, At: 1}, reasonID},
	}
	for name, c := range cases {
		if _, _, r := check(c.ev); r != c.want {
			t.Errorf("%s: reason = %v, want %v", name, r, c.want)
		}
	}
}

func TestMissingRequiredAttributeRefusesTheEvent(t *testing.T) {
	// job.finished needs core, status and reported. An out-of-set core is
	// dropped, which leaves it missing, so the event is refused, not guessed.
	ev := Event{ID: "j:f", Tenant: "t", Kind: KindJobFinished, At: 1}.
		With(Enum("core", "gpt-9000")).With(Enum("status", "replied"))
	if _, _, r := check(ev); r != reasonRequired {
		t.Fatalf("reason = %v, want reasonRequired", r)
	}
}

func TestAttributeShapeMustMatch(t *testing.T) {
	// A string where a number belongs is dropped, never coerced.
	ev := Event{ID: "j:f", Tenant: "t", Kind: KindJobFinished, At: 1}.
		With(Enum("core", "codex")).With(Enum("status", "replied")).With(Enum("durationMs", "12"))
	out, dropped, r := check(ev)
	if r != reasonOK || dropped != 1 || len(out.Attrs()) != 2 {
		t.Fatalf("got (%v, dropped %d, kept %d)", r, dropped, len(out.Attrs()))
	}
}

func TestMemberMustBeAHashNeverAnId(t *testing.T) {
	bad := []string{"alice@example.com", "user-42", strings.ToUpper(goodHash), goodHash[:63], goodHash + "0"}
	for _, v := range bad {
		ev := Event{ID: "p", Tenant: "t", Kind: KindPresenceLeft, At: 1}.With(Hash("member", v))
		if _, _, r := check(ev); r != reasonRequired {
			t.Errorf("member %q accepted (reason %v); a person may only appear hashed", v, r)
		}
	}
	ok := Event{ID: "p", Tenant: "t", Kind: KindPresenceLeft, At: 1}.With(Hash("member", goodHash))
	if _, _, r := check(ok); r != reasonOK {
		t.Fatalf("good hash refused: %v", r)
	}
}

func TestZoneFormats(t *testing.T) {
	cases := map[string]bool{
		"Europe/Berlin": true, "America/Argentina/Buenos_Aires": true, "UTC": true, "Etc/GMT+8": true,
		"": false, "/Europe": false, "Europe/": false, "Europe//Berlin": false, "Europe Berlin": false,
		"../etc/passwd": false, strings.Repeat("a", 41): false,
	}
	for z, want := range cases {
		if got := validZone(z); got != want {
			t.Errorf("validZone(%q) = %v, want %v", z, got, want)
		}
	}
}

func TestIdentFormats(t *testing.T) {
	cases := map[string]bool{"doc": true, "slide-deck.v2": true, "a_b": true,
		"": false, "1doc": false, "Doc": false, "has space": false, strings.Repeat("a", 33): false}
	for s, want := range cases {
		if got := validIdent(s); got != want {
			t.Errorf("validIdent(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestNumRejectsNegativeAndNonFinite(t *testing.T) {
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		ev := Event{ID: "r:1", Tenant: "t", Kind: KindReplyUsage, At: 1}.
			With(Enum("core", "codex")).With(Flag("reported", true)).With(Num("costUsd", v))
		if out, dropped, r := check(ev); r != reasonOK || dropped != 1 || len(out.Attrs()) != 2 {
			t.Errorf("num %v: got (%v, dropped %d)", v, r, dropped)
		}
	}
}

func TestTooManyAttributesRefusesTheEvent(t *testing.T) {
	ev := claimed()
	for i := 0; i < maxAttrs+1; i++ {
		ev = ev.With(Enum("core", "codex"))
	}
	if _, _, r := check(ev); r != reasonAttrCount {
		t.Fatalf("reason = %v, want reasonAttrCount", r)
	}
}

func TestEventJSONIsTheEnvelope(t *testing.T) {
	ev := Event{ID: "reply:r1:s1:7", Tenant: "t1", Kind: KindReplyUsage, At: 1761300000000}.
		With(Enum("core", "claude-code")).With(Flag("reported", true)).With(Num("tokensIn", 1200))
	out, _, r := check(ev)
	if r != reasonOK {
		t.Fatal(r)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"id":"reply:r1:s1:7","tenant":"t1","kind":"reply.usage","at":1761300000000,` +
		`"attrs":{"core":"claude-code","reported":true,"tokensIn":1200}}`
	if string(b) != want {
		t.Fatalf("json = %s\nwant   %s", b, want)
	}
}

func TestStampsEmitTimeWhenAtIsZero(t *testing.T) {
	ev := Event{ID: "j:c", Tenant: "t", Kind: KindJobClaimed}.With(Enum("core", "acp"))
	out, _, r := check(ev)
	if r != reasonOK || out.At < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("At not stamped: %d (%v)", out.At, r)
	}
}
