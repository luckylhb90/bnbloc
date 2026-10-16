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

// Generate posts a prompt to {base}/images/generations and returns the
// inline image. It asks for b64_json and, when the provider refuses that
// parameter (400), asks once without it (the assist client's
// response_format rule: some OpenAI-compatible gateways reject unknown
// parameters).
func (a *openAICompatible) Generate(ctx context.Context, p Provider, req ImageRequest) (*Image, error) {
	body := map[string]any{
		"model":  p.Model.Model,
		"prompt": req.Prompt,
		"n":      1,
	}
	if size := aspectSize(req.Aspect, p.Params); size != "" {
		body["size"] = size
	}
	for k, v := range p.Params {
		if strings.HasPrefix(k, paramImagePrefix) && !strings.HasPrefix(k, paramImageSizePrefix) {
			body[strings.TrimPrefix(k, paramImagePrefix)] = v
		}
	}
	img, err := postGenerate(ctx, p, body, true)
	if errors.Is(err, errBadRequest) { // the provider refused the parameters: retry once plainly
		img, err = postGenerate(ctx, p, body, false)
	}
	if err != nil {
		return nil, err
	}
	return img, nil
}

// postGenerate posts the generation body, asking for b64_json when asked.
func postGenerate(ctx context.Context, p Provider, body map[string]any, b64 bool) (*Image, error) {
	if b64 {
		body["response_format"] = "b64_json"
	} else {
		delete(body, "response_format")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.Model.BaseURL, "/")+"/images/generations", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
	img := reply.Data[0]
	b, err := base64.StdEncoding.DecodeString(img.B64JSON)
	if err != nil || len(b) == 0 || len(b) > wire.MaxMediaImageBytes {
		return nil, fmt.Errorf("%w: the reply's image is not readable base64 within %d bytes", ErrProviderUnreachable, wire.MaxMediaImageBytes)
	}
	mime := sniffImage(b)
	if mime == "" {
		return nil, fmt.Errorf("%w: the reply's image is not a PNG, JPEG or WebP", ErrProviderUnreachable)
	}
	return &Image{Bytes: b, MimeType: mime, RevisedPrompt: strings.TrimSpace(img.RevisedPrompt)}, nil
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
