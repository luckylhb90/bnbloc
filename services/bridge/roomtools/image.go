package roomtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// Image generation (docs/prd/2026-10-16-voice-and-image-generation.md §4):
// the generate_image tool draws one image on the deployment's image
// provider and publishes it into the room's Docs as a deliverable of the
// running job. The image never passes through the model's context: the
// tool's result is the artifact id, the file name and a one-line note.

// imageAuthor fences the provider's revised prompt (data the provider wrote).
const imageAuthor = "the image provider"

func generateImageTool() Tool {
	return Tool{
		Name:  "generate_image",
		Title: "Draw an image",
		Description: "Draw one image from a prompt on the deployment's image provider and publish it into the room's Docs; the person who asked you sees it. " +
			"The result carries the file's id and name, never the image itself. Say what to draw in the prompt, concretely; aspect is square (default), landscape or portrait. " +
			"Agree with the person first what the image is for; drawing spends their organization's AI budget.",
		InputSchema: objectSchema(map[string]any{
			"prompt":   map[string]any{"type": "string", "description": "What to draw, concretely: subject, setting, style."},
			"aspect":   map[string]any{"type": "string", "enum": []string{"square", "landscape", "portrait"}, "description": "Default square."},
			"fileName": map[string]any{"type": "string", "description": "A workspace-style file name, e.g. poster.png. Default derived from the prompt."},
		}, "prompt"),
		Call: generateImage,
	}
}

// generateImageArgs is the tool's input.
type generateImageArgs struct {
	Prompt   string `json:"prompt"`
	Aspect   string `json:"aspect"`
	FileName string `json:"fileName"`
}

// generateImage runs the tool: one generation as the job's requester, then
// the one publish path into the room's Docs.
func generateImage(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in generateImageArgs
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" {
		return toolError("prompt is required: say what to draw")
	}
	job, _, refused := jobGrant(env, "generate_image")
	if refused != nil {
		return *refused
	}
	room := env.Session.Scope().RoomID
	q := url.Values{"job": {job.JobID}, "room": {room}}
	body := map[string]any{"prompt": in.Prompt}
	if in.Aspect != "" {
		body["aspect"] = in.Aspect
	}
	raw, err := env.Gateway.Do(ctx, "POST", "/v1/media/image?"+q.Encode(), body)
	if err != nil {
		return refusal("generating the image", err)
	}
	var reply struct {
		DataURL       string `json:"dataUrl"`
		MimeType      string `json:"mimeType"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		RevisedPrompt string `json:"revisedPrompt"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || !strings.HasPrefix(reply.DataURL, "data:") {
		return toolError("the image provider's reply could not be read")
	}
	image, err := decodeDataURL(reply.DataURL)
	if err != nil {
		return toolError("the image provider's reply could not be read: %v", err)
	}
	name := strings.TrimSpace(in.FileName)
	if name == "" {
		name = imageFileName(in.Prompt, reply.MimeType)
	}
	artifactID, err := env.Publish(ctx, room, job, name, reply.MimeType, image)
	if err != nil {
		return toolError("publishing the image into the room failed: %v", err)
	}
	out := map[string]any{
		"artifactId": artifactID,
		"fileName":   name,
		"note":       "The image is in the room's Docs; tell the person it is there and what it shows.",
	}
	if reply.Width > 0 {
		out["width"], out["height"] = reply.Width, reply.Height
	}
	if revised := strings.TrimSpace(reply.RevisedPrompt); revised != "" {
		// What the provider wrote, not what the person said: data.
		out["revisedPrompt"] = roomlog.Fence(imageAuthor, revised)
	}
	return Result{Value: out}
}

// decodeDataURL splits a data:...;base64,<bytes> URL into its bytes.
func decodeDataURL(dataURL string) ([]byte, error) {
	_, b64, ok := strings.Cut(dataURL, ",")
	if !ok {
		return nil, fmt.Errorf("no data")
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("empty image")
	}
	return b, nil
}

// imageFileName derives a file name from the prompt: its first words and
// the image's extension.
func imageFileName(prompt, mimeType string) string {
	ext := ".png"
	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	}
	words := strings.Fields(prompt)
	if len(words) > 4 {
		words = words[:4]
	}
	name := "image"
	if len(words) > 0 {
		name = strings.Join(words, "-")
	}
	for _, r := range name {
		if r > 0x7e || r < 0x20 {
			name = "image"
			break
		}
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, strings.ToLower(name))
	if name == "" {
		name = "image"
	}
	return name + ext
}
