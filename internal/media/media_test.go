package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// box is a test seal box.
func box(t *testing.T) *sealbox.Box {
	t.Helper()
	b, err := sealbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// settings is a test Settings with a memory store.
func settings(t *testing.T) *Settings {
	t.Helper()
	return &Settings{Store: store.NewMemoryPlatformSettings(), Box: box(t), Now: func() int64 { return 42 }}
}

// putProvider stores the test provider for cap.
func putProvider(t *testing.T, s *Settings, cap Capability, srvURL string) {
	t.Helper()
	_, err := s.Put(context.Background(), cap, "", wire.MediaProviderUpdate{
		Kind: AdapterKindOpenAICompatible, BaseURL: srvURL, Model: "test-model", APIKey: "sk-test",
		Params: map[string]string{"image.size.square": "1024x1024"},
	}, "ops")
	if err != nil {
		t.Fatal(err)
	}
}

// provider resolves cap's provider or fails.
func provider(t *testing.T, s *Settings, cap Capability) Provider {
	t.Helper()
	p, ok, err := s.Provider(context.Background(), cap, "")
	if err != nil || !ok {
		t.Fatalf("provider = %v, %v, %v", p, ok, err)
	}
	return p
}

// A settings round trip: the provider stores, the key never comes back, an
// unknown kind and a params overage are refused, an empty base URL clears.
func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := settings(t)
	if _, ok, _ := s.Provider(ctx, CapTranscribe, ""); ok {
		t.Fatal("no provider is configured yet")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello"}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapTranscribe, srv.URL+"/v1")
	view, err := s.View(ctx, CapTranscribe, "")
	if err != nil || !view.KeySet || view.Model != "test-model" || view.Kind != AdapterKindOpenAICompatible {
		t.Fatalf("view = %+v %v", view, err)
	}
	if view.Params["image.size.square"] != "1024x1024" {
		t.Fatalf("params = %v", view.Params)
	}
	p := provider(t, s, CapTranscribe)
	if p.Model.APIKey != "sk-test" {
		t.Fatal("the key did not come back to the caller")
	}
	row, _, _ := s.Store.GetPlatformSetting(ctx, "media.transcribe")
	if strings.Contains(row.Value, "sk-test") {
		t.Fatal("the key is stored in clear")
	}
	if _, err := s.Put(ctx, CapTranscribe, "", wire.MediaProviderUpdate{Kind: "no-such-adapter", BaseURL: srv.URL, Model: "m"}, "ops"); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	if _, err := s.Put(ctx, CapTranscribe, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAICompatible, BaseURL: srv.URL + "/v1", Model: "m",
		Params: map[string]string{"k": strings.Repeat("x", 600)}}, "ops"); err == nil {
		t.Fatal("a params value over 512 bytes must be refused")
	}
	if _, err := s.Put(ctx, CapTranscribe, "", wire.MediaProviderUpdate{Kind: AdapterKindOpenAICompatible, BaseURL: "", Model: ""}, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Provider(ctx, CapTranscribe, ""); ok {
		t.Fatal("an empty base URL clears the provider")
	}
}

// The transcription wire shape: multipart fields model/file, the bearer
// key, the reply's text capped, never splitting a surrogate pair.
func TestTranscribeWire(t *testing.T) {
	s := settings(t)
	var gotModel, gotAuth, gotFile, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no file part: %v", err)
		} else {
			b, _ := io.ReadAll(file)
			gotFile, gotType = string(b), header.Header.Get("Content-Type")
			_ = file.Close()
		}
		gotModel = r.FormValue("model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hi there"}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapTranscribe, srv.URL+"/v1")
	text, err := provider(t, s, CapTranscribe).Transcribe(context.Background(), []byte("AUDIO"), "audio/wav", "clip.wav")
	if err != nil || text != "hi there" {
		t.Fatalf("text = %q %v", text, err)
	}
	if gotModel != "test-model" || gotAuth != "Bearer sk-test" || gotFile != "AUDIO" || gotType != "audio/wav" {
		t.Fatalf("model=%q auth=%q file=%q type=%q", gotModel, gotAuth, gotFile, gotType)
	}
}

// A transcript is capped at MaxMediaTranscriptChars without splitting a
// UTF-16 surrogate pair (the cap runes end in a leading surrogate).
func TestTranscriptCap(t *testing.T) {
	s := settings(t)
	long := strings.Repeat("a", wire.MaxMediaTranscriptChars) + "\U0001F600"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reply struct {
			Text string `json:"text"`
		}
		reply.Text = long
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer srv.Close()
	putProvider(t, s, CapTranscribe, srv.URL+"/v1")
	text, err := provider(t, s, CapTranscribe).Transcribe(context.Background(), []byte("A"), "audio/wav", "a.wav")
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(text)); got != wire.MaxMediaTranscriptChars {
		t.Fatalf("cap = %d runes", got)
	}
}

