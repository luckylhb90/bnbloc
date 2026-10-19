package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The OpenAI-compatible videos API (docs/prd/2026-10-19-video-generation.md
// §2.1): POST {base}/videos starts a video and answers its video object
// (an id and a status), GET {base}/videos/{id} answers the object again,
// and GET {base}/videos/{id}/content the file once it is completed. A video
// takes from half a minute to minutes, so nothing here waits for one: the
// caller polls the id.

// videoObject is the videos API's video object, as far as it is read.
type videoObject struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Model  string `json:"model"`
	// Error is a failed video's reason: an object with a message, or a
	// string.
	Error any `json:"error"`
}

// SubmitVideo posts the video's JSON to {base}/videos: the model, the
// prompt, the size the aspect names and, when asked, its length (the API
// takes seconds as a string), plus the params' extra body fields.
func (a *openAICompatible) SubmitVideo(ctx context.Context, p Provider, req VideoRequest) (VideoStatus, error) {
	body := map[string]any{
		"model":  p.Choose(req.Model),
		"prompt": req.Prompt,
		"size":   videoSize(req.Aspect, p.Params),
	}
	if req.Seconds > 0 {
		body["seconds"] = strconv.Itoa(req.Seconds)
	}
	for k, v := range p.Params {
		if strings.HasPrefix(k, paramVideoPrefix) && !strings.HasPrefix(k, paramVideoSizePrefix) {
			body[strings.TrimPrefix(k, paramVideoPrefix)] = v
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return VideoStatus{}, err
	}
	return videoCall(ctx, p, http.MethodPost, "/videos", raw)
}

// VideoStatus reads the video object at {base}/videos/{id}.
func (a *openAICompatible) VideoStatus(ctx context.Context, p Provider, id string) (VideoStatus, error) {
	return videoCall(ctx, p, http.MethodGet, "/videos/"+url.PathEscape(id), nil)
}

// VideoContent reads {base}/videos/{id}/content: an MP4 of at most max
// bytes. A file over max is refused (ErrTooLarge) before it is read when
// the provider says its length, and as soon as the read passes max
// otherwise: never truncated.
func (a *openAICompatible) VideoContent(ctx context.Context, p Provider, id string, max int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url("/videos/"+url.PathEscape(id)+"/content"), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := p.do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > max {
		return nil, "", tooLarge(resp.ContentLength, max)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	if int64(len(data)) > max {
		return nil, "", tooLarge(-1, max)
	}
	mimeType := SniffVideo(data)
	if mimeType == "" {
		return nil, "", fmt.Errorf("%w: the video's file is not an MP4", ErrProviderUnreachable)
	}
	return data, mimeType, nil
}

// ListModels reads {base}/models: the ids of the list's data.
func (a *openAICompatible) ListModels(ctx context.Context, p Provider) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url("/models"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	var reply struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: the reply is not a model list", ErrProviderUnreachable)
	}
	out := make([]string, 0, len(reply.Data))
	for _, m := range reply.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// videoCall sends one videos API request (a JSON body when set) and reads
// the video object it answers.
func videoCall(ctx context.Context, p Provider, method, path string, body []byte) (VideoStatus, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.url(path), rd)
	if err != nil {
		return VideoStatus{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.do(req)
	if err != nil {
		return VideoStatus{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody))
	if err != nil {
		return VideoStatus{}, fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	var v videoObject
	if err := json.Unmarshal(raw, &v); err != nil || strings.TrimSpace(v.ID) == "" {
		return VideoStatus{}, fmt.Errorf("%w: the reply is not a video object", ErrProviderUnreachable)
	}
	return videoStatusOf(v), nil
}

// videoStatusOf maps the provider's status onto the video's three states:
// queued, in progress and processing are rendering; completed is done;
// failed is failed with the provider's reason. A status the family does
// not name is failed, saying which: an agent polling it would otherwise
// wait on a state that never ends.
func videoStatusOf(v videoObject) VideoStatus {
	out := VideoStatus{ID: strings.TrimSpace(v.ID), Model: strings.TrimSpace(v.Model)}
	switch strings.TrimSpace(v.Status) {
	case "queued", "in_progress", "processing":
		out.State = wire.MediaVideoRendering
	case "completed":
		out.State = wire.MediaVideoDone
	case "failed":
		out.State = wire.MediaVideoFailed
		if out.Error = errorMessage(v.Error); out.Error == "" {
			out.Error = "the provider failed the video without a reason"
		}
	default:
		out.State = wire.MediaVideoFailed
		out.Error = errorMessage(fmt.Sprintf("the provider answered the status %q", v.Status))
	}
	return out
}

// tooLarge is ErrTooLarge saying the sizes (size -1: unknown, over max).
func tooLarge(size, max int64) error {
	if size < 0 {
		return fmt.Errorf("%w: the video is over %s, the room's file cap", ErrTooLarge, megabytes(max))
	}
	return fmt.Errorf("%w: the video is %s, over %s, the room's file cap", ErrTooLarge, megabytes(size), megabytes(max))
}

// megabytes is a byte count as people read it.
func megabytes(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
}

// paramVideoPrefix / paramVideoSizePrefix are the params namespaces of a
// video: its extra body fields, and its size presets per aspect
// (video.size.16:9).
const (
	paramVideoPrefix     = "video."
	paramVideoSizePrefix = "video.size."
)

// videoSize resolves the request's aspect to a provider size value: the
// params' preset for that aspect, else the family's sizes (1280x720 and
// 720x1280, the videos API's own).
func videoSize(aspect string, params map[string]string) string {
	if aspect == "" {
		aspect = wire.MediaVideoAspectLandscape
	}
	if v, ok := params[paramVideoSizePrefix+aspect]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if aspect == wire.MediaVideoAspectPortrait {
		return "720x1280"
	}
	return "1280x720"
}

// SniffVideo reports a video's MIME type from its magic bytes (an ISO base
// media file: an MP4's `ftyp` box first), "" when it is not one.
func SniffVideo(b []byte) string {
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		return "video/mp4"
	}
	return ""
}
