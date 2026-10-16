package wire

// Media model calls (docs/prd/2026-10-16-voice-and-image-generation.md):
// speech-to-text and image generation through admin-configured providers
// (internal/media). A provider is configuration (kind selects the wire
// protocol adapter), never a hardcoded vendor.

// MediaCapabilities is GET /v1/media: which media calls run for the caller
// (a configured provider and an organization that has not turned AI off).
type MediaCapabilities struct {
	Transcribe MediaCapability `json:"transcribe"`
	Image      MediaCapability `json:"image"`
}

// MediaCapability is one media ability as the caller sees it.
type MediaCapability struct {
	// Available: a provider is configured for the caller's organization.
	Available bool `json:"available"`
}

// MediaProviderConfig is GET /v1/platform/media/{capability}: the
// capability's provider. The key is never returned, only whether one is set.
// kind names the wire protocol adapter (media.AdapterKind*).
type MediaProviderConfig struct {
	Configurable bool   `json:"configurable"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"baseUrl"`
	Model        string `json:"model"`
	KeySet       bool   `json:"keySet"`
	// Params is the provider's own knobs as the admin set them.
	Params      map[string]string `json:"params,omitempty"`
	UpdatedBy   string            `json:"updatedBy,omitempty"`
	UpdatedAtMs int64             `json:"updatedAtMs,omitempty"`
}

// MediaProviderUpdate is PUT /v1/platform/media/{capability}: an empty APIKey
// keeps the stored one, ClearKey removes it, an empty BaseURL turns the
// capability off. Kind must name a registered adapter.
type MediaProviderUpdate struct {
	Kind     string            `json:"kind"`
	BaseURL  string            `json:"baseUrl"`
	Model    string            `json:"model"`
	APIKey   string            `json:"apiKey,omitempty"`
	ClearKey bool              `json:"clearKey,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
}

// MediaProviderTest is POST /v1/platform/media/{capability}/test: one real
// short call through the stored provider.
type MediaProviderTest struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Reply is what the provider answered, bounded (a transcript line, or
	// nothing for an image test).
	Reply string `json:"reply,omitempty"`
	// LatencyMs is the call's wall time.
	LatencyMs int64 `json:"latencyMs"`
}

// MediaTranscribeReply is POST /v1/media/transcriptions: the transcript,
// capped at MaxMediaTranscriptChars characters.
type MediaTranscribeReply struct {
	Text string `json:"text"`
}

// Image aspect ratios a generation may ask for.
//
//wire:union MediaImageAspect
const (
	MediaImageAspectSquare    = "square"
	MediaImageAspectLandscape = "landscape"
	MediaImageAspectPortrait  = "portrait"
)

// MediaImageAspects are the aspects a generation may ask for.
var MediaImageAspects = []string{MediaImageAspectSquare, MediaImageAspectLandscape, MediaImageAspectPortrait}

// MediaImageRequest is POST /v1/media/image.
type MediaImageRequest struct {
	// Prompt is what to draw, at most MaxMediaImagePrompt characters.
	Prompt string `json:"prompt"`
	// Aspect defaults to MediaImageAspectSquare.
	Aspect string `json:"aspect,omitempty" tstype:"MediaImageAspect"`
}

// MediaImageReply is POST /v1/media/image's answer: the image inline
// (dataUrl), never a link out to another host.
type MediaImageReply struct {
	DataURL  string `json:"dataUrl"`
	MimeType string `json:"mimeType"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	// RevisedPrompt is the provider's own rewording, when it sends one: data
	// the provider wrote, fenced wherever an agent reads it.
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
}
