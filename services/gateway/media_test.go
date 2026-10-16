package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// mediaProviderFake is an OpenAI-compatible media provider: it answers a
// transcription from a multipart form and an image from b64_json, recording
// what it was asked.
type mediaProviderFake struct {
	transcript string
	// imageB64 is the reply's inline image (a 1x1 PNG when empty).
	imageB64 string
	// status, when set, answers every request with it and this body.
	status int
	body   string
	// seen paths and forms of the last requests.
	paths []string
	last  map[string]string
}

func (f *mediaProviderFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.paths = append(f.paths, r.URL.Path)
	if f.status != 0 {
		http.Error(w, f.body, f.status)
		return
	}
	switch r.URL.Path {
	case "/v1/audio/transcriptions":
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			tFail(w, "multipart: %v", err)
			return
		}
		f.last = map[string]string{}
		for k, v := range r.Form {
			if len(v) > 0 {
				f.last[k] = v[0]
			}
		}
		if _, header, err := r.FormFile("file"); err == nil {
			f.last["__filename"] = header.Filename
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"text": f.transcript})
	case "/v1/images/generations":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.last = map[string]string{}
		for k, v := range body {
			switch t := v.(type) {
			case string:
				f.last[k] = t
			default:
				raw, _ := json.Marshal(t)
				f.last[k] = string(raw)
			}
		}
		b64 := f.imageB64
		if b64 == "" {
			b64 = base64.StdEncoding.EncodeToString(tinyTestPNG)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": b64, "revised_prompt": "a nicer one"}}})
	default:
		http.NotFound(w, r)
	}
}

func tFail(w http.ResponseWriter, format string, args ...any) {
	http.Error(w, fmt.Sprintf(format, args...), http.StatusBadRequest)
}

// tinyTestPNG is a 1x1 PNG.
var tinyTestPNG = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01,
	0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89}

// mediaGW is the gallery gateway with the secret store key, an audit sink
// and the media providers configured by a super admin (the configuration
// rows drained, so a test sees only its own).
func mediaGW(t *testing.T, fake *mediaProviderFake) *appGW {
	t.Helper()
	e, _, sink := assistGW(t)
	defer func() { sink.take() }()
	model := httptest.NewServer(fake)
	t.Cleanup(model.Close)
	for _, cap := range []string{"transcribe", "image"} {
		r := e.call(http.MethodPut, "/v1/platform/media/"+cap, wire.MediaProviderUpdate{
			Kind: "openai-compatible", BaseURL: model.URL + "/v1", Model: "media-model", APIKey: "sk-media"}, e.superAdmin())
		if r.status != http.StatusOK {
			t.Fatalf("configure %s = %d %s", cap, r.status, r.body)
		}
	}
	return e
}

// A provider is a super admin's: its key is sealed and never returned, a
// member cannot set it, and the capability's view names the kind.
func TestMediaProviderSettings(t *testing.T) {
	e := mediaGW(t, &mediaProviderFake{transcript: "hi"})
	r := e.call(http.MethodGet, "/v1/platform/media/transcribe", nil, e.superAdmin())
	if r.status != http.StatusOK {
		t.Fatalf("get = %d %s", r.status, r.body)
	}
	var view wire.MediaProviderConfig
	if err := json.Unmarshal([]byte(r.body), &view); err != nil {
		t.Fatal(err)
	}
	if !view.KeySet || view.Kind != "openai-compatible" || view.Model != "media-model" || strings.Contains(string(r.body), "sk-media") {
		t.Fatalf("view = %+v %s", view, r.body)
	}
	if r := e.call(http.MethodPut, "/v1/platform/media/transcribe", wire.MediaProviderUpdate{Kind: "openai-compatible", BaseURL: "https://x.example/v1", Model: "m"}, e.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a member cannot set the provider: %d", r.status)
	}
	if r := e.call(http.MethodPut, "/v1/platform/media/nope", wire.MediaProviderUpdate{}, e.superAdmin()); r.status != http.StatusNotFound {
		t.Fatalf("an unknown capability: %d", r.status)
	}
}

