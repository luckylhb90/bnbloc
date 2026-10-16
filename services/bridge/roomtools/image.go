package roomtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
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
	Count    int    `json:"count"`
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
	if in.Count > 1 {
		body["count"] = in.Count
	}
	raw, err := env.Gateway.Do(ctx, "POST", "/v1/media/image?"+q.Encode(), body)
	if err != nil {
		return refusal("generating the image", err)
	}
	var reply struct {
		Images []struct {
			DataURL       string `json:"dataUrl"`
			MimeType      string `json:"mimeType"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			RevisedPrompt string `json:"revisedPrompt"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || len(reply.Images) == 0 {
		return toolError("the image provider's reply could not be read")
	}
	// Every image of the batch publishes under its own name; the first keeps
	// the plain name, the rest are numbered (poster.png, poster-2.png…).
	base := strings.TrimSpace(in.FileName)
	published := make([]map[string]any, 0, len(reply.Images))
	for i, img := range reply.Images {
		image, err := decodeDataURL(img.DataURL)
		if err != nil {
			return toolError("the image provider's reply could not be read: %v", err)
		}
		name := base
		if name == "" {
			name = imageFileName(in.Prompt, img.MimeType)
			base = name
		}
		if i > 0 {
			name = numberedFile(base, i+1)
		}
		artifactID, err := env.Publish(ctx, room, job, name, img.MimeType, image)
		if err != nil {
			return toolError("publishing %s into the room failed: %v", name, err)
		}
		row := map[string]any{"artifactId": artifactID, "fileName": name}
		if img.Width > 0 {
			row["width"], row["height"] = img.Width, img.Height
		}
		if revised := strings.TrimSpace(img.RevisedPrompt); revised != "" {
			// What the provider wrote, not what the person said: data.
			row["revisedPrompt"] = roomlog.Fence(imageAuthor, revised)
		}
		published = append(published, row)
	}
	return Result{Value: map[string]any{
		"images": published,
		"note":   "The images are in the room's Docs; tell the person they are there and what they show.",
	}}
}

// numberedFile names the n-th image of a batch: the base with -n before
// the extension (poster.png, poster-3.png).
func numberedFile(base string, n int) string {
	ext := path.Ext(base)
	if ext == "" {
		ext = ".png"
	}
	return strings.TrimSuffix(base, ext) + "-" + strconv.Itoa(n) + ext
}

// extOf is a MIME type's file extension.
func extOf(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	}
	return ".png"
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
