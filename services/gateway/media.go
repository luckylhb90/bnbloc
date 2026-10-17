package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/media"
	"github.com/luckylhb90/bnbloc/internal/modelsettings"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Media model calls (docs/prd/2026-10-16-voice-and-image-generation.md):
// speech to text for the composer and image generation for agents, through
// admin-configured providers (internal/media). internal/media owns the
// provider call; this file admits the caller and relays the reply, exactly
// as the assist routes do (the rate, the budget, the audit kind).
//
//	GET  /v1/media                          which media calls run for the caller
//	POST /v1/media/transcriptions           one transcription (raw audio body)
//	POST /v1/media/image                    one image generation
//	GET  /v1/platform/media/{capability}    the capability's provider (never its key)
//	PUT  /v1/platform/media/{capability}    set it (super admin)
//	POST /v1/platform/media/{capability}/test   one real short call through it

// mediaSettings is the media providers' settings.
func (s *Service) mediaSettings() *media.Settings {
	return &media.Settings{Store: s.PlatformSettings, Box: s.SecretBox, Now: func() int64 { return s.nowTime().UnixMilli() }}
}

// mediaProvider resolves the capability's provider for claims'
// organization: its own while set and allowed (media.Settings.Provider), a
// configured provider, and an organization that has not turned AI off (the
// assist switch's meaning: no data leaving to a model). The caller's
// Authorization header is pinned for an auth-"caller" provider (the
// provider bills the caller, like the platform's own LLM gateway); the
// header value never reaches a key-auth provider.
func (s *Service) mediaProvider(ctx context.Context, claims auth.Claims, cap media.Capability, callerAuth string) (media.Provider, bool, error) {
	if s.PlatformSettings == nil {
		return media.Provider{}, false, nil
	}
	t, err := s.tenantSettings(ctx, claims.Tenant)
	if err != nil && !errors.Is(err, store.ErrTenantNotFound) {
		return media.Provider{}, false, err
	}
	if t.AssistDisabled {
		return media.Provider{}, false, nil
	}
	p, ok, err := s.mediaSettings().Provider(ctx, cap, claims.Tenant)
	if err != nil || !ok {
		return p, ok, err
	}
	if p.AuthForward != "" {
		// An auth-"caller" provider: pin the caller's own Authorization.
		if strings.TrimSpace(callerAuth) == "" {
			return media.Provider{}, false, nil
		}
		p = p.WithCallerAuth(strings.TrimSpace(callerAuth))
	}
	return p, true, nil
}

// mediaCaller resolves who a media call acts as: the caller themself, or
// with ?job=&room= the bridge running the job as the job's requester
// (contentGrant, the content routes' admission).
func (s *Service) mediaCaller(w http.ResponseWriter, r *http.Request, caller auth.Claims) (auth.Claims, bool) {
	if strings.TrimSpace(r.URL.Query().Get("job")) == "" {
		return caller, true
	}
	actor, err := s.contentGrant(r, caller)
	if err != nil {
		writeGrantErr(w, err)
		return auth.Claims{}, false
	}
	return actor.Claims, true
}

// headerAuth is the request's Authorization header value, for pinning into
// a caller-auth provider.
func headerAuth(r *http.Request) string { return strings.TrimSpace(r.Header.Get("Authorization")) }

