package media

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// tinyMP4 is the head of an MP4 file: an ftyp box, then bytes.
var tinyMP4 = append([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, make([]byte, 52)...)

// videoFake is an OpenAI-compatible videos API: a submitted video renders
// for `polls` status reads, then answers `final` (its error when failed),
// and its content is `content`.
type videoFake struct {
	mu      sync.Mutex
	polls   int
	final   string
	errBody string
	content []byte
	// chunked streams the content without a length.
	chunked bool
	paths   []string
	body    map[string]any
}

func (f *videoFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
		f.body = nil
		_ = json.NewDecoder(r.Body).Decode(&f.body)
		_, _ = w.Write([]byte(`{"id":"video_abc","object":"video","status":"queued","model":"film-model"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_abc":
		status := "in_progress"
		if f.polls <= 0 {
			status = f.final
		}
		f.polls--
		reply := map[string]any{"id": "video_abc", "object": "video", "status": status, "model": "film-model"}
		if status == "failed" && f.errBody != "" {
			reply["error"] = map[string]any{"code": "policy", "message": f.errBody}
		}
		_ = json.NewEncoder(w).Encode(reply)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_abc/content":
		w.Header().Set("Content-Type", "video/mp4")
		if f.chunked {
			_, _ = w.Write(f.content[:1])
			w.(http.Flusher).Flush() // no Content-Length: the length shows only by reading
			_, _ = w.Write(f.content[1:])
			return
		}
		_, _ = w.Write(f.content)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"film-model"},{"id":"image-model"}]}`))
	default:
		http.Error(w, `{"error":{"message":"no such video"}}`, http.StatusNotFound)
	}
}

// videoProvider stores the fake as the video provider and resolves it.
func videoProvider(t *testing.T, f *videoFake) Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := settings(t)
	putProvider(t, s, CapVideo, srv.URL+"/v1")
	return provider(t, s, CapVideo)
}

// The videos API's wire shape: the submit's model, prompt, size of the
// aspect and seconds as a string; queued and in progress are rendering,
// completed is done; the content is the MP4, sniffed.
func TestVideoWire(t *testing.T) {
	ctx := context.Background()
	f := &videoFake{polls: 1, final: "completed", content: tinyMP4}
	p := videoProvider(t, f)
	st, err := p.SubmitVideo(ctx, VideoRequest{Prompt: "a paper boat", Seconds: 6, Aspect: wire.MediaVideoAspectPortrait})
	if err != nil || st.ID != "video_abc" || st.State != wire.MediaVideoRendering {
		t.Fatalf("submit = %+v %v", st, err)
	}
	if f.body["model"] != "test-model" || f.body["prompt"] != "a paper boat" || f.body["size"] != "720x1280" || f.body["seconds"] != "6" {
		t.Fatalf("body = %v", f.body)
	}
	if st, err = p.VideoStatus(ctx, "video_abc"); err != nil || st.State != wire.MediaVideoRendering {
		t.Fatalf("first poll = %+v %v", st, err)
	}
	if st, err = p.VideoStatus(ctx, "video_abc"); err != nil || st.State != wire.MediaVideoDone || st.Model != "film-model" {
		t.Fatalf("second poll = %+v %v", st, err)
	}
	data, mimeType, err := p.VideoContent(ctx, "video_abc", 1<<20)
	if err != nil || mimeType != "video/mp4" || len(data) != len(tinyMP4) {
		t.Fatalf("content = %d bytes %q %v", len(data), mimeType, err)
	}
	// No length asked: the provider's default, and the landscape size.
	if _, err := p.SubmitVideo(ctx, VideoRequest{Prompt: "a pond"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.body["seconds"]; ok || f.body["size"] != "1280x720" {
		t.Fatalf("default body = %v", f.body)
	}
}

// A failed video says the provider's reason; a status the family does not
// name is failed too (an agent never waits on it); an unknown id is the
// provider's 404; an id that is not one path segment never leaves.
func TestVideoFailures(t *testing.T) {
	ctx := context.Background()
	f := &videoFake{final: "failed", errBody: "the prompt was refused"}
	p := videoProvider(t, f)
	st, err := p.VideoStatus(ctx, "video_abc")
	if err != nil || st.State != wire.MediaVideoFailed || st.Error != "the prompt was refused" {
		t.Fatalf("failed = %+v %v", st, err)
	}
	f.final, f.polls = "melted", 0
	if st, err = p.VideoStatus(ctx, "video_abc"); err != nil || st.State != wire.MediaVideoFailed || !strings.Contains(st.Error, "melted") {
		t.Fatalf("unknown status = %+v %v", st, err)
	}
	var pe *ProviderError
	if _, err := p.VideoStatus(ctx, "video_gone"); !errors.As(err, &pe) || pe.Status != http.StatusNotFound || pe.Message != "no such video" {
		t.Fatalf("unknown id = %v", err)
	}
	calls := len(f.paths)
	for _, id := range []string{"", "..", "a/b", "a b", "a?b", strings.Repeat("x", 513)} {
		if ValidVideoID(id) {
			t.Errorf("%q is valid", id)
		}
		if _, err := p.VideoStatus(ctx, id); err == nil {
			t.Errorf("%q reached the provider", id)
		}
	}
	if len(f.paths) != calls {
		t.Fatalf("a refused id reached the provider: %v", f.paths[calls:])
	}
	if !ValidVideoID("video_ZXhhbXBsZQ==") || !ValidVideoID("vid-1.2:3+4~5") {
		t.Fatal("a provider's id is refused")
	}
}

// The content is read at most max bytes: over it the read is refused, with
// the length known up front or only by reading, never truncated; a file
// that is not an MP4 is refused.
func TestVideoContentBounded(t *testing.T) {
	ctx := context.Background()
	f := &videoFake{final: "completed", content: tinyMP4}
	p := videoProvider(t, f)
	max := int64(len(tinyMP4) - 1)
	if _, _, err := p.VideoContent(ctx, "video_abc", max); !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "the room's file cap") {
		t.Fatalf("known length over the cap = %v", err)
	}
	f.chunked = true
	if _, _, err := p.VideoContent(ctx, "video_abc", max); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("streamed over the cap = %v", err)
	}
	if data, _, err := p.VideoContent(ctx, "video_abc", int64(len(tinyMP4))); err != nil || len(data) != len(tinyMP4) {
		t.Fatalf("at the cap = %d %v", len(data), err)
	}
	f.chunked, f.content = false, []byte("<html>not a video</html>")
	if _, _, err := p.VideoContent(ctx, "video_abc", 1<<20); !errors.Is(err, ErrProviderUnreachable) {
		t.Fatalf("not an MP4 = %v", err)
	}
}

// The model list is the provider's (a test's call that bills nothing).
func TestListModels(t *testing.T) {
	p := videoProvider(t, &videoFake{})
	models, err := p.ListModels(context.Background())
	if err != nil || strings.Join(models, ",") != "film-model,image-model" {
		t.Fatalf("models = %v %v", models, err)
	}
}

// The size presets: the params' own size for an aspect, the defaults.
func TestVideoSize(t *testing.T) {
	params := map[string]string{"video.size.9:16": "1080x1920"}
	if got := videoSize(wire.MediaVideoAspectPortrait, params); got != "1080x1920" {
		t.Fatalf("portrait = %s", got)
	}
	if got := videoSize("", params); got != "1280x720" {
		t.Fatalf("default = %s", got)
	}
}
