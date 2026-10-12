package wire

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEventPayloadsDecodedOnce: an event payload is read through
// DecodePayload, never by switching on the shape it happens to arrive in
// (the typed value in-process, a map after the ingest): the copies handled
// one shape each and silently dropped the other (2026-10-12 review).
func TestEventPayloadsDecodedOnce(t *testing.T) {
	re := regexp.MustCompile(`\bev\.Payload\.\(`)
	root := filepath.Join("..", "..")
	for _, dir := range []string{"services", "internal", "core", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if strings.HasPrefix(filepath.ToSlash(path), "../../internal/wire/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if re.Match(raw) {
				t.Errorf("%s type-asserts an event payload; use wire.DecodePayload", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
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