// handleMediaCapabilities answers GET /v1/media: which media calls run for
// the caller (the entries the web shows) and, for image, the models the
// caller may pick from (the first the default).
func (s *Service) handleMediaCapabilities(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	out := wire.MediaCapabilities{}
	for _, cap := range media.Capabilities {
		p, ok, err := s.mediaProvider(r.Context(), claims, cap, headerAuth(r))
		if err != nil {
			obs.ServerError(w, "read the media provider failed", err)
			httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
			return
		}
		if !ok || !p.Model.Configured() {
			continue
		}
		if cap == media.CapImage {
			out.Image.Available = true
			out.Image.Models = p.Models
			continue
		}
		out.Transcribe.Available = true
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// errMediaUnavailable: no provider, or the organization turned AI off.
var errMediaUnavailable = errors.New("this is not available here: the deployment has no provider for it, or your organization turned AI off")

// handleMediaTranscribe answers POST /v1/media/transcriptions: the request
// body is the audio (audio/*), at most wire.MaxMediaAudioBytes; the reply is
// the transcript. Feeds message composition, so it drives agents' routes'
// permission rule (PermDriveAgent), spends the cost budget and the
// member's assist rate, and is audited as an assist run (never the audio,
// never the text).
func (s *Service) handleMediaTranscribe(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	// A room tool's call runs as the job's requester (?job=&room=, the
	// content routes' admission): the bridge driving the job acts for them.
	actor, ok := s.mediaCaller(w, r, claims)
	if !ok {
		return
	}
	claims = actor
	if !s.assistLimiter.get(assistRate, assistBurst, s.Peers.Size).allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	// ?attachment= names a room attachment to transcribe (the media tools
	// component's transcribe_audio): its bytes are read as the caller
	// reads them, then transcribed. Without it the body is the audio.
	var (
		audio    []byte
		mimeType string
		filename string
	)
	if id := strings.TrimSpace(r.URL.Query().Get("attachment")); id != "" {
		roomID := strings.TrimSpace(r.URL.Query().Get("room"))
		if roomID == "" {
			httpx.Error(w, "attachment is a room's: name the room (room)", http.StatusBadRequest)
			return
		}
		audio, mimeType, filename, ok = s.readRoomAttachmentAudio(ctx, claims, roomID, id)
		if !ok {
			httpx.Error(w, "no such audio attachment in this room", http.StatusNotFound)
			return
		}
	} else {
		audio, ok = readBody(w, r, wire.MaxMediaAudioBytes)
		if !ok {
			return
		}
		if len(audio) == 0 {
			httpx.Error(w, "the request body is the audio: it is empty", http.StatusBadRequest)
			return
		}
		mimeType = strings.TrimSpace(r.Header.Get("Content-Type"))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		filename = filenameQuery(r)
	}
	p, has, err := s.mediaProvider(ctx, claims, media.CapTranscribe, headerAuth(r))
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, errMediaUnavailable.Error(), http.StatusConflict)
		return
	}
	start := time.Now()
	text, err := p.Transcribe(ctx, audio, mimeType, filename)
	usage := wire.AssistUsage{Model: p.Model.Model, LatencyMs: timeSinceMs(s, start)}
	s.auditMediaRun(ctx, claims, "transcribe", usage, err)
	if err != nil {
		writeMediaErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.MediaTranscribeReply{Text: text})
}

// handleMediaImage answers POST /v1/media/image: one image generation.
// Same admission, budget, rate and audit as a transcription (never the
// prompt, never the image).
func (s *Service) handleMediaImage(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	// A room tool's call runs as the job's requester (?job=&room=, the
	// content routes' admission): the bridge driving the job acts for them.
	actor, ok := s.mediaCaller(w, r, claims)
	if !ok {
		return
	}
	s.runMediaImage(w, r, actor, readJSONMediaImage, "image")
}

// readJSONMediaImage reads a member route's JSON body.
func readJSONMediaImage(w http.ResponseWriter, r *http.Request) (wire.MediaImageRequest, bool) {
	raw, ok := readBody(w, r, 64<<10+wire.MaxMediaImageBytes)
	if !ok {
		return wire.MediaImageRequest{}, false
	}
	var req wire.MediaImageRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return wire.MediaImageRequest{}, false
	}
	return req, true
}

