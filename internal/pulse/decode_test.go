package pulse

import (
	"encoding/json"
	"errors"
	"testing"
)

// Every kind's valid event, built the way the product builds it.
func validEvents() []Event {
	h := Digest("job-1")
	m := Digest("member-1")
	return []Event{
		Event{ID: "j:c", Tenant: "t1", Kind: KindJobClaimed, At: 1761300000000}.With(Enum("core", "codex")).With(Enum("phase", "claimed")).With(Hash("job", h)),
		Event{ID: "j:f", Tenant: "t1", Kind: KindJobFinished, At: 1761300000000}.With(Enum("core", "codex")).With(Enum("status", "failed")).With(Enum("failureReason", "runner_failed")).With(Num("durationMs", 750)).With(Hash("job", h)),
		Event{ID: "j:s", Tenant: "t1", Kind: KindJobSwept, At: 1761300000000}.With(Enum("core", "codex")).With(Enum("reason", "lease_expired")).With(Hash("job", h)),
		Event{ID: "r:1", Tenant: "t1", Kind: KindReplyUsage, At: 1761300000000}.With(Enum("core", "claude-code")).With(Flag("reported", true)).With(Num("tokensIn", 1200)).With(Num("tokensOut", 80)).With(Num("costUsd", 0.02)),
		Event{ID: "m:1", Tenant: "t1", Kind: KindMessageSent, At: 1761300000000}.With(Enum("channelType", "agent")).With(Hash("member", m)),
		Event{ID: "a:1", Tenant: "t1", Kind: KindArtifactVersion, At: 1761300000000}.With(Ident("kind", "deck")).With(Flag("isNew", true)),
		Event{ID: "p:1", Tenant: "t1", Kind: KindAppWritten, At: 1761300000000}.With(Flag("public", false)),
		Event{ID: "p:2", Tenant: "t1", Kind: KindPresenceSeen, At: 1761300000000}.With(Hash("member", m)),
		Event{ID: "p:3", Tenant: "t1", Kind: KindPresenceLeft, At: 1761300000000}.With(Hash("member", m)).With(Ident("country", "de")),
		Event{ID: "d:1", Tenant: "t1", Kind: KindMemberJoined, At: 1761300000000}.With(Enum("source", "directory")),
	}
}

// What the consumer reads back is exactly what the product wrote.
func TestDecodeRoundTripsEveryKind(t *testing.T) {
	for _, ev := range validEvents() {
		want, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(want)
		if err != nil {
			t.Fatalf("%s: decode: %v", ev.Kind, err)
		}
		back, _ := json.Marshal(got)
		if string(back) != string(want) {
			t.Fatalf("%s: round trip changed the event\n got %s\nwant %s", ev.Kind, back, want)
		}
	}
}

func TestDecodeRefusesWhatEmitRefuses(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want error
	}{
		"version 2":        {`{"v":2,"id":"x","tenant":"t","kind":"job.claimed","at":1,"attrs":{"core":"codex"}}`, ErrVersion},
		"unknown kind":     {`{"v":1,"id":"x","tenant":"t","kind":"job.teleported","at":1,"attrs":{}}`, ErrUnknownKind},
		"no tenant":        {`{"v":1,"id":"x","tenant":"","kind":"job.claimed","at":1,"attrs":{"core":"codex"}}`, ErrRefused{Reason: "tenant"}},
		"required missing": {`{"v":1,"id":"x","tenant":"t","kind":"job.finished","at":1,"attrs":{"core":"codex"}}`, ErrRefused{Reason: "required attribute missing"}},
		"malformed":        {`{"v":1,"id":`, nil},
	}
	for name, c := range cases {
		_, err := Decode([]byte(c.raw))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) && err.Error() != c.want.Error() {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

// Unknown or ill-shaped attributes are dropped, as Emit drops them.
func TestDecodeDropsUnknownAndIllShapedAttributes(t *testing.T) {
	raw := `{"v":1,"id":"x","tenant":"t","kind":"job.finished","at":1,"attrs":{"core":"codex","status":"replied","text":"hello","durationMs":"12"}}`
	ev, err := Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ev.Attrs()); n != 2 {
		t.Fatalf("kept %d attributes, want 2 (core, status)", n)
	}
}

func TestDigestIsStableAndOneWay(t *testing.T) {
	a, b := Digest("job-1"), Digest("job-1")
	if a != b || !isHash(a) || a == "job-1" || Digest("job-2") == a {
		t.Fatalf("digest: %q %q", a, b)
	}
}
