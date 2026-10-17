package wire

import "encoding/json"

// Decision models (docs/prd/2026-10-17-decision-models.md): a model that
// answers bounded questions about an input with calibrated numbers, through
// an admin-configured provider (internal/decision). Two request formats,
// one call: POST /v1/decisions takes the OpenAI Decisions format (ordered
// lists) and POST /v1/systemone the SystemOne format (maps keyed by name),
// each answered in its own format, so a caller written for either needs
// only the gateway's address and session.

// Decision question types.
//
//wire:union DecisionQuestionType
const (
	// DecisionPredicate: a yes/no question; the answer is the probability
	// of yes.
	DecisionPredicate = "predicate"
	// DecisionChoice: one of Choices; the answer is the choice, its
	// confidence and a probability per choice.
	DecisionChoice = "choice"
	// DecisionScore: a level of Levels (lowest first); the answer is a level
	// index (fractional), its confidence and a probability per level.
	DecisionScore = "score"
)

// DecisionRefusal is an answer's type when the model declined that one
// question (its other answers stand).
const DecisionRefusal = "refusal"

// Decision bounds (the provider's own, so a refusal is ours and says why).
const (
	MaxDecisionQuestions    = 128
	MaxDecisionChoices      = 255
	MaxDecisionLevels       = 10
	MaxDecisionInputText    = 64 << 10 // all text parts of the input
	MaxDecisionImages       = 8
	MaxDecisionImageBytes   = 4 << 20 // one decoded image
	MaxDecisionInstructions = 4 << 10
	MaxDecisionLabel        = 512 // a name, a choice value or a level label
	MaxDecisionDescription  = 2 << 10
)

// DecisionRequest is POST /v1/decisions: Input (Text, or Messages) and the
// questions. Answers come back in question order.
type DecisionRequest struct {
	// Model is one of the provider's models (the first is the default; an
	// unknown name falls back to it).
	Model string `json:"model,omitempty"`
	// Input is the thing to judge: a JSON string, or a list of
	// DecisionMessage (user messages whose parts are input_text /
	// input_image, a data URL). internal/decision.ParseInput reads it.
	Input json.RawMessage `json:"input"`
	// Questions are 1…MaxDecisionQuestions questions; names, when set, are
	// unique and come back in the answers.
	Questions []DecisionQuestion `json:"questions"`
}

// DecisionMessage is one user message of an input.
type DecisionMessage struct {
	Role    string         `json:"role"`
	Content []DecisionPart `json:"content"`
}

// DecisionPart is one part of a message: input_text (Text) or input_image
// (ImageURL, a data URL of a PNG, JPEG, WebP or GIF image).
type DecisionPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// DecisionQuestion is one question.
type DecisionQuestion struct {
	Type         string `json:"type" tstype:"DecisionQuestionType"`
	Name         string `json:"name,omitempty"`
	Instructions string `json:"instructions"`
	// Choices, for a choice question: 2…MaxDecisionChoices values.
	Choices []DecisionChoiceOption `json:"choices,omitempty"`
	// Levels, for a score question: 2…MaxDecisionLevels, lowest first.
	Levels []DecisionLevel `json:"levels,omitempty"`
}

// DecisionChoiceOption is one value a choice question may answer.
type DecisionChoiceOption struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// DecisionLevel is one level of a score question.
type DecisionLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// DecisionReply is POST /v1/decisions' answer: one answer per question, in
// question order, and the provider's usage.
type DecisionReply struct {
	// Model is the model the provider says answered.
	Model   string           `json:"model"`
	Answers []DecisionAnswer `json:"answers"`
	Usage   DecisionUsage    `json:"usage"`
}

// DecisionAnswer is one question's answer: its question's type, or
// DecisionRefusal. Probability is a predicate's (the probability of yes);
// Choice a choice's; Score a score's (a level index, possibly fractional).
// Probabilities is per choice value or per level, Confidence the model's
// for the choice or the score.
type DecisionAnswer struct {
	Type          string                `json:"type"`
	Name          string                `json:"name,omitempty"`
	Probability   *float64              `json:"probability,omitempty"`
	Choice        string                `json:"choice,omitempty"`
	Score         *float64              `json:"score,omitempty"`
	Confidence    *float64              `json:"confidence,omitempty"`
	Probabilities []DecisionProbability `json:"probabilities,omitempty"`
}

// DecisionProbability is the probability of one choice value (Value, a
// string) or one level (Value, its index, and Label).
type DecisionProbability struct {
	Value       any     `json:"value"`
	Label       string  `json:"label,omitempty"`
	Probability float64 `json:"probability"`
}

// DecisionUsage is the provider's token count for the call (the upstream
// formats' own field names).
type DecisionUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// SystemOneRequest is POST /v1/systemone: State is the thing to judge (a
// string, or any JSON value, judged as its JSON text); Questions is a map
// of question name to SystemOneQuestion, read in its written order
// (internal/decision.FromSystemOne).
type SystemOneRequest struct {
	Model     string          `json:"model,omitempty"`
	State     json.RawMessage `json:"state"`
	Questions json.RawMessage `json:"questions"`
}

// SystemOne question types: SystemOneNoul is the yes/no question (a
// DecisionPredicate); choice and score are named as in the list format.
const SystemOneNoul = "noul"

// SystemOneReply is POST /v1/systemone's answer: one answer per question,
// keyed by its name.
type SystemOneReply struct {
	Model   string                     `json:"model"`
	Answers map[string]SystemOneAnswer `json:"answers"`
	Usage   DecisionUsage              `json:"usage"`
}

// SystemOneAnswer is one answer: noul (Noul, the probability of yes),
// choice (Choice, Confidence, Probabilities per label), score (Score,
// Confidence, Legend and Probabilities per level index), or refusal.
type SystemOneAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// DecisionCapability is GET /v1/decisions: whether decisions run for the
// caller, and the models they may pick from (the first the default).
type DecisionCapability struct {
	Available bool     `json:"available"`
	Models    []string `json:"models,omitempty"`
}