// runMediaImage is the ONE owner of an image generation call (the member
// route and an app's ~app/ai/image): the caller's assist rate, the request's
// validation, the organization's provider, the generation (Count images in
// one provider call; an edit of the request's inline image), the audit row
// (never the prompt, never the images) and the reply.
func (s *Service) runMediaImage(w http.ResponseWriter, r *http.Request, claims auth.Claims, read func(http.ResponseWriter, *http.Request) (wire.MediaImageRequest, bool), auditName string) {
	ctx := r.Context()
	if !s.assistLimiter.get(assistRate, assistBurst, s.Peers.Size).allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	req, ok := read(w, r)
	if !ok {
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	switch {
	case req.Prompt == "":
		httpx.Error(w, "image needs a prompt", http.StatusBadRequest)
		return
	case !utf8.ValidString(req.Prompt) || utf8.RuneCountInString(req.Prompt) > wire.MaxMediaImagePrompt:
		httpx.Error(w, fmt.Sprintf("the prompt is at most %d characters", wire.MaxMediaImagePrompt), http.StatusBadRequest)
		return
	case req.Aspect != "" && !aspectAllowed(req.Aspect):
		httpx.Error(w, "aspect must be one of "+strings.Join(wire.MediaImageAspects, ", "), http.StatusBadRequest)
		return
	}
	var input *media.ImageInput
	if req.Image != "" {
		b, mimeType, err := decodeDataURLImage(req.Image)
		if err != nil {
			httpx.Error(w, "image must be a data URL of a PNG, JPEG or WebP image", http.StatusBadRequest)
			return
		}
		input = &media.ImageInput{Bytes: b, MimeType: mimeType}
	}
	p, has, err := s.mediaProvider(ctx, claims, media.CapImage, headerAuth(r))
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, errMediaUnavailable.Error(), http.StatusConflict)
		return
	}
	if req.Count < 1 {
		req.Count = 1 // the default: one image
	}
	if req.Count > wire.MaxMediaImages {
		httpx.Error(w, fmt.Sprintf("count must be 1…%d", wire.MaxMediaImages), http.StatusBadRequest)
		return
	}
	start := time.Now()
	imgs, err := p.Generate(ctx, media.ImageRequest{Prompt: req.Prompt, Model: req.Model, Aspect: req.Aspect, Count: req.Count, Input: input})
	usage := wire.AssistUsage{Model: p.Model.Model, LatencyMs: timeSinceMs(s, start)}
	s.auditMediaRun(ctx, claims, auditName, usage, err)
	if err != nil {
		writeMediaErr(w, err)
		return
	}
	// Publish, when asked under a job grant: each image becomes a room
	// artifact version (the image then is not also inlined).
	roomID := strings.TrimSpace(r.URL.Query().Get("room"))
	publish := req.Publish && roomID != "" && strings.TrimSpace(r.URL.Query().Get("job")) != ""
	out := wire.MediaImageReply{Images: make([]wire.MediaImage, 0, len(imgs))}
	for i, img := range imgs {
		width, height := pngSize(img.Bytes)
		row := wire.MediaImage{MimeType: img.MimeType, Width: width, Height: height, RevisedPrompt: img.RevisedPrompt}
		if publish {
			id, err := s.publishGeneratedImage(ctx, claims, roomID, fileNameOf(req.FileName, i, img.MimeType), img)
			if err != nil {
				obs.ServerError(w, "publish the generated image failed", err)
				httpx.Error(w, "the image is drawn, but publishing it into the room failed: try again", http.StatusBadGateway)
				return
			}
			row.ArtifactID = id
		} else {
			row.DataURL = "data:" + img.MimeType + ";base64," + base64Of(img.Bytes)
		}
		out.Images = append(out.Images, row)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// publishGeneratedImage publishes one drawn image into the room's Docs as
// the member in claims (a job grant's requester): a file artifact of one
// version, through the room service's own multipart publish route — the
// same contract the bridge's deliverable publishing drives. It answers the
// artifact id.
func (s *Service) publishGeneratedImage(ctx context.Context, claims auth.Claims, roomID, name string, img media.Image) (string, error) {
	kind := "file"
	if strings.HasPrefix(img.MimeType, "image/") {
		kind = "image"
	}
	artifactID := "artf_" + hexOf([]byte(roomID + "\x00" + claims.Subject + "\x00" + name))[:24]
	create := map[string]any{"artifactId": artifactID, "kind": kind, "title": name, "fileName": name}
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/artifacts", "application/json", mustRaw(create), claims.Subject, claims.Tenant, nil); err != nil {
		return "", fmt.Errorf("create the artifact: %w", err)
	}
	// One blob version, like the bridge's own publish path.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("baseVersion", "0")
	_ = mw.WriteField("summary", "generated by the AI")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": name}))
	h.Set("Content-Type", img.MimeType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(img.Bytes); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	path := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts/" + url.PathEscape(artifactID) + "/versions"
	var reply struct {
		Unchanged bool                  `json:"unchanged"`
		Version   *wire.ArtifactVersion `json:"version"`
	}
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodPost, path, mw.FormDataContentType(), buf.Bytes(), claims.Subject, claims.Tenant, &reply); err != nil {
		return "", fmt.Errorf("publish the version: %w", err)
	}
	return artifactID, nil
}