// GET /v1/media answers what runs for the caller; the organization's
// assistDisabled switch turns both off.
func TestMediaCapabilities(t *testing.T) {
	e := mediaGW(t, &mediaProviderFake{transcript: "hi"})
	r := e.call(http.MethodGet, "/v1/media", nil, e.as("maya"))
	var caps wire.MediaCapabilities
	if err := json.Unmarshal([]byte(r.body), &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Transcribe.Available || !caps.Image.Available {
		t.Fatalf("caps = %+v", caps)
	}
	off := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	r = e.call(http.MethodGet, "/v1/media", nil, e.as("maya"))
	_ = json.Unmarshal([]byte(r.body), &caps)
	if caps.Transcribe.Available || caps.Image.Available {
		t.Fatalf("an organization with AI off: %+v", caps)
	}
}

// A transcription: the raw audio body in, the text out, the audit row
// holding the model and latency but never the audio or the text.
func TestMediaTranscribe(t *testing.T) {
	fake := &mediaProviderFake{transcript: "hello world"}
	e := mediaGW(t, fake)
	sink := e.gw.ExtAudit.(*recordedAudit)
	r := e.callRaw(http.MethodPost, "/v1/media/transcriptions?filename=clip.wav", "audio/wav", []byte("WAVBYTES"), e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("transcribe = %d %s", r.status, r.body)
	}
	var reply wire.MediaTranscribeReply
	if err := json.Unmarshal([]byte(r.body), &reply); err != nil || reply.Text != "hello world" {
		t.Fatalf("reply = %s %v", r.body, err)
	}
	if fake.last["model"] != "media-model" || fake.last["__filename"] != "clip.wav" {
		t.Fatalf("form = %v", fake.last)
	}
	rows := sink.take()
	if len(rows) != 1 || rows[0].Name != "transcribe" || rows[0].Kind != wire.AuditKindAssist {
		t.Fatalf("audit = %+v", rows)
	}
	if strings.Contains(rows[0].Detail, "WAVBYTES") || strings.Contains(rows[0].Detail, "hello world") {
		t.Fatalf("the audit row leaks content: %s", rows[0].Detail)
	}
}

// A generation: the prompt in, the inline image out, the refusal's status
// kept, the audit row without the prompt or the image.
func TestMediaImage(t *testing.T) {
	fake := &mediaProviderFake{}
	e := mediaGW(t, fake)
	sink := e.gw.ExtAudit.(*recordedAudit)
	r := e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "a poster", Aspect: "landscape"}, e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("image = %d %s", r.status, r.body)
	}
	var reply wire.MediaImageReply
	if err := json.Unmarshal([]byte(r.body), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply.DataURL, "data:image/png;base64,") || reply.MimeType != "image/png" || reply.Width != 1 || reply.Height != 1 {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.RevisedPrompt != "a nicer one" {
		t.Fatalf("revised = %q", reply.RevisedPrompt)
	}
	if fake.last["size"] != "1536x1024" {
		t.Fatalf("size = %v", fake.last)
	}
	rows := sink.take()
	if len(rows) != 1 || rows[0].Name != "image" {
		t.Fatalf("audit = %+v", rows)
	}
	if strings.Contains(rows[0].Detail, "a poster") || strings.Contains(rows[0].Detail, reply.DataURL[:40]) {
		t.Fatalf("the audit row leaks content: %s", rows[0].Detail)
	}
	// A refusal keeps the provider's own status.
	fake.status, fake.body = http.StatusUnavailableForLegalReasons, `{"error":{"message":"content policy"}}`
	r = e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "nope"}, e.as("maya"))
	if r.status != http.StatusUnavailableForLegalReasons || !strings.Contains(string(r.body), "content policy") {
		t.Fatalf("refusal = %d %s", r.status, r.body)
	}
	fake.status, fake.body = 0, ""
}

