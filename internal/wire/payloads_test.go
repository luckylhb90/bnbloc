package wire

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestEventPayloadsDecodedOnce: an event payload is read through
// DecodePayload, never by switching on the shape it happens to arrive in
// (the typed value in-process, a map after the ingest): the copies handled
// one shape each and silently dropped the other (2026-10-12 review).
func TestEventPayloadsDecodedOnce(t *testing.T) {
	re := regexp.MustCompile(`\bev\.Payload\.\(`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "core", "cmd"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/wire/") {
			return
		}
		if re.Match(src) {
			t.Errorf("%s type-asserts an event payload; use wire.DecodePayload", rel)
		}
	})
}

// DecodePayload reads a payload in every shape it travels in: typed
// in-process, a decoded JSON map after the internal ingest, raw JSON from
// history.
func TestDecodePayloadEveryShape(t *testing.T) {
	typed := MemberRemovedPayload{UserID: "maya"}
	raw, _ := json.Marshal(typed)
	var asMap map[string]any
	_ = json.Unmarshal(raw, &asMap)
	for name, in := range map[string]any{
		"typed": typed, "pointer": &typed, "map": asMap, "raw": json.RawMessage(raw), "bytes": raw,
	} {
		got, ok := DecodePayload[MemberRemovedPayload](in)
		if !ok || got.UserID != "maya" {
			t.Errorf("%s: %+v %v", name, got, ok)
		}
	}
	if _, ok := DecodePayload[MemberRemovedPayload](nil); ok {
		t.Error("nil decoded")
	}
	if _, ok := DecodePayload[MemberRemovedPayload]("not an object"); ok {
		t.Error("a string decoded as an object")
	}
	batch := ReportMessagesInput{AgentSessionID: "s1", Messages: []Message{{Seq: 1, Payload: map[string]any{"text": "hi", "notice": NoticeProgress}}}}
	p, ok := DecodePayload[AgentMessagesPayload](batch)
	if !ok || p.AgentSessionID != "s1" || len(p.Messages) != 1 || p.Messages[0].Payload.Text != "hi" || p.Messages[0].Payload.Notice != NoticeProgress {
		t.Fatalf("agent batch = %+v %v", p, ok)
	}
}

// A chat payload's author and text read the same from the published map,
// the typed payload and its JSON.
func TestChatAuthorText(t *testing.T) {
	for _, p := range []any{
		map[string]any{"userId": "maya", "text": "hi @jack"},
		ChatMessagePayload{UserID: "maya", Text: "hi @jack"},
		json.RawMessage(`{"userId":"maya","text":"hi @jack"}`),
	} {
		if a, txt, ok := ChatAuthorText(p); !ok || a != "maya" || txt != "hi @jack" {
			t.Errorf("%T: %q %q %v", p, a, txt, ok)
		}
	}
}
