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
	// Models are the provider's models the caller may pick from (the first
	// the default); empty when the provider serves one.
	Models []string `json:"models,omitempty"`
}

// MediaProviderConfig is GET /v1/platform/media/{capability}: the
// capability's providers. The key is never returned, only whether one is
// set. kind names the wire protocol adapter (media.AdapterKind*); auth is
// "key" (a sealed deployment key) or "caller" (each caller's own session
// token is forwarded — a proxy that bills the caller, like the platform's
// own LLM gateway).
type MediaProviderConfig struct {
	Configurable bool   `json:"configurable"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"baseUrl"`
	Model        string `json:"model"`
	// Models are the models a call may name (the provider serves them all);
	// Model is the first of them, the default.
	Models []string `json:"models,omitempty"`
	Auth   string   `json:"auth"`
	KeySet bool     `json:"keySet"`
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
	Kind    string `json:"kind"`
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	// Models are the models a call may name; Model is the first of them.
	Models []string `json:"models,omitempty"`
	// Auth is "key" (default: a sealed API key) or "caller" (forward the
	// caller's own session token).
	Auth     string `json:"auth,omitempty"`
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
	// Model is one of the provider's models (MediaProviderConfig.Models);
	// the first is the default.
	Model string `json:"model,omitempty"`
	// Aspect defaults to MediaImageAspectSquare.
	Aspect string `json:"aspect,omitempty" tstype:"MediaImageAspect"`
	// Count is how many images to draw in one provider call (1…MaxMediaImages,
	// default 1): one request's cost, not n.
	Count int `json:"count,omitempty"`
	// Image, when set, is the image to change (an edit) instead of drawing a
	// new one: a data URL the caller holds (a previous reply's, an
	// attachment's bytes the caller may read). At most MaxMediaImageBytes.
	Image string `json:"image,omitempty"`
	// Publish, when true in a room (an agent's job grant, ?job=&room=, or a
	// room app's page), publishes each drawn image into the room's Docs: the
	// reply carries each image's artifactId. A member's own call (no room)
	// never publishes.
	Publish bool `json:"publish,omitempty"`
	// FileName, with Publish, names the first image (poster.png); the rest
	// are numbered (poster-2.png…).
	FileName string `json:"fileName,omitempty"`
	// ArtifactID, in a room (as Publish), is the room image to edit: it is
	// read as the caller (a job's requester, an app's participant), and the
	// result is published as its next version
	// (docs/prd/2026-10-17-image-generation-redesign.md §2.2).
	ArtifactID string `json:"artifactId,omitempty"`
	// Region, with an edit, is the part of the image to change, in its own
	// pixels: the provider gets a mask.
	Region *MediaImageRegion `json:"region,omitempty"`
	// Inputs, in a room, are room files (art_…) and attachments (att_…) the
	// prompt uses as references, at most MaxMediaImageInputs, read as the
	// caller. Without ArtifactID the first is the image to change.
	Inputs []string `json:"inputs,omitempty"`
}

// MediaImageRegion is a rectangle of an image in its own pixels, origin at
// the top-left.
type MediaImageRegion struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
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
	// became (and DataURL is empty: the image is not also inlined); Version
	// its version (above 1 for an edit), FileName its name, and Note what
	// happened when it is not what was asked (an edit saved as a new file).
	ArtifactID string `json:"artifactId,omitempty"`
	Version    int    `json:"version,omitempty"`
	FileName   string `json:"fileName,omitempty"`
	Note       string `json:"note,omitempty"`
	MimeType   string `json:"mimeType"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	// RevisedPrompt is the provider's own rewording, when it sends one: data
	// the provider wrote, fenced wherever an agent reads it.
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
}