// The bounds: no provider a 409 sentence, an over-cap audio a 413, an
// empty prompt a 400, an unknown aspect a 400.
func TestMediaBounds(t *testing.T) {
	e := mediaGW(t, &mediaProviderFake{transcript: "hi"})
	sink := e.gw.ExtAudit.(*recordedAudit)
	r := e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "x", Aspect: "diagonal"}, e.as("maya"))
	if r.status != http.StatusBadRequest {
		t.Fatalf("aspect = %d", r.status)
	}
	r = e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: ""}, e.as("maya"))
	if r.status != http.StatusBadRequest {
		t.Fatalf("empty prompt = %d", r.status)
	}
	r = e.callRaw(http.MethodPost, "/v1/media/transcriptions", "audio/wav", []byte(strings.Repeat("a", int(wire.MaxMediaAudioBytes)+1)), e.as("maya"))
	if r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("over cap = %d", r.status)
	}
	sink.take()
}

// callRaw sends a raw body with a content type and the member's auth.
func (e *appGW) callRaw(method, path, contentType string, body []byte, header string) resp {
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(string(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", strings.TrimPrefix(header, "Authorization: "))
	rec := httptest.NewRecorder()
	e.gw.Handler().ServeHTTP(rec, req)
	return resp{status: rec.Code, body: rec.Body.Bytes()}
}

// A page's ai.image call: the app's prompt to the organization's image
// provider, audited with the app's id, the image inline; a visitor and an
// organization with AI off refused; ai.voice turns audio into text.
func TestAppAIMedia(t *testing.T) {
	fake := &mediaProviderFake{}
	e := mediaGW(t, fake)
	sink := e.gw.ExtAudit.(*recordedAudit)
	jack := e.ticketBase("jack", e.appA)
	r := e.call(http.MethodPost, jack+"~app/ai/image", wire.MediaImageRequest{Prompt: "a poster", Aspect: "square"})
	if r.status != http.StatusOK {
		t.Fatalf("ai.image = %d %s", r.status, r.body)
	}
	var reply wire.MediaImageReply
	if err := json.Unmarshal(r.body, &reply); err != nil || !strings.HasPrefix(reply.DataURL, "data:image/png;base64,") {
		t.Fatalf("reply = %s %v", r.body, err)
	}
	rows := sink.take()
	if len(rows) != 1 || rows[0].Name != "app:"+e.appA+":image" || rows[0].Actor != "jack" {
		t.Fatalf("audit = %+v", rows)
	}
	if strings.Contains(rows[0].Detail, "a poster") {
		t.Fatalf("the audit row leaks the prompt: %s", rows[0].Detail)
	}
	if fake.last["size"] != "1024x1024" {
		t.Fatalf("size = %v", fake.last)
	}

	fake.transcript = "hello from the app"
	r = e.callRaw(http.MethodPost, jack+"~app/ai/voice?filename=clip.wav", "audio/wav", []byte("WAVBYTES"), "Authorization: Bearer "+e.session("jack"))
	var voice wire.MediaTranscribeReply
	if err := json.Unmarshal(r.body, &voice); err != nil || voice.Text != "hello from the app" {
		t.Fatalf("voice = %s %v", r.body, err)
	}
	rows = sink.take()
	if len(rows) != 1 || rows[0].Name != "app:"+e.appA+":voice" {
		t.Fatalf("voice audit = %+v", rows)
	}

	tok, _ := e.shareWith(e.appA, "anyone")
	vis := visitorHeader(e.visitor(tok, ""))
	if r := e.call(http.MethodPost, shareBase(tok)+"~app/ai/image", wire.MediaImageRequest{Prompt: "p"}, vis); r.status != http.StatusForbidden || r.json()["code"] != wire.AppSignInRequired {
		t.Fatalf("a visitor's image = %d %s", r.status, r.body)
	}
	off := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodPost, jack+"~app/ai/image", wire.MediaImageRequest{Prompt: "p"}); r.status != http.StatusConflict {
		t.Fatalf("image with AI off = %d %s", r.status, r.body)
	}
}
