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
	// OrgHosts are the hosts an organization's own provider may name (the
	// platform row only; none = organizations cannot set their own).
	OrgHosts []string `json:"orgHosts"`
	// Params is the provider's own knobs as the admin set them.
	Params      map[string]string `json:"params,omitempty"`
	UpdatedBy   string            `json:"updatedBy,omitempty"`
	UpdatedAtMs int64             `json:"updatedAtMs,omitempty"`
}

// MediaOrgProvider is GET /v1/tenant/media/{capability}: the organization's
// own provider (BaseURL "" = it uses the platform's) and the hosts it may
// name.
type MediaOrgProvider struct {
	Provider MediaProviderConfig `json:"provider"`
	// AllowedHosts are the platform's orgHosts; empty = an organization
	// cannot set its own provider.
	AllowedHosts []string `json:"allowedHosts"`
	// PlatformModel names the platform's model ("" = none), shown as what
	// members use when the organization sets none.
	PlatformModel string `json:"platformModel,omitempty"`
}

// MediaProviderUpdate is PUT /v1/platform/media/{capability}: an empty APIKey
// keeps the stored one, ClearKey removes it, an empty BaseURL turns the
// capability off. Kind must name a registered adapter.
type MediaProviderUpdate struct {
	Kind     string `json:"kind"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey,omitempty"`
	ClearKey bool   `json:"clearKey,omitempty"`
	// OrgHosts is the platform's to set (the hosts organizations may name);
	// an update without it keeps the stored ones.
	OrgHosts []string          `json:"orgHosts,omitempty"`
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
	// Count is how many images to draw in one provider call (1…MaxMediaImages,
	// default 1): one request's cost, not n.
	Count int `json:"count,omitempty"`
	// Image, when set, is the image to change (an edit) instead of drawing a
	// new one: a data URL the caller holds (a previous reply's, an
	// attachment's bytes the caller may read). At most MaxMediaImageBytes.
	Image string `json:"image,omitempty"`
	// Publish, when true under a job grant (?job=&room=), publishes each
	// drawn image into the room's Docs as a deliverable of the job: the
	// reply carries each image's artifactId. A member's or an app's call
	// never publishes.
	Publish bool `json:"publish,omitempty"`
	// FileName, with Publish, names the first image (poster.png); the rest
	// are numbered (poster-2.png…).
	FileName string `json:"fileName,omitempty"`
}

// MediaImageReply is POST /v1/media/image's answer: the images inline
// (dataUrl), never a link out to another host. One answer per Count ask.
type MediaImageReply struct {
	Images []MediaImage `json:"images"`
}

// MediaImage is one generated (or edited) image.
type MediaImage struct {
	DataURL string `json:"dataUrl,omitempty"`
	// ArtifactID, when the call published, is the room artifact the image
	// became (and DataURL is empty: the image is not also inlined).
	ArtifactID string `json:"artifactId,omitempty"`
	MimeType   string `json:"mimeType"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	// RevisedPrompt is the provider's own rewording, when it sends one: data
	// the provider wrote, fenced wherever an agent reads it.
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
}
