package decision

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/luckylhb90/bnbloc/component"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Request is a validated decision ask: the input as text or messages, and
// the questions.
type Request struct {
	Model     string
	Text      string
	Messages  []wire.DecisionMessage
	Questions []wire.DecisionQuestion
}

// Images is how many images the input carries.
func (r Request) Images() int {
	n := 0
	for _, m := range r.Messages {
		for _, p := range m.Content {
			if p.Type == partImage {
				n++
			}
		}
	}
	return n
}

// Message part types.
const (
	partText  = "input_text"
	partImage = "input_image"
)

// InvalidError is a refused request: its message names the field.
type InvalidError struct{ msg string }

func (e InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return InvalidError{msg: fmt.Sprintf(format, args...)}
}

// Validate is the one rule for a decision request (every caller: a
// member, an agent through the room relay, an app page): the input, the
// questions and every bound, checked before a provider is called.
func Validate(in wire.DecisionRequest) (Request, error) {
	out := Request{Model: strings.TrimSpace(in.Model)}
	if err := parseInput(in.Input, &out); err != nil {
		return Request{}, err
	}
	n := len(in.Questions)
	if n == 0 || n > wire.MaxDecisionQuestions {
		return Request{}, invalid("questions: 1 to %d questions", wire.MaxDecisionQuestions)
	}
	names := map[string]bool{}
	for i, q := range in.Questions {
		at := fmt.Sprintf("questions[%d]", i)
		q.Name = strings.TrimSpace(q.Name)
		q.Instructions = strings.TrimSpace(q.Instructions)
		if q.Name != "" {
			if err := text(at+".name", q.Name, wire.MaxDecisionLabel); err != nil {
				return Request{}, err
			}
			if names[q.Name] {
				return Request{}, invalid("%s.name %q is used twice", at, q.Name)
			}
			names[q.Name] = true
		}
		if q.Instructions == "" {
			return Request{}, invalid("%s.instructions: the question is required", at)
		}
		if err := text(at+".instructions", q.Instructions, wire.MaxDecisionInstructions); err != nil {
			return Request{}, err
		}
		switch q.Type {
		case wire.DecisionPredicate:
			if len(q.Choices) > 0 || len(q.Levels) > 0 {
				return Request{}, invalid("%s: a predicate takes no choices or levels", at)
			}
		case wire.DecisionChoice:
			if len(q.Choices) < component.MinDecisionChoices || len(q.Choices) > component.MaxDecisionChoices || len(q.Levels) > 0 {
				return Request{}, invalid("%s.choices: %d to %d choices", at, component.MinDecisionChoices, component.MaxDecisionChoices)
			}
			values := map[string]bool{}
			for j, c := range q.Choices {
				if err := text(fmt.Sprintf("%s.choices[%d].value", at, j), c.Value, wire.MaxDecisionLabel); err != nil || strings.TrimSpace(c.Value) == "" {
					return Request{}, invalid("%s.choices[%d].value: required, at most %d characters", at, j, wire.MaxDecisionLabel)
				}
				if values[c.Value] {
					return Request{}, invalid("%s.choices: %q is listed twice", at, c.Value)
				}
				values[c.Value] = true
				if err := text(fmt.Sprintf("%s.choices[%d].description", at, j), c.Description, wire.MaxDecisionDescription); err != nil {
					return Request{}, err
				}
			}
		case wire.DecisionScore:
			if len(q.Levels) < 2 || len(q.Levels) > wire.MaxDecisionLevels || len(q.Choices) > 0 {
				return Request{}, invalid("%s.levels: 2 to %d levels, lowest first", at, wire.MaxDecisionLevels)
			}
			for j, l := range q.Levels {
				if err := text(fmt.Sprintf("%s.levels[%d].label", at, j), l.Label, wire.MaxDecisionLabel); err != nil || strings.TrimSpace(l.Label) == "" {
					return Request{}, invalid("%s.levels[%d].label: required, at most %d characters", at, j, wire.MaxDecisionLabel)
				}
				if err := text(fmt.Sprintf("%s.levels[%d].description", at, j), l.Description, wire.MaxDecisionDescription); err != nil {
					return Request{}, err
				}
			}
		default:
			return Request{}, invalid("%s.type must be %q, %q or %q", at, wire.DecisionPredicate, wire.DecisionChoice, wire.DecisionScore)
		}
		out.Questions = append(out.Questions, q)
	}
	return out, nil
}

// parseInput reads the input: a JSON string, or a list of user messages of
// input_text and input_image (data URL) parts.
func parseInput(raw json.RawMessage, out *Request) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return invalid("input: the thing to judge is required")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return invalid("input: not a JSON string")
		}
		if strings.TrimSpace(s) == "" {
			return invalid("input: the thing to judge is required")
		}
		out.Text = s
		return text("input", s, wire.MaxDecisionInputText)
	}
	var msgs []wire.DecisionMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return invalid("input: a string or a list of user messages")
	}
	if len(msgs) == 0 {
		return invalid("input: at least one message")
	}
	textBytes, images := 0, 0
	for i, m := range msgs {
		if m.Role != "user" {
			return invalid("input[%d].role must be \"user\"", i)
		}
		if len(m.Content) == 0 {
			return invalid("input[%d].content: at least one part", i)
		}
		for j, p := range m.Content {
			at := fmt.Sprintf("input[%d].content[%d]", i, j)
			switch p.Type {
			case partText:
				if p.ImageURL != "" || !utf8.ValidString(p.Text) {
					return invalid("%s: an input_text part carries text only", at)
				}
				textBytes += len(p.Text)
			case partImage:
				images++
				if p.Text != "" || images > wire.MaxDecisionImages {
					return invalid("%s: at most %d images, each an input_image with image_url", at, wire.MaxDecisionImages)
				}
				if err := imageDataURL(at+".image_url", p.ImageURL); err != nil {
					return err
				}
			default:
				return invalid("%s.type must be %q or %q", at, partText, partImage)
			}
		}
	}
	if textBytes > wire.MaxDecisionInputText {
		return invalid("input: at most %d bytes of text", wire.MaxDecisionInputText)
	}
	out.Messages = msgs
	return nil
}

// imageDataURL checks an inline image: a data URL of a PNG, JPEG, WebP or
// GIF of at most MaxDecisionImageBytes. The gateway never fetches a URL.
func imageDataURL(at, s string) error {
	head, b64, ok := strings.Cut(s, ",")
	if !ok || !strings.HasPrefix(head, "data:image/") || !strings.HasSuffix(head, ";base64") {
		return invalid("%s: a data URL of an image (data:image/…;base64,…); links are not fetched", at)
	}
	if base64.StdEncoding.DecodedLen(len(b64)) > wire.MaxDecisionImageBytes+3 {
		return invalid("%s: at most %d bytes", at, wire.MaxDecisionImageBytes)
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) == 0 || len(b) > wire.MaxDecisionImageBytes {
		return invalid("%s: not readable base64 of at most %d bytes", at, wire.MaxDecisionImageBytes)
	}
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n",
		len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF,
		len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP",
		len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return nil
	}
	return invalid("%s: a PNG, JPEG, WebP or GIF image", at)
}

// text checks one bounded text field.
func text(at, s string, max int) error {
	if !utf8.ValidString(s) || len(s) > max {
		return invalid("%s: text of at most %d bytes", at, max)
	}
	return nil
}
