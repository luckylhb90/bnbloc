package roomtools

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

const transcriptReply = `{"text":"let's ship the retro poster by Friday"}`

// audioEnv is a bound env with a workspace holding one recording, whose
// gateway answers one transcript.
func audioEnv(t *testing.T) (*Env, *recordingGateway) {
	t.Helper()
	ws := fakeWorkspace{"notes/standup.wav": "WAVBYTES"}
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(transcriptReply), nil }}
	env := contentEnv(ws, gw, true)
	return env, gw
}

// transcribe_audio reads the workspace file and posts its bytes to the
// media route as the job's requester; the transcript comes back fenced as
// data, and nothing is published.
func TestTranscribeAudio(t *testing.T) {
	env, gw := audioEnv(t)
	res := transcribeAudio(context.Background(), env, json.RawMessage(`{"path":"notes/standup.wav"}`))
	if res.Err != "" {
		t.Fatalf("transcribe = %s", res.Err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("calls = %+v", gw.calls)
	}
	c := gw.calls[0]
	u, _ := url.Parse(c.Path)
	if u.Path != "/v1/media/transcriptions" || u.Query().Get("job") != "job-1" || u.Query().Get("room") != "room-1" || u.Query().Get("filename") != "standup.wav" {
		t.Fatalf("call = %s", c.Path)
	}
	body, ok := c.Body.(RawBody)
	if !ok || body.ContentType != "audio/wav" || string(body.Data) != "WAVBYTES" {
		t.Fatalf("body = %T %+v", c.Body, c.Body)
	}
	out := res.Value.(map[string]any)
	if out["path"] != "notes/standup.wav" {
		t.Fatalf("result = %+v", out)
	}
	if transcript, _ := out["transcript"].(string); !strings.Contains(transcript, roomlog.Fence(transcriptAuthor, "let's ship the retro poster by Friday")) {
		t.Fatalf("transcript = %q", transcript)
	}
}

// Refusals: a missing path, a file that is not there, a non-audio file, no
// workspace at all, and a provider failure.
func TestTranscribeAudioRefusals(t *testing.T) {
	env, _ := audioEnv(t)
	if res := transcribeAudio(context.Background(), env, json.RawMessage(`{}`)); res.Err == "" {
		t.Fatal("a missing path must be refused")
	}
	if res := transcribeAudio(context.Background(), env, json.RawMessage(`{"path":"notes/gone.wav"}`)); res.Err == "" || !strings.Contains(res.Err, "reading notes/gone.wav failed") {
		t.Fatalf("missing file = %q", res.Err)
	}
	textWs := fakeWorkspace{"notes/readme.txt": "TEXT"}
	gw2 := &recordingGateway{reply: func(string, string) ([]byte, error) { return []byte(transcriptReply), nil }}
	textEnv := contentEnv(textWs, gw2, true)
	if res := transcribeAudio(context.Background(), textEnv, json.RawMessage(`{"path":"notes/readme.txt"}`)); res.Err == "" || !strings.Contains(res.Err, "not an audio file") {
		t.Fatalf("text file = %q", res.Err)
	}
	noWs := contentEnv(nil, gw2, true)
	if res := transcribeAudio(context.Background(), noWs, json.RawMessage(`{"path":"x.wav"}`)); res.Err == "" || !strings.Contains(res.Err, "does not expose") {
		t.Fatalf("no workspace = %q", res.Err)
	}
	fail := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return nil, &StatusError{Status: 502, Body: []byte(`{"code":"","error":"the media provider could not be reached"}`)}
	}}
	failing := contentEnv(fakeWorkspace{"notes/standup.wav": "WAVBYTES"}, fail, true)
	if res := transcribeAudio(context.Background(), failing, json.RawMessage(`{"path":"notes/standup.wav"}`)); res.Err == "" || !strings.Contains(res.Err, "could not be reached") {
		t.Fatalf("provider failure = %q", res.Err)
	}
}