// mustRaw is a map's JSON bytes (a create body).
func mustRaw(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// hexOf is the bytes' hex (an artifact id derivation; ids.Hex would work
// too — this keeps the derivation local and uninteresting).
func hexOf(b []byte) string { return hex.EncodeToString(b) }

// fileNameOf names the i-th image of a batch: the asked name (the first),
// else a name derived from nothing but the index and the MIME type.
func fileNameOf(asked string, i int, mimeType string) string {
	ext := ".png"
	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	}
	name := strings.TrimSpace(asked)
	if name == "" {
		name = "image" + ext
	}
	if i == 0 {
		return name
	}
	return strings.TrimSuffix(name, path.Ext(name)) + "-" + strconv.Itoa(i+1) + ext
}

// decodeDataURLImage splits a data:…;base64,<bytes> URL into sniffed image
// bytes and their MIME type.
func decodeDataURLImage(dataURL string) ([]byte, string, error) {
	_, b64, ok := strings.Cut(dataURL, ",")
	if !ok {
		return nil, "", errBadDataURL
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) == 0 || len(b) > wire.MaxMediaImageBytes {
		return nil, "", errBadDataURL
	}
	mimeType := sniffMIME(b)
	if mimeType == "" {
		return nil, "", errBadDataURL
	}
	return b, mimeType, nil
}

// errBadDataURL: an input image that is not an inline PNG, JPEG or WebP.
var errBadDataURL = errors.New("not an inline image")

// sniffMIME is the image sniff of the gateway's media path (media.Image's
// sniff spelled here; the owner is the adapter's).
func sniffMIME(b []byte) string {
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

// readRoomAttachmentAudio reads a room attachment's bytes as the member in
// claims, refusing anything that is not audio. It answers the bytes, the
// content type and the file name.
func (s *Service) readRoomAttachmentAudio(ctx context.Context, claims auth.Claims, roomID, id string) ([]byte, string, string, bool) {
	meta, found, err := s.attachmentMeta(ctx, claims, roomID, id)
	if err != nil || !found {
		return nil, "", "", false
	}
	if !strings.HasPrefix(meta.ContentType, "audio/") {
		return nil, "", "", false
	}
	data, found, err := s.attachmentRead(ctx, claims, roomID, id, "", wire.MaxMediaAudioBytes)
	if err != nil || !found || len(data) == 0 {
		return nil, "", "", false
	}
	return data, meta.ContentType, meta.Filename, true
}

// auditMediaRun records one media model call: the capability's name, the
// model and the latency, never the audio, the prompt, the transcript or the
// image (auditModelRun's shape, kind assist).
func (s *Service) auditMediaRun(ctx context.Context, claims auth.Claims, name string, usage wire.AssistUsage, runErr error) {
	modelName := firstNonEmpty(usage.Model, "")
	detail := "media " + name + ", model " + modelName + ", " + itoa(usage.LatencyMs) + " ms"
	outcome := wire.AuditOutcomeAllow
	if runErr != nil {
		outcome, detail = "failed", detail+", failed: "+mediaErrClass(runErr)
	}
	s.audit(ctx, claims.Tenant, store.ExtAuditRecord{
		Kind: wire.AuditKindAssist, Name: name, Action: "run",
		Actor: claims.Subject, Outcome: outcome, Detail: detail,
	})
}

// mediaErrClass names a media failure for the audit row without the
// provider's message (it may quote the request).
func mediaErrClass(err error) string {
	var pe *media.ProviderError
	switch {
	case errors.As(err, &pe):
		return "provider answered " + itoa(int64(pe.Status))
	case errors.Is(err, media.ErrProviderUnreachable):
		return "unreadable reply"
	case errors.Is(err, context.Canceled):
		return "caller went away"
	}
	return "provider unreachable"
}

// writeMediaErr answers a media failure: the provider's refusal carries its
// own message; everything else is one plain sentence.
func writeMediaErr(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	var pe *media.ProviderError
	if errors.As(err, &pe) {
		// A refusal the provider owns (4xx) answers its own status; anything
		// else is ours (502).
		status := http.StatusBadGateway
		if pe.Status >= 400 && pe.Status < 500 {
			status = pe.Status
		}
		httpx.WriteStatus(w, status, "the media provider refused the call: "+pe.Message, err)
		return
	}
	httpx.WriteStatus(w, http.StatusBadGateway, "the media provider could not be reached", err)
}

// filenameQuery is the transcription's optional ?filename= (a provider may
// need the extension).
func filenameQuery(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("filename"))
}

