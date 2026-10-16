package roomtools

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// The image generation reply: a tiny PNG as a data URL, one image.
const tinyPNGDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNgAAIAAAUAAen63NgAAAAASUVORK5CYII="

var imageReply = []byte(`{"images":[{"dataUrl":"` + tinyPNGDataURL + `","mimeType":"image/png","width":1,"height":1,"revisedPrompt":"a nicer poster"}]}`)

// imageEnv is a bound env whose gateway answers one image reply and whose
// Publish records what it was asked to publish.
func imageEnv(t *testing.T) (*Env, *recordingGateway, *[]string) {
	t.Helper()
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) { return imageReply, nil }}
	env := contentEnv(fakeWorkspace{}, gw, true)
	published := []string{}
	env.Config.Publish = func(ctx context.Context, room string, job Job, name, mimeType string, content []byte) (string, error) {
		if ctx == nil {
			t.Error("publish without a context")
		}
		if room != "room-1" || job.JobID != "job-1" {
			t.Errorf("publish room=%s job=%s", room, job.JobID)
		}
		published = append(published, name+" "+mimeType)
		return "artf_generated", nil
	}
	return env, gw, &published
}

// generate_image calls the media route as the job's requester and publishes
// the reply's image through the one publish path; the image itself never
// enters the result, the provider's revised prompt comes fenced.
func TestGenerateImage(t *testing.T) {
	env, gw, published := imageEnv(t)
	res := generateImage(context.Background(), env, json.RawMessage(`{"prompt":"a team retro poster","aspect":"landscape","fileName":"retro.png"}`))
	if res.Err != "" {
		t.Fatalf("generate = %s", res.Err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("calls = %+v", gw.calls)
	}
	u, _ := url.Parse(gw.calls[0].Path)
	if u.Path != "/v1/media/image" || u.Query().Get("job") != "job-1" || u.Query().Get("room") != "room-1" {
		t.Fatalf("call = %s", gw.calls[0].Path)
	}
	if strings.Join(*published, ";") != "retro.png image/png" {
		t.Fatalf("published = %v", *published)
	}
	out := res.Value.(map[string]any)
	images, _ := out["images"].([]map[string]any)
	if len(images) != 1 || images[0]["artifactId"] != "artf_generated" || images[0]["fileName"] != "retro.png" || images[0]["width"] != 1 {
		t.Fatalf("result = %+v", out)
	}
	// The result never carries the image bytes; the revised prompt is fenced.
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "iVBORw0KGgo") {
		t.Fatal("the image bytes entered the result")
	}
	if revised, _ := images[0]["revisedPrompt"].(string); !strings.Contains(revised, roomlog.Fence(imageAuthor, "a nicer poster")) {
		t.Fatalf("revised = %q", revised)
	}
}

// A batch publishes one image per name, numbered after the first.
func TestGenerateImageBatch(t *testing.T) {
	env, gw, published := imageEnv(t)
	gw.reply = func(string, string) ([]byte, error) {
		return []byte(`{"images":[` +
			`{"dataUrl":"` + tinyPNGDataURL + `","mimeType":"image/png","width":1,"height":1},` +
			`{"dataUrl":"` + tinyPNGDataURL + `","mimeType":"image/png","width":1,"height":1},` +
			`{"dataUrl":"` + tinyPNGDataURL + `","mimeType":"image/png","width":1,"height":1}]}`), nil
	}
	res := generateImage(context.Background(), env, json.RawMessage(`{"prompt":"three poster options","count":3,"fileName":"poster.png"}`))
	if res.Err != "" {
		t.Fatalf("batch = %s", res.Err)
	}
	if strings.Join(*published, ";") != "poster.png image/png;poster-2.png image/png;poster-3.png image/png" {
		t.Fatalf("published = %v", *published)
	}
	out := res.Value.(map[string]any)
	images, _ := out["images"].([]map[string]any)
	if len(images) != 3 {
		t.Fatalf("images = %+v", out)
	}
}

// A default file name is derived from the prompt; the aspect may be empty.
func TestGenerateImageDefaultName(t *testing.T) {
	env, _, published := imageEnv(t)
	res := generateImage(context.Background(), env, json.RawMessage(`{"prompt":"Blue Coffee Mug"}`))
	if res.Err != "" {
		t.Fatalf("generate = %s", res.Err)
	}
	if strings.Join(*published, ";") != "blue-coffee-mug.png image/png" {
		t.Fatalf("published = %v", *published)
	}
}

// Refusals: a missing prompt, an unbound session, a failed generation and a
// failed publish reach the agent as tool errors it can act on.
func TestGenerateImageRefusals(t *testing.T) {
	env, _, _ := imageEnv(t)
	if res := generateImage(context.Background(), env, json.RawMessage(`{"prompt":"  "}`)); res.Err == "" {
		t.Fatal("a missing prompt must be refused")
	}
	unbound := &Env{Session: &Session{scope: Scope{RoomID: "room-1"}}, Gateway: env.Gateway, Config: env.Config}
	if res := generateImage(context.Background(), unbound, json.RawMessage(`{"prompt":"x"}`)); res.Err == "" || !strings.Contains(res.Err, "answering a request") {
		t.Fatalf("unbound = %q", res.Err)
	}
	env2, gw, _ := imageEnv(t)
	gw.reply = func(string, string) ([]byte, error) {
		return nil, &StatusError{Status: 409, Body: []byte(`{"code":"","error":"no provider"}`)}
	}
	if res := generateImage(context.Background(), env2, json.RawMessage(`{"prompt":"x"}`)); res.Err == "" || !strings.Contains(res.Err, "no provider") {
		t.Fatalf("gateway refusal = %q", res.Err)
	}
	env3, _, _ := imageEnv(t)
	env3.Config.Publish = func(context.Context, string, Job, string, string, []byte) (string, error) {
		return "", context.DeadlineExceeded
	}
	if res := generateImage(context.Background(), env3, json.RawMessage(`{"prompt":"x"}`)); res.Err == "" {
		t.Fatal("a failed publish must be a tool error")
	}
}