// The generation wire shape: response_format b64_json, the size preset per
// aspect, a reply that is not inline base64 refused, the image sniffed.
func TestGenerateWire(t *testing.T) {
	s := settings(t)
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBody = body
		b64 := base64.StdEncoding.EncodeToString(tinyPNG)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `","revised_prompt":"a better one"}]}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapImage, srv.URL+"/v1")
	imgs, err := provider(t, s, CapImage).Generate(context.Background(), ImageRequest{Prompt: "a poster", Aspect: wire.MediaImageAspectLandscape})
	if err != nil || len(imgs) != 1 {
		t.Fatalf("imgs = %v %v", imgs, err)
	}
	img := imgs[0]
	if !bytes.Equal(img.Bytes, tinyPNG) || img.MimeType != "image/png" || img.RevisedPrompt != "a better one" {
		t.Fatalf("img = %d bytes %q %q", len(img.Bytes), img.MimeType, img.RevisedPrompt)
	}
	if gotBody["response_format"] != "b64_json" || gotBody["size"] != "1536x1024" || gotBody["prompt"] != "a poster" || gotBody["model"] != "test-model" {
		t.Fatalf("body = %v", gotBody)
	}
}

// A batch ask draws n images in ONE provider call (n rides the body).
func TestGenerateBatchWire(t *testing.T) {
	s := settings(t)
	var gotN any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotN = body["n"]
		b64 := base64.StdEncoding.EncodeToString(tinyPNG)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"},{"b64_json":"` + b64 + `"},{"b64_json":"` + b64 + `"}]}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapImage, srv.URL+"/v1")
	imgs, err := provider(t, s, CapImage).Generate(context.Background(), ImageRequest{Prompt: "three cats", Count: 3})
	if err != nil || len(imgs) != 3 {
		t.Fatalf("imgs = %d %v", len(imgs), err)
	}
	if gotN != float64(3) {
		t.Fatalf("n = %v", gotN)
	}
}

// An edit posts multipart to /images/edits with the input image as the
// "image" part and the prompt as a field.
func TestGenerateEditWire(t *testing.T) {
	s := settings(t)
	var gotPath, gotPrompt string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		gotPrompt = r.FormValue("prompt")
		file, _, err := r.FormFile("image")
		if err == nil {
			gotFile, _ = io.ReadAll(file)
			_ = file.Close()
		}
		b64 := base64.StdEncoding.EncodeToString(tinyPNG)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"}]}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapImage, srv.URL+"/v1")
	in := tinyPNG
	imgs, err := provider(t, s, CapImage).Generate(context.Background(), ImageRequest{
		Prompt: "a red hat on the cat", Input: &ImageInput{Bytes: in, MimeType: "image/png"},
	})
	if err != nil || len(imgs) != 1 {
		t.Fatalf("edit = %v %v", imgs, err)
	}
	if gotPath != "/v1/images/edits" || gotPrompt != "a red hat on the cat" || !bytes.Equal(gotFile, in) {
		t.Fatalf("path=%q prompt=%q file=%d bytes", gotPath, gotPrompt, len(gotFile))
	}
}

// The aspect presets: the params' own size for an aspect, the defaults.
func TestAspectSize(t *testing.T) {
	params := map[string]string{"image.size.portrait": "999x777"}
	if got := aspectSize(wire.MediaImageAspectPortrait, params); got != "999x777" {
		t.Fatalf("portrait = %s", got)
	}
	if got := aspectSize(wire.MediaImageAspectSquare, params); got != "1024x1024" {
		t.Fatalf("square = %s", got)
	}
	if got := aspectSize("", params); got != "1024x1024" {
		t.Fatalf("empty aspect = %s", got)
	}
}

// A provider that refuses the parameters (400) is retried once without
// response_format; a provider whose reply holds no inline image is refused
// (no URL egress path exists).
func TestGenerateRefusals(t *testing.T) {
	s := settings(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if calls == 1 {
			http.Error(w, `{"error":{"message":"response_format is not supported"}}`, http.StatusBadRequest)
			return
		}
		if _, ok := body["response_format"]; ok {
			t.Error("the retry must not ask for response_format")
		}
		_, _ = w.Write([]byte(`{"data":[{"url":"https://elsewhere.example/x.png"}]}`))
	}))
	defer srv.Close()
	putProvider(t, s, CapImage, srv.URL+"/v1")
	_, err := provider(t, s, CapImage).Generate(context.Background(), ImageRequest{Prompt: "a poster"})
	if err == nil || !strings.Contains(err.Error(), "no inline image") {
		t.Fatalf("a URL reply must be refused: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d (the 400 must be retried once)", calls)
	}
}

// A provider refusal carries its own status and message; an unreachable
// provider is ErrProviderUnreachable.
func TestProviderErrors(t *testing.T) {
	s := settings(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"content policy"}}`, http.StatusUnavailableForLegalReasons)
	}))
	defer srv.Close()
	putProvider(t, s, CapImage, srv.URL+"/v1")
	_, err := provider(t, s, CapImage).Generate(context.Background(), ImageRequest{Prompt: "nope"})
	var pe *ProviderError
	if err == nil || !errors.As(err, &pe) || pe.Status != 451 || pe.Message != "content policy" {
		t.Fatalf("refusal = %v", err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv2.Close()
	putProvider(t, s, CapTranscribe, srv2.URL+"/v1")
	if _, err := provider(t, s, CapTranscribe).Transcribe(context.Background(), []byte("A"), "audio/wav", "a.wav"); err == nil {
		t.Fatal("garbage must be refused")
	}
}

// tinyPNG is a 1x1 PNG.
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // signature
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89,
}

// The client is held to the provider's host: a redirect is not followed.
func TestNoRedirect(t *testing.T) {
	s := settings(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	putProvider(t, s, CapTranscribe, srv.URL+"/v1")
	_, err := provider(t, s, CapTranscribe).Transcribe(context.Background(), []byte("A"), "audio/wav", "a.wav")
	if err == nil {
		t.Fatal("a redirect must not be followed")
	}
}

// A provider whose stored kind names no registered adapter fails to resolve
// (an adapter was removed): never a panic, never a silent fallback.
func TestRemovedAdapter(t *testing.T) {
	ctx := context.Background()
	s := settings(t)
	if err := s.Store.PutPlatformSetting(ctx, store.PlatformSetting{Key: "media.image",
		Value: `{"baseUrl":"https://llm.example.com/v1","model":"m","kind":"gone"}`}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Provider(ctx, CapImage, ""); err == nil || ok {
		t.Fatalf("a removed adapter must fail: %v %v", ok, err)
	}
}