// aspectAllowed reports an aspect the request may name.
func aspectAllowed(aspect string) bool {
	for _, a := range wire.MediaImageAspects {
		if a == aspect {
			return true
		}
	}
	return false
}

// timeSinceMs is start's elapsed milliseconds.
func timeSinceMs(s *Service, start time.Time) int64 { return time.Since(start).Milliseconds() }

// base64Of is the bytes' standard base64 (a data URL's body).
func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// pngSize reads a PNG's IHDR dimensions; 0, 0 when the bytes are not a PNG
// (the client sizes the image from its own load).
func pngSize(b []byte) (int, int) {
	if len(b) < 24 || string(b[:8]) != "\x89PNG\r\n\x1a\n" || string(b[12:16]) != "IHDR" {
		return 0, 0
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24]))
}

// itoa is strconv.Itoa, spelled for the audit detail lines.
func itoa(n int64) string { return fmt.Sprintf("%d", n) }

// mediaCapability is the path's capability (a route's {capability}).
func mediaCapability(r *http.Request) (media.Capability, bool) {
	switch strings.TrimSpace(r.PathValue("capability")) {
	case string(media.CapTranscribe):
		return media.CapTranscribe, true
	case string(media.CapImage):
		return media.CapImage, true
	}
	return "", false
}

// handleMediaProvider answers GET /v1/platform/media/{capability}.
func (s *Service) handleMediaProvider(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
	cap, ok := mediaCapability(r)
	if !ok {
		httpx.Error(w, "capability must be one of "+strings.Join(capNames(), ", "), http.StatusNotFound)
		return
	}
	view, err := s.mediaSettings().View(r.Context(), cap, "")
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// capNames are the capabilities, as strings.
func capNames() []string {
	out := make([]string, 0, len(media.Capabilities))
	for _, c := range media.Capabilities {
		out = append(out, string(c))
	}
	return out
}

// handlePutMediaProvider answers PUT /v1/platform/media/{capability}: set
// the capability's provider (kind, base URL, model, key, params).
func (s *Service) handlePutMediaProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	cap, ok := mediaCapability(r)
	if !ok {
		httpx.Error(w, "capability must be one of "+strings.Join(capNames(), ", "), http.StatusNotFound)
		return
	}
	var u wire.MediaProviderUpdate
	if !httpx.DecodeJSON(w, r, 64<<10, &u) {
		return
	}
	view, err := s.mediaSettings().Put(r.Context(), cap, "", u, claims.Subject)
	switch {
	case errors.Is(err, modelsettings.ErrSettingsOff):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &modelsettings.InvalidError{}):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		obs.ServerError(w, "save the media provider failed", err)
		httpx.Error(w, "save the media provider failed", http.StatusInternalServerError)
	default:
		s.audit(r.Context(), claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindAssist, Name: "media-" + string(cap),
			Action: "provider-set", Actor: claims.Subject, Outcome: wire.AuditOutcomeAllow,
			Detail: "kind " + view.Kind + ", model " + view.Model + " at " + hostOfBaseURL(view.BaseURL)})
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

// hostOfBaseURL is the host of a base URL, for the audit detail line only.
func hostOfBaseURL(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	return u.Host
}

