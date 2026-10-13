package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/sse"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The client of the assist model: OpenAI-compatible chat completions
// (POST <base>/chat/completions). A text task streams (stream: true, the
// usage in the last chunk); a JSON task asks for a JSON object
// (response_format), and again without it when the model refuses that
// parameter. Every request reaches only the configured host
// (extgov.NetGuard: also no link-local or metadata address) within
// Timeout.

// Timeout bounds one call, the stream included.
const Timeout = 60 * time.Second

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client calls the assist model.
type Client struct {
	// Transport, when set, replaces the guarded transport (tests); the
	// host guard applies either way.
	Transport http.RoundTripper
	Now       func() time.Time
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	MaxTokens      int            `json:"max_tokens"`
	Temperature    float64        `json:"temperature"`
	Stream         bool           `json:"stream,omitempty"`
	StreamOptions  *streamOptions `json:"stream_options,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// http is a client held to m's host.
func (c *Client) http(m Model) (*http.Client, error) {
	u, err := url.Parse(m.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("the assist model's base URL is invalid")
	}
	var base http.RoundTripper
	if c != nil {
		base = c.Transport
	}
	return &http.Client{
		Transport: extgov.NewNetGuard([]string{u.Host}).RoundTripper(base),
		// A model never redirects a completion; following one could leave
		// the host (the guard would refuse it anyway).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func (c *Client) post(ctx context.Context, m Model, body chatRequest) (*http.Response, error) {
	hc, err := c.http(m)
	if err != nil {
		return nil, err
	}
	body.Model = m.Model
	if body.MaxTokens == 0 {
		body.MaxTokens = MaxReplyTokens
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if body.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if m.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("the assist model did not answer within %s", Timeout)
		}
		return nil, fmt.Errorf("reach the assist model: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, modelError(resp)
	}
	return resp, nil
}

// modelError reads a refusal: the provider's error message, bounded.
func modelError(resp *http.Response) *ModelError {
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
	return &ModelError{Status: resp.StatusCode, Message: msg}
}

func usageOf(m Model, u *chatUsage, start, end time.Time) wire.AssistUsage {
	out := wire.AssistUsage{Model: m.Model, LatencyMs: end.Sub(start).Milliseconds()}
	if u != nil {
		out.PromptTokens, out.CompletionTokens = u.PromptTokens, u.CompletionTokens
	}
	return out
}

// Stream runs a text task, handing each piece of the reply to onDelta as
// it arrives (an error from onDelta stops the call: the caller went away).
func (c *Client) Stream(ctx context.Context, m Model, msgs []Message, onDelta func(string) error) (wire.AssistUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	start := c.now()
	resp, err := c.post(ctx, m, chatRequest{Messages: msgs, Temperature: 0.3, Stream: true, StreamOptions: &streamOptions{IncludeUsage: true}})
	if err != nil {
		return wire.AssistUsage{}, err
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A proxy that ignores stream answers one completion.
		var cr chatResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxReplyBytes)).Decode(&cr); err != nil {
			return wire.AssistUsage{}, fmt.Errorf("the assist model's answer is unreadable: %w", err)
		}
		if len(cr.Choices) > 0 && cr.Choices[0].Message.Content != "" {
			if err := onDelta(cr.Choices[0].Message.Content); err != nil {
				return wire.AssistUsage{}, err
			}
		}
		return usageOf(m, cr.Usage, start, c.now()), nil
	}
	var usage *chatUsage
	r := sse.NewReader(io.LimitReader(resp.Body, maxReplyBytes))
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return wire.AssistUsage{}, fmt.Errorf("the assist model did not finish within %s", Timeout)
			}
			return wire.AssistUsage{}, fmt.Errorf("read the assist model's stream: %w", err)
		}
		if strings.TrimSpace(string(ev.Data)) == "[DONE]" {
			break
		}
		var chunk chatResponse
		if err := json.Unmarshal(ev.Data, &chunk); err != nil {
			return wire.AssistUsage{}, fmt.Errorf("the assist model's stream is unreadable: %w", err)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			if err := onDelta(ch.Delta.Content); err != nil {
				return wire.AssistUsage{}, err
			}
		}
	}
	return usageOf(m, usage, start, c.now()), nil
}

// Complete runs a JSON task and returns the reply's text. It asks for a
// JSON object, and asks again without response_format when the model
// refuses the request (not every model behind a proxy supports it).
func (c *Client) Complete(ctx context.Context, m Model, msgs []Message, jsonObject bool) (string, wire.AssistUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	start := c.now()
	req := chatRequest{Messages: msgs, Temperature: 0.2}
	if jsonObject {
		req.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}
	resp, err := c.post(ctx, m, req)
	var me *ModelError
	if errors.As(err, &me) && me.Status == http.StatusBadRequest && req.ResponseFormat != nil {
		req.ResponseFormat = nil
		resp, err = c.post(ctx, m, req)
	}
	if err != nil {
		return "", wire.AssistUsage{}, err
	}
	defer resp.Body.Close()
	var cr chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReplyBytes)).Decode(&cr); err != nil {
		return "", wire.AssistUsage{}, fmt.Errorf("the assist model's answer is unreadable: %w", err)
	}
	text := ""
	if len(cr.Choices) > 0 {
		text = cr.Choices[0].Message.Content
	}
	return text, usageOf(m, cr.Usage, start, c.now()), nil
}
