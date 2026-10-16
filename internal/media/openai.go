package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// openAICompatible is the OpenAI-compatible wire family: the shapes
// POST {base}/audio/transcriptions (multipart, a `text` reply) and POST
// {base}/images/generations (JSON, an inline b64_json image) answer with.
// Providers that follow these shapes configure kind "openai-compatible";
// their knobs (a size preset name, a language hint) arrive as params.
type openAICompatible struct{}

func (*openAICompatible) Kind() string { return AdapterKindOpenAICompatible }

// Serves: the OpenAI-compatible family has both shapes.
func (*openAICompatible) Serves(cap Capability) bool { return cap == CapTranscribe || cap == CapImage }

// maxReplyBody bounds what is read of one provider answer.
const maxReplyBody = wire.MaxMediaImageReplyBytes

// Transcribe posts the audio as multipart to {base}/audio/transcriptions
// and returns the reply's text.
func (a *openAICompatible) Transcribe(ctx context.Context, p Provider, audio []byte, mimeType, filename string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if filename == "" {
		filename = "audio"
	}
	if err := mw.WriteField("model", p.Model.Model); err != nil {
		return "", err
	}
	for k, v := range p.Params {
		if strings.HasPrefix(k, paramTranscribePrefix) {
			if err := mw.WriteField(strings.TrimPrefix(k, paramTranscribePrefix), v); err != nil {
				return "", err
			}
		}
	}
	fh := textproto.MIMEHeader{}
	fh.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	fh.Set("Content-Type", mimeType)
	part, err := mw.CreatePart(fh)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.Model.BaseURL, "/")+"/audio/transcriptions", bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if p.Model.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.Model.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", providerError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	var reply struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || strings.TrimSpace(reply.Text) == "" && !bytes.Contains(raw, []byte(`"text"`)) {
		return "", fmt.Errorf("%w: the reply is not a transcription object", ErrProviderUnreachable)
	}
	return capText(reply.Text, wire.MaxMediaTranscriptChars), nil
}

// Generate draws (or, with Input set, edits) Count images in ONE provider
// call — one request's cost and latency, not n. A generation posts JSON to
// {base}/images/generations; an edit posts multipart to
// {base}/images/edits with the input image as the "image" part. Both ask
// for b64_json and, when the provider refuses that parameter (400), ask
// once without it (the assist client's response_format rule: some
// OpenAI-compatible gateways reject unknown parameters).
func (a *openAICompatible) Generate(ctx context.Context, p Provider, req ImageRequest) ([]Image, error) {
	n := req.Count
	if n < 1 {
		n = 1
	}
	body := map[string]any{
		"model":  p.Model.Model,
		"prompt": req.Prompt,
		"n":      n,
	}
	if size := aspectSize(req.Aspect, p.Params); size != "" {
		body["size"] = size
	}
	for k, v := range p.Params {
		if strings.HasPrefix(k, paramImagePrefix) && !strings.HasPrefix(k, paramImageSizePrefix) {
			body[strings.TrimPrefix(k, paramImagePrefix)] = v
		}
	}
	imgs, err := postImages(ctx, p, body, req.Input, true)
	if errors.Is(err, errBadRequest) { // the provider refused the parameters: retry once plainly
		imgs, err = postImages(ctx, p, body, req.Input, false)
	}
	if err != nil {
		return nil, err
	}
	return imgs, nil
}