// handleOrgMediaProvider answers GET /v1/tenant/media/{capability}: the
// organization's own provider and the hosts it may name (the platform's
// orgHosts; empty = an organization cannot set its own).
func (s *Service) handleOrgMediaProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	cap, ok := mediaCapability(r)
	if !ok {
		httpx.Error(w, "capability must be one of "+strings.Join(capNames(), ", "), http.StatusNotFound)
		return
	}
	ctx := r.Context()
	own, err := s.mediaSettings().View(ctx, cap, claims.Tenant)
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	platform, err := s.mediaSettings().View(ctx, cap, "")
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	out := wire.MediaOrgProvider{Provider: own, AllowedHosts: platform.OrgHosts, PlatformModel: platform.Model}
	if out.AllowedHosts == nil {
		out.AllowedHosts = []string{}
	}
	if out.Provider.OrgHosts == nil {
		out.Provider.OrgHosts = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handlePutOrgMediaProvider answers PUT /v1/tenant/media/{capability}: the
// organization's own provider, on a host the platform allows. An empty base
// URL goes back to the platform's.
func (s *Service) handlePutOrgMediaProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	cap, ok := mediaCapability(r)
	if !ok {
		httpx.Error(w, "capability must be one of "+strings.Join(capNames(), ", "), http.StatusNotFound)
		return
	}
	var u wire.MediaProviderUpdate
	if !httpx.DecodeJSON(w, r, 64<<10, &u) {
		return
	}
	u.OrgHosts = nil // an organization never sets the hosts
	view, err := s.mediaSettings().Put(r.Context(), cap, claims.Tenant, u, claims.Subject)
	switch {
	case errors.Is(err, modelsettings.ErrSettingsOff):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &modelsettings.InvalidError{}):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		obs.ServerError(w, "save the media provider failed", err)
		httpx.Error(w, "save the media provider failed", http.StatusInternalServerError)
	default:
		s.audit(r.Context(), claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindAssist, Name: "org-media-" + string(cap),
			Action: "provider-set", Actor: claims.Subject, Outcome: wire.AuditOutcomeAllow,
			Detail: "kind " + view.Kind + ", model " + view.Model + " at " + hostOfBaseURL(view.BaseURL)})
		s.handleOrgMediaProvider(w, r, claims)
	}
}

// handleTestMediaProvider answers POST /v1/platform/media/{capability}/test
// through the platform's provider.
func (s *Service) handleTestMediaProvider(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
	s.testMediaProvider(w, r, "")
}

// handleTestOrgMediaProvider answers POST
// /v1/tenant/media/{capability}/test through the organization's effective
// provider (its own, else the platform's).
func (s *Service) handleTestOrgMediaProvider(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.testMediaProvider(w, r, claims.Tenant)
}

// testMediaProvider makes one real short call through the capability's
// provider for tenant; a failure is the answer, not an HTTP error.
func (s *Service) testMediaProvider(w http.ResponseWriter, r *http.Request, tenant string) {
	cap, ok := mediaCapability(r)
	if !ok {
		httpx.Error(w, "capability must be one of "+strings.Join(capNames(), ", "), http.StatusNotFound)
		return
	}
	ctx := r.Context()
	p, has, err := s.mediaSettings().Provider(ctx, cap, tenant)
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, "no provider is configured for this capability", http.StatusConflict)
		return
	}
	out := wire.MediaProviderTest{}
	start := time.Now()
	switch cap {
	case media.CapTranscribe:
		text, err := p.Transcribe(ctx, testAudio(), "audio/wav", "test.wav")
		out.LatencyMs = time.Since(start).Milliseconds()
		if err != nil {
			out.Error = mediaErrText(err)
			break
		}
		out.OK, out.Reply = true, firstNonEmpty(strings.TrimSpace(text), "(no speech)")
	default:
		imgs, err := p.Generate(ctx, media.ImageRequest{Prompt: "A small solid blue square on white.", Aspect: wire.MediaImageAspectSquare})
		out.LatencyMs = time.Since(start).Milliseconds()
		if err != nil {
			out.Error = mediaErrText(err)
			break
		}
		if len(imgs) > 0 {
			out.OK = true
			out.Reply = fmt.Sprintf("%s, %d bytes", imgs[0].MimeType, len(imgs[0].Bytes))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// testAudio is a minimal silent WAV file (44-byte header, 8 zero samples):
// enough for a provider to prove it answers a transcription.
func testAudio() []byte {
	const header = "RIFF$\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\xbb\x00\x00\x80\xbb\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00"
	return []byte(header)
}

// mediaErrText is a media failure's sentence for a test answer.
func mediaErrText(err error) string {
	var pe *media.ProviderError
	if errors.As(err, &pe) {
		return pe.Error()
	}
	return "the provider could not be reached: " + err.Error()
}
