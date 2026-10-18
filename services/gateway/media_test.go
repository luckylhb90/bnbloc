package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/media"
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
	case "/v1/images/edits":
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			tFail(w, "multipart: %v", err)
			return
		}
		f.last = map[string]string{"prompt": r.FormValue("prompt"), "model": r.FormValue("model")}
		if file, _, err := r.FormFile("image"); err == nil {
			b, _ := io.ReadAll(file)
			f.last["__image"] = fmt.Sprintf("%d bytes", len(b))
			_ = file.Close()
		}
		f.last["__images"] = fmt.Sprint(len(r.MultipartForm.File["image[]"]))
		if file, _, err := r.FormFile("mask"); err == nil {
			b, _ := io.ReadAll(file)
			f.last["__mask"] = fmt.Sprintf("%d bytes", len(b))
			_ = file.Close()
		}
		b64 := f.imageB64
		if b64 == "" {
			b64 = base64.StdEncoding.EncodeToString(tinyTestPNG)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": b64, "revised_prompt": "a nicer one"}}})
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
		count := 1
		if n, ok := body["n"].(float64); ok && int(n) > 0 {
			count = int(n)
		}
		b64 := f.imageB64
		if b64 == "" {
			b64 = base64.StdEncoding.EncodeToString(tinyTestPNG)
		}
		data := make([]any, 0, count)
		for i := 0; i < count; i++ {
			data = append(data, map[string]any{"b64_json": b64, "revised_prompt": "a nicer one"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
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
	if err := json.Unmarshal([]byte(r.body), &reply); err != nil || len(reply.Images) != 1 {
		t.Fatalf("reply = %s %v", r.body, err)
	}
	img := reply.Images[0]
	if !strings.HasPrefix(img.DataURL, "data:image/png;base64,") || img.MimeType != "image/png" || img.Width != 1 || img.Height != 1 {
		t.Fatalf("image = %+v", img)
	}
	if img.RevisedPrompt != "a nicer one" {
		t.Fatalf("revised = %q", img.RevisedPrompt)
	}
	if fake.last["size"] != "1536x1024" {
		t.Fatalf("size = %v", fake.last)
	}
	rows := sink.take()
	if len(rows) != 1 || rows[0].Name != "image" {
		t.Fatalf("audit = %+v", rows)
	}
	if strings.Contains(rows[0].Detail, "a poster") || strings.Contains(rows[0].Detail, img.DataURL[:40]) {
		t.Fatalf("the audit row leaks content: %s", rows[0].Detail)
	}
	// A batch: three images in one provider call, one audit row.
	fake.last["n"] = ""
	sink.take()
	r = e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "three poster options", Count: 3}, e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("batch = %d %s", r.status, r.body)
	}
	var batch wire.MediaImageReply
	if err := json.Unmarshal(r.body, &batch); err != nil || len(batch.Images) != 3 {
		t.Fatalf("batch = %s %v", r.body, err)
	}
	if rows := sink.take(); len(rows) != 1 {
		t.Fatalf("batch audit = %+v", rows)
	}

	// An edit: the input image rides the request (a data URL), the provider
	// gets /images/edits.
	sink.take()
	r = e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "a red hat on it", Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyTestPNG)}, e.as("maya"))
	if r.status != http.StatusOK {
		t.Fatalf("edit = %d %s", r.status, r.body)
	}
	if rows := sink.take(); len(rows) != 1 {
		t.Fatalf("edit audit = %+v", rows)
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
	if err := json.Unmarshal(r.body, &reply); err != nil || len(reply.Images) != 1 || !strings.HasPrefix(reply.Images[0].DataURL, "data:image/png;base64,") {
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

	// A room app publishes: the picture is a room file the participant
	// authored (no job source), and the page may edit it as a version.
	r = e.call(http.MethodPost, jack+"~app/ai/image", wire.MediaImageRequest{Prompt: "a sticker", Publish: true, FileName: "sticker.png"})
	var pub wire.MediaImageReply
	if err := json.Unmarshal(r.body, &pub); err != nil || r.status != http.StatusOK || len(pub.Images) != 1 || pub.Images[0].ArtifactID == "" || pub.Images[0].DataURL != "" {
		t.Fatalf("app publish = %d %s", r.status, r.body)
	}
	sticker := pub.Images[0].ArtifactID
	art, err := e.b.Artifacts.GetArtifact(e.ctx(), sticker)
	if err != nil || art.RoomID != e.roomID || art.CreatedBy != "jack" {
		t.Fatalf("app artifact = %+v %v", art, err)
	}
	fake.imageB64 = base64.StdEncoding.EncodeToString(png8x6(t))
	r = e.call(http.MethodPost, jack+"~app/ai/image", wire.MediaImageRequest{Prompt: "bluer", ArtifactID: sticker})
	if err := json.Unmarshal(r.body, &pub); err != nil || r.status != http.StatusOK || pub.Images[0].ArtifactID != sticker || pub.Images[0].Version != 2 {
		t.Fatalf("app edit = %d %s", r.status, r.body)
	}
	if v, err := e.b.Artifacts.GetArtifactVersion(e.ctx(), sticker, 2); err != nil || v.SourceJobID != "" || v.AuthorUserID != "jack" || v.Summary != "drawn in an app" {
		t.Fatalf("app version = %+v %v", v, err)
	}
	fake.imageB64 = ""
	sink.take()

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

// M6: an organization's own provider replaces the platform's for its
// members while its host stays allowed (the assist org-model pattern); a
// host the platform does not allow is refused; a member without org admins
// cannot set one; an empty base URL goes back to the platform's.
func TestMediaOrgProvider(t *testing.T) {
	fake := &mediaProviderFake{transcript: "platform says"}
	e := mediaGW(t, fake)
	orgFake := &mediaProviderFake{transcript: "the org's own provider"}
	orgSrv := httptest.NewServer(orgFake)
	t.Cleanup(orgSrv.Close)
	// The platform row allows the org host.
	r := e.call(http.MethodPut, "/v1/platform/media/transcribe", wire.MediaProviderUpdate{
		Kind: "openai-compatible", BaseURL: fakeBaseOf(t, e), Model: "media-model", APIKey: "sk-media",
		OrgHosts: []string{hostOfBaseURL(orgSrv.URL)}, Params: map[string]string{"image.size.square": "1024x1024"},
	}, e.superAdmin())
	if r.status != http.StatusOK {
		t.Fatalf("platform orgHosts = %d %s", r.status, r.body)
	}
	// An org admin sets the organization's own provider on the allowed host.
	orgAdmin := "Authorization: Bearer " + e.gw.Signer.Issue(auth.Claims{Subject: "ada", Tenant: e.tenant, Role: "org_admin", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	r = e.call(http.MethodPut, "/v1/tenant/media/transcribe", wire.MediaProviderUpdate{
		Kind: "openai-compatible", BaseURL: orgSrv.URL + "/v1", Model: "org-model", APIKey: "sk-org",
	}, orgAdmin)
	if r.status != http.StatusOK {
		t.Fatalf("org provider = %d %s", r.status, r.body)
	}
	// A member's call now answers through the org provider's model.
	res := e.callRaw(http.MethodPost, "/v1/media/transcriptions", "audio/wav", []byte("ORG"), e.as("maya"))
	if res.status != http.StatusOK {
		t.Fatalf("transcribe = %d %s", res.status, res.body)
	}
	var reply wire.MediaTranscribeReply
	if json.Unmarshal(res.body, &reply) != nil || reply.Text != "the org's own provider" {
		t.Fatalf("reply = %s", res.body)
	}
	if orgFake.last["model"] != "org-model" {
		t.Fatalf("org model = %v", orgFake.last)
	}
	// A host the platform does not allow is refused.
	other := httptest.NewServer(&mediaProviderFake{})
	t.Cleanup(other.Close)
	r = e.call(http.MethodPut, "/v1/tenant/media/transcribe", wire.MediaProviderUpdate{
		Kind: "openai-compatible", BaseURL: other.URL + "/v1", Model: "m",
	}, orgAdmin)
	if r.status != http.StatusBadRequest {
		t.Fatalf("a host the platform does not allow = %d %s", r.status, r.body)
	}
	// A member cannot set the organization's provider.
	r = e.call(http.MethodPut, "/v1/tenant/media/transcribe", wire.MediaProviderUpdate{
		Kind: "openai-compatible", BaseURL: orgSrv.URL + "/v1", Model: "m",
	}, e.as("maya"))
	if r.status != http.StatusForbidden {
		t.Fatalf("a member setting the org provider = %d", r.status)
	}
	// An empty base URL goes back to the platform's provider.
	r = e.call(http.MethodPut, "/v1/tenant/media/transcribe", wire.MediaProviderUpdate{Kind: "openai-compatible", BaseURL: "", Model: "", ClearKey: true}, orgAdmin)
	if r.status != http.StatusOK {
		t.Fatalf("clear org provider = %d %s", r.status, r.body)
	}
	res = e.callRaw(http.MethodPost, "/v1/media/transcriptions", "audio/wav", []byte("P"), e.as("maya"))
	if res.status != http.StatusOK {
		t.Fatalf("platform again = %d %s", res.status, res.body)
	}
	// The platform row's view carries the allowed hosts.
	r = e.call(http.MethodGet, "/v1/platform/media/transcribe", nil, e.superAdmin())
	var view wire.MediaProviderConfig
	if json.Unmarshal(r.body, &view) != nil || len(view.OrgHosts) != 1 {
		t.Fatalf("platform view = %s", r.body)
	}
}

// fakeBaseOf names the fake the mediaGW config used (re-read from the row).
func fakeBaseOf(t *testing.T, e *appGW) string {
	t.Helper()
	r := e.call(http.MethodGet, "/v1/platform/media/transcribe", nil, e.superAdmin())
	var view wire.MediaProviderConfig
	if json.Unmarshal(r.body, &view) != nil {
		t.Fatalf("read the provider = %s", r.body)
	}
	return view.BaseURL
}

// An agent's image call (the room relay's ?job=&room=) publishes each image
// as its own room artifact, minted by the room service, in the job's
// channel and sourced to the job; a caller-auth provider is never sent the
// bridge owner's session for it.
func TestMediaImagePublishUnderJobGrant(t *testing.T) {
	fake := &mediaProviderFake{}
	e := mediaGW(t, fake)
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, ChannelID: "ch-1", MessageSeq: 4, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	bridge := "Authorization: Bearer " + e.session("jack")
	path := "/v1/media/image?room=" + e.roomID + "&job=job-1"
	r := e.call(http.MethodPost, path, wire.MediaImageRequest{Prompt: "two posters", Count: 2, Publish: true, FileName: "poster.png"}, bridge)
	if r.status != http.StatusOK {
		t.Fatalf("publish = %d %s", r.status, r.body)
	}
	var reply wire.MediaImageReply
	if err := json.Unmarshal(r.body, &reply); err != nil || len(reply.Images) != 2 {
		t.Fatalf("reply = %s %v", r.body, err)
	}
	a, b := reply.Images[0], reply.Images[1]
	if a.ArtifactID == "" || b.ArtifactID == "" || a.ArtifactID == b.ArtifactID || a.DataURL != "" {
		t.Fatalf("two images publish two artifacts, not inlined: %+v", reply.Images)
	}
	for _, img := range reply.Images {
		art, err := e.b.Artifacts.GetArtifact(e.ctx(), img.ArtifactID)
		if err != nil {
			t.Fatal(err)
		}
		if art.RoomID != e.roomID || art.ChannelID != "ch-1" || art.CreatedBy != "maya" || art.CurrentVersion != 1 {
			t.Fatalf("artifact = %+v", art)
		}
		v, err := e.b.Artifacts.GetArtifactVersion(e.ctx(), img.ArtifactID, 1)
		if err != nil || v.SourceJobID != "job-1" || v.SourceSeq != 4 || v.MimeType != "image/png" {
			t.Fatalf("version = %+v %v", v, err)
		}
	}

	// A caller-auth provider: the member's own call forwards their session;
	// the agent's call (the bearer is jack's, the call maya's) finds none.
	model := httptest.NewServer(fake)
	t.Cleanup(model.Close)
	if r := e.call(http.MethodPut, "/v1/platform/media/image", wire.MediaProviderUpdate{
		Kind: "openai-compatible", BaseURL: model.URL + "/v1", Model: "media-model", Auth: "caller"}, e.superAdmin()); r.status != http.StatusOK {
		t.Fatalf("configure caller auth = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "mine"}, e.as("maya")); r.status != http.StatusOK {
		t.Fatalf("own call = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, path, wire.MediaImageRequest{Prompt: "theirs", Publish: true}, bridge); r.status != http.StatusConflict {
		t.Fatalf("a job grant forwarded the bridge owner's session: %d %s", r.status, r.body)
	}
}

// An agent edits a room picture (docs/prd/2026-10-17-image-generation-redesign.md
// §2.2–2.3): the file is read as the requester and sent as the image to
// change, a region becomes a mask, references ride as more image parts,
// and the result is the file's next version with editedFrom; a version
// published meanwhile makes the edit a new file, said so; another room's
// file, a region outside the image and a member's own call naming room
// pictures are refused before any spend.
func TestMediaImageEditUnderJobGrant(t *testing.T) {
	fake := &mediaProviderFake{}
	e := mediaGW(t, fake)
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, ChannelID: "ch-1", MessageSeq: 4, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	bridge := "Authorization: Bearer " + e.session("jack")
	path := "/v1/media/image?room=" + e.roomID + "&job=job-1"
	draw := func(req wire.MediaImageRequest) (int, wire.MediaImageReply, string) {
		t.Helper()
		r := e.call(http.MethodPost, path, req, bridge)
		var reply wire.MediaImageReply
		_ = json.Unmarshal(r.body, &reply)
		return r.status, reply, string(r.body)
	}
	// The poster the agent drew earlier (v1), 8×6.
	_, first, _ := draw(wire.MediaImageRequest{Prompt: "a poster", Publish: true, FileName: "poster.png"})
	poster := first.Images[0].ArtifactID
	fake.imageB64 = base64.StdEncoding.EncodeToString(png8x6(t))
	_, redraw, _ := draw(wire.MediaImageRequest{Prompt: "a poster, 8 by 6", ArtifactID: poster})
	if len(redraw.Images) != 1 || redraw.Images[0].Version != 2 {
		t.Fatalf("whole edit = %+v", redraw)
	}
	// The provider answering the same picture: no new version, and said so.
	_, same, _ := draw(wire.MediaImageRequest{Prompt: "the same", ArtifactID: poster})
	if same.Images[0].Version != 2 || !strings.Contains(same.Images[0].Note, "unchanged") {
		t.Fatalf("unchanged edit = %+v", same)
	}

	// A region edit with a reference: the next version, editedFrom the one read.
	_, logoReply, _ := draw(wire.MediaImageRequest{Prompt: "the logo", Publish: true, FileName: "logo.png"})
	logo := logoReply.Images[0].ArtifactID
	fake.imageB64 = base64.StdEncoding.EncodeToString(append(png8x6(t), 0)) // a different picture
	code, reply, raw := draw(wire.MediaImageRequest{Prompt: "put the logo top left", ArtifactID: poster, Region: &wire.MediaImageRegion{X: 0, Y: 0, W: 4, H: 3}, Inputs: []string{logo}})
	if code != http.StatusOK || len(reply.Images) != 1 {
		t.Fatalf("region edit = %d %s", code, raw)
	}
	got := reply.Images[0]
	if got.ArtifactID != poster || got.Version != 3 || got.FileName != "poster.png" || got.Note != "" || got.DataURL != "" {
		t.Fatalf("edit result = %+v", got)
	}
	if fake.paths[len(fake.paths)-1] != "/v1/images/edits" || fake.last["__images"] != "2" || fake.last["__mask"] == "" {
		t.Fatalf("provider got %v %v", fake.paths[len(fake.paths)-1], fake.last)
	}
	v, err := e.b.Artifacts.GetArtifactVersion(e.ctx(), poster, 3)
	if err != nil || v.EditedFrom != 2 || v.SourceJobID != "job-1" || v.SourceSeq != 4 || v.AuthorUserID != "maya" {
		t.Fatalf("version = %+v %v", v, err)
	}

	// A painted area: the message's mask attachment is the edit's mask, as sent.
	painted, err := media.EditMask(png8x6(t), media.Region{X: 1, Y: 1, W: 3, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	mask := e.attachment(e.roomID, "mask.png", "image/png", string(painted))
	fake.imageB64 = base64.StdEncoding.EncodeToString(append(png8x6(t), 1))
	code, reply, raw = draw(wire.MediaImageRequest{Prompt: "a hat", ArtifactID: poster, Region: &wire.MediaImageRegion{X: 1, Y: 1, W: 3, H: 2, Mask: mask}})
	if code != http.StatusOK || reply.Images[0].Version != 4 || fake.last["__mask"] != fmt.Sprintf("%d bytes", len(painted)) {
		t.Fatalf("painted edit = %d %s, provider mask %s", code, raw, fake.last["__mask"])
	}

	// A version published between the read and the publish: the edit is a new file.
	fake.imageB64 = base64.StdEncoding.EncodeToString(append(png8x6(t), 0, 0))
	e.room.Artifacts = racingArtifacts{ArtifactStore: e.b.Artifacts, poster: poster}
	code, reply, raw = draw(wire.MediaImageRequest{Prompt: "warmer", ArtifactID: poster})
	e.room.Artifacts = e.b.Artifacts
	if code != http.StatusOK || reply.Images[0].ArtifactID == poster || !strings.Contains(reply.Images[0].Note, "saved as a new file") {
		t.Fatalf("race = %d %s", code, raw)
	}

	// Refused before any spend.
	calls := len(fake.paths)
	away := e.artifact("room-elsewhere", store.ArtifactKindImage)
	for name, req := range map[string]wire.MediaImageRequest{
		"another room's file": {Prompt: "x", ArtifactID: away},
		"region outside":      {Prompt: "x", ArtifactID: poster, Region: &wire.MediaImageRegion{X: 6, Y: 0, W: 4, H: 2}},
		"region, no image":    {Prompt: "x", Region: &wire.MediaImageRegion{X: 0, Y: 0, W: 1, H: 1}},
		"mask of another size": {Prompt: "x", ArtifactID: poster, Region: &wire.MediaImageRegion{X: 0, Y: 0, W: 1, H: 1,
			Mask: e.attachment(e.roomID, "small.png", "image/png", string(tinyTestPNG))}},
		"mask not an attachment": {Prompt: "x", ArtifactID: poster, Region: &wire.MediaImageRegion{X: 0, Y: 0, W: 1, H: 1, Mask: "att_nope"}},
		"too many inputs":        {Prompt: "x", Inputs: []string{logo, logo, logo, logo, logo}},
	} {
		if code, _, raw := draw(req); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
	if r := e.call(http.MethodPost, "/v1/media/image", wire.MediaImageRequest{Prompt: "x", ArtifactID: poster}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Errorf("a member's call named a room picture: %d %s", r.status, r.body)
	}
	if len(fake.paths) != calls {
		t.Fatalf("the provider was called for a refused request")
	}
}

// png8x6 is an opaque 8×6 PNG.
func png8x6(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// racingArtifacts publishes a version of poster just before the edit's own
// publish, as another member would between the read and the write.
type racingArtifacts struct {
	store.ArtifactStore
	poster string
}

func (a racingArtifacts) PublishArtifactVersion(ctx context.Context, id string, v store.ArtifactVersion, base int) (store.Artifact, store.ArtifactVersion, error) {
	if id == a.poster {
		cur, err := a.ArtifactStore.GetArtifact(ctx, id)
		if err != nil {
			return store.Artifact{}, store.ArtifactVersion{}, err
		}
		theirs := v
		theirs.AuthorUserID, theirs.SourceJobID, theirs.EditedFrom = "jack", "", 0
		if _, _, err := a.ArtifactStore.PublishArtifactVersion(ctx, id, theirs, cur.CurrentVersion); err != nil {
			return store.Artifact{}, store.ArtifactVersion{}, err
		}
	}
	return a.ArtifactStore.PublishArtifactVersion(ctx, id, v, base)
}