// postImages posts the generation (or edit) body, asking for b64_json when
// asked, and decodes every inline image of the reply.
func postImages(ctx context.Context, p Provider, body map[string]any, input *ImageInput, b64 bool) ([]Image, error) {
	if b64 {
		body["response_format"] = "b64_json"
	} else {
		delete(body, "response_format")
	}
	var req *http.Request
	var err error
	if input != nil {
		// An edit: the model, the prompt and the input image as parts. The
		// body's fields ride along as parts too; the extra JSON-only knobs
		// (size, n) stay fields.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range body {
			switch v.(type) {
			case string:
				_ = mw.WriteField(k, v.(string))
			default:
				if raw, merr := json.Marshal(v); merr == nil {
					_ = mw.WriteField(k, string(raw))
				}
			}
		}
		fh := textproto.MIMEHeader{}
		fh.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "image", "filename": "image"}))
		fh.Set("Content-Type", input.MimeType)
		part, werr := mw.CreatePart(fh)
		if werr != nil {
			return nil, werr
		}
		if _, werr = part.Write(input.Bytes); werr != nil {
			return nil, werr
		}
		if werr = mw.Close(); werr != nil {
			return nil, werr
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(p.Model.BaseURL, "/")+"/images/edits", bytes.NewReader(buf.Bytes()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
	} else {
		raw, merr := json.Marshal(body)
		if merr != nil {
			return nil, merr
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(p.Model.BaseURL, "/")+"/images/generations", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	}
	if p.Model.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.Model.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		return nil, errBadRequest
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, providerError(resp)
	}
	rraw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBody))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
	}
	var reply struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rraw, &reply); err != nil || len(reply.Data) == 0 || reply.Data[0].B64JSON == "" {
		return nil, fmt.Errorf("%w: the reply holds no inline image (b64_json)", ErrProviderUnreachable)
	}
	out := make([]Image, 0, len(reply.Data))
	for _, img := range reply.Data {
		if img.B64JSON == "" {
			break // a provider mixing inline and linked images: stop at the first link
		}
		b, err := base64.StdEncoding.DecodeString(img.B64JSON)
		if err != nil || len(b) == 0 || len(b) > wire.MaxMediaImageBytes {
			return nil, fmt.Errorf("%w: the reply's image is not readable base64 within %d bytes", ErrProviderUnreachable, wire.MaxMediaImageBytes)
		}
		mime := sniffImage(b)
		if mime == "" {
			return nil, fmt.Errorf("%w: the reply's image is not a PNG, JPEG or WebP", ErrProviderUnreachable)
		}
		out = append(out, Image{Bytes: b, MimeType: mime, RevisedPrompt: strings.TrimSpace(img.RevisedPrompt)})
		if len(out) >= wire.MaxMediaImages {
			break // a provider answering more than the cap is cut, not failed
		}
	}
	return out, nil
}

// errBadRequest: the provider refused the parameters (400); the caller
// retries once without the optional ones.
var errBadRequest = errors.New("the provider refused the parameters")

// paramTranscribePrefix / paramImagePrefix / paramImageSizePrefix are the
// params namespaces: a transcription's extra form fields, an image
// generation's extra body fields, and its size presets per aspect.
const (
	paramTranscribePrefix = "transcribe."
	paramImagePrefix      = "image."
	paramImageSizePrefix  = "image.size."
)

// aspectSize resolves the request's aspect to a provider size value: the
// params' preset for that aspect (image.size.square…), else the family's
// default presets.
func aspectSize(aspect string, params map[string]string) string {
	if aspect == "" {
		aspect = wire.MediaImageAspectSquare
	}
	if v, ok := params[paramImageSizePrefix+aspect]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	switch aspect {
	case wire.MediaImageAspectLandscape:
		return "1536x1024"
	case wire.MediaImageAspectPortrait:
		return "1024x1536"
	default:
		return "1024x1024"
	}
}

// capText truncates text to at most max characters without splitting a
// UTF-16 surrogate pair (the byte at the cut is a leading surrogate).
func capText(text string, max int) string {
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	cut := r[:max]
	// A leading D800-DBFF surrogate at the cut would orphan its trail.
	if last := cut[len(cut)-1]; last >= 0xD800 && last <= 0xDBFF {
		cut = cut[:len(cut)-1]
	}
	return string(cut)
}

// sniffImage reports the image's MIME type from its magic bytes (PNG, JPEG
// or WebP), "" when it is none of them.
func sniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// providerError reads a refusal: the provider's error message, bounded.
func providerError(resp *http.Response) *ProviderError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var e struct {
		Error any `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			msg, _ = v["message"].(string)
		}
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return &ProviderError{Status: resp.StatusCode, Message: msg}
}
