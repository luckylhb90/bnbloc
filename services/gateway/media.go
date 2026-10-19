package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"slices"
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
// speech to text for the composer, image and video generation for agents,
// through admin-configured providers (internal/media). internal/media owns
// the provider call; this file admits the caller and relays the reply,
// exactly as the assist routes do (the rate, the budget, the audit kind).
//
//	GET  /v1/media                          which media calls run for the caller
//	POST /v1/media/transcriptions           one transcription (raw audio body)
//	POST /v1/media/image                    one image generation
//	POST /v1/media/video                    submit one video (media_video.go)
//	GET  /v1/media/video/{id}               its status; done, its room file
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
// assist switch's meaning: no data leaving to a model). ownAuth is the
// request's Authorization only when it is the acting member's own
// (ownAuthOf): an auth-"caller" provider bills the caller, so it is pinned
// there, and a call acting for someone else (a job grant) finds no
// provider rather than forwarding the bridge owner's session. The header
// value never reaches a key-auth provider.
func (s *Service) mediaProvider(ctx context.Context, claims auth.Claims, cap media.Capability, ownAuth string) (media.Provider, bool, error) {
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
		if strings.TrimSpace(ownAuth) == "" {
			return media.Provider{}, false, nil
		}
		p = p.WithCallerAuth(strings.TrimSpace(ownAuth))
	}
	return p, true, nil
}

// mediaCaller resolves who a model call (media, decisions) acts as: the
// caller themself, or with ?job=&room= the bridge running the job as the
// job's requester (contentGrant, the content routes' admission; Job set).
func (s *Service) mediaCaller(w http.ResponseWriter, r *http.Request, caller auth.Claims) (contentActor, bool) {
	if strings.TrimSpace(r.URL.Query().Get("job")) == "" {
		return contentActor{Claims: caller}, true
	}
	actor, err := s.contentGrant(r, caller)
	if err != nil {
		writeGrantErr(w, err)
		return contentActor{}, false
	}
	return actor, true
}

// ownAuthOf is the request's Authorization when the call acts as the
// bearer's own subject, "" when it acts for someone else (a job grant: the
// bearer is the bridge owner's). A caller-auth provider is only ever sent
// the acting member's own session.
func ownAuthOf(r *http.Request, actor contentActor) string {
	if actor.Job != nil {
		return ""
	}
	return headerAuth(r)
}

// headerAuth is the request's Authorization header value, for pinning into
// a caller-auth provider.
func headerAuth(r *http.Request) string { return strings.TrimSpace(r.Header.Get("Authorization")) }

// handleMediaCapabilities answers GET /v1/media: which media calls run for
// the caller (the entries the web shows) and, for a generation (image,
// video), the models the caller may pick from (the first the default).
func (s *Service) handleMediaCapabilities(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	out := wire.MediaCapabilities{}
	slots := map[media.Capability]*wire.MediaCapability{media.CapTranscribe: &out.Transcribe, media.CapImage: &out.Image, media.CapVideo: &out.Video}
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
		slot := slots[cap]
		slot.Available = true
		if cap != media.CapTranscribe {
			slot.Models = p.Models // a transcription names no model
		}
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
	claims = actor.Claims
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
	p, has, err := s.mediaProvider(ctx, claims, media.CapTranscribe, ownAuthOf(r, actor))
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
	s.auditMediaRun(ctx, claims, "transcribe", "", usage, err)
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
	s.runMediaImage(w, r, actor, jobMediaPlace(actor.Job), readJSONMediaImage, "image")
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
// (never the prompt, never the images) and the reply. actor is who the call
// acts as; place, when set (an agent's job, a room app), is where the
// images may be published and whose room pictures the call may read.
func (s *Service) runMediaImage(w http.ResponseWriter, r *http.Request, actor contentActor, place *mediaPlace, read func(http.ResponseWriter, *http.Request) (wire.MediaImageRequest, bool), auditName string) {
	ctx := r.Context()
	claims := actor.Claims
	if !s.assistLimiter.get(assistRate, assistBurst, s.Peers.Size).allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	req, ok := read(w, r)
	if !ok {
		return
	}
	if req.Prompt, ok = mediaPrompt(w, req.Prompt, "image"); !ok {
		return
	}
	if req.Aspect != "" && !slices.Contains(wire.MediaImageAspects, req.Aspect) {
		httpx.Error(w, "aspect must be one of "+strings.Join(wire.MediaImageAspects, ", "), http.StatusBadRequest)
		return
	}
	gen := media.ImageRequest{Prompt: req.Prompt, Model: req.Model, Aspect: req.Aspect, Count: req.Count}
	if req.Image != "" {
		b, mimeType, err := decodeDataURLImage(req.Image)
		if err != nil {
			httpx.Error(w, "image must be a data URL of a PNG, JPEG or WebP image", http.StatusBadRequest)
			return
		}
		gen.Input = &media.ImageInput{Bytes: b, MimeType: mimeType}
	}
	// The room's pictures (an agent's call): the file to edit and the
	// references, read as the job's requester.
	edit, ok := s.imageRoomInputs(w, r, actor.Claims, place, req, &gen)
	if !ok {
		return
	}
	p, has, err := s.mediaProvider(ctx, claims, media.CapImage, ownAuthOf(r, actor))
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
	gen.Count = req.Count
	imgs, err := p.Generate(ctx, gen)
	usage := wire.AssistUsage{Model: p.Model.Model, LatencyMs: timeSinceMs(s, start)}
	s.auditMediaRun(ctx, claims, auditName, imageAuditDetail(gen, edit), usage, err)
	if err != nil {
		writeMediaErr(w, err)
		return
	}
	// Publish, when asked under a job grant: each image becomes a room
	// artifact, an edit the edited file's next version (the image then is
	// not also inlined).
	publish := (req.Publish || edit != nil) && place != nil
	out := wire.MediaImageReply{Images: make([]wire.MediaImage, 0, len(imgs))}
	for i, img := range imgs {
		width, height, _ := media.ImageSize(img.Bytes)
		row := wire.MediaImage{MimeType: img.MimeType, Width: width, Height: height, RevisedPrompt: img.RevisedPrompt}
		if publish {
			target := edit
			if i > 0 {
				target = nil // one edit is one version; the rest of a batch are files beside it
			}
			name := fileNameOf(req.FileName, i, img.MimeType)
			if edit != nil {
				name = fileNameOf(edit.FileName, i, img.MimeType)
			}
			pub, err := s.publishMediaFile(ctx, claims, *place, mediaFile{Name: name, Bytes: img.Bytes, MimeType: img.MimeType}, target)
			if err != nil {
				obs.ServerError(w, "publish the generated image failed", err)
				httpx.Error(w, "the image is drawn, but publishing it into the room failed: try again", http.StatusBadGateway)
				return
			}
			row.ArtifactID, row.Version, row.FileName, row.Note = pub.ArtifactID, pub.Version, name, pub.Note
		} else {
			row.DataURL = "data:" + img.MimeType + ";base64," + base64Of(img.Bytes)
		}
		out.Images = append(out.Images, row)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// publishedFile is where one generated file landed: the artifact, its
// version, and a note when it is not what was asked.
type publishedFile struct {
	ArtifactID string
	Version    int
	Note       string
}

// mediaPlace is where generated files land and whose pictures a call
// reads: a room, its channel, and the source a version records (an agent's
// job and its request; an app records none).
type mediaPlace struct {
	RoomID, ChannelID string
	SourceJobID       string
	SourceSeq         int64
	Summary           string
}

// jobMediaPlace is an agent job's place: its room and channel, sourced to
// the job and its request.
func jobMediaPlace(job *store.AgentJob) *mediaPlace {
	if job == nil {
		return nil
	}
	return &mediaPlace{RoomID: job.RoomID, ChannelID: job.ChannelID, SourceJobID: job.JobID, SourceSeq: job.MessageSeq, Summary: "generated by the AI"}
}

// mediaFile is one generated file to publish: its name, bytes and MIME
// type, and the id it is published under ("" = the room service mints
// one; a stable id makes a repeated publish the same file: a video's).
type mediaFile struct {
	Name       string
	Bytes      []byte
	MimeType   string
	ArtifactID string
}

// publishMediaFile is the one owner of "a generated file (a drawn image, a
// filmed video) becomes room content", as the member in claims (a job's
// requester, an app's participant), in the place's room and channel, with
// its source. With edit it publishes the edited file's next version
// (editedFrom the version read), and when the file changed meanwhile (409)
// a new file beside it, saying so; otherwise a new file of one version
// (the room keeps an existing file of f.ArtifactID's history: its first
// version is then refused as a conflict, which the caller reads as
// published already).
func (s *Service) publishMediaFile(ctx context.Context, claims auth.Claims, place mediaPlace, f mediaFile, edit *imageEdit) (publishedFile, error) {
	room := "/v1/rooms/" + url.PathEscape(place.RoomID) + "/artifacts"
	if edit != nil {
		v, err := s.publishMediaVersion(ctx, claims, place, room, edit.ArtifactID, edit.Version, edit.Version, f)
		var rbe *roomBackendError
		if !errors.As(err, &rbe) || rbe.Status != http.StatusConflict {
			pub := publishedFile{ArtifactID: edit.ArtifactID, Version: v}
			if err == nil && v == edit.Version {
				pub.Note = "the provider returned the image unchanged: no new version"
			}
			return pub, err
		}
		pub, err := s.publishMediaFile(ctx, claims, place, f, nil)
		pub.Note = "the file changed meanwhile: saved as a new file"
		return pub, err
	}
	kind := "file"
	if strings.HasPrefix(f.MimeType, "image/") {
		kind = "image"
	}
	create := map[string]any{"kind": kind, "title": f.Name, "fileName": f.Name, "channelId": place.ChannelID}
	if f.ArtifactID != "" {
		create["artifactId"] = f.ArtifactID
	}
	var created wire.ArtifactReply
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodPost, room, "application/json", mustRaw(create), claims.Subject, claims.Tenant, &created); err != nil {
		return publishedFile{}, fmt.Errorf("create the artifact: %w", err)
	}
	artifactID := created.Artifact.ArtifactID
	if artifactID == "" {
		return publishedFile{}, errors.New("create the artifact: the reply names no artifact")
	}
	v, err := s.publishMediaVersion(ctx, claims, place, room, artifactID, 0, 0, f)
	return publishedFile{ArtifactID: artifactID, Version: v}, err
}

// publishMediaVersion publishes one blob version of artifactID on top of
// base (editedFrom 0: not an edit), like the bridge's own publish path.
// It answers the version published. The upload is bounded by ctx, never a
// client timeout: a video's file outlives a short call's.
func (s *Service) publishMediaVersion(ctx context.Context, claims auth.Claims, place mediaPlace, room, artifactID string, base, editedFrom int, f mediaFile) (int, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("baseVersion", strconv.Itoa(base))
	_ = mw.WriteField("summary", place.Summary)
	if place.SourceJobID != "" {
		_ = mw.WriteField("sourceJobId", place.SourceJobID)
	}
	if place.SourceSeq > 0 {
		_ = mw.WriteField("sourceSeq", strconv.FormatInt(place.SourceSeq, 10))
	}
	if editedFrom > 0 {
		_ = mw.WriteField("editedFrom", strconv.Itoa(editedFrom))
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": f.Name}))
	h.Set("Content-Type", f.MimeType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return 0, err
	}
	if _, err := part.Write(f.Bytes); err != nil {
		return 0, err
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}
	var reply wire.ArtifactReply
	if err := s.roomBackendDo(ctx, backendStreamHTTP, http.MethodPost, room+"/"+url.PathEscape(artifactID)+"/versions", mw.FormDataContentType(), buf.Bytes(), claims.Subject, claims.Tenant, &reply); err != nil {
		return 0, fmt.Errorf("publish the version: %w", err)
	}
	if reply.Version != nil {
		return reply.Version.Version, nil
	}
	return reply.Artifact.CurrentVersion, nil // the bytes were already the current version
}

// imageRoomInputs reads the place's room pictures into gen, as the member
// in claims: the file to edit (req.ArtifactID; its next version is the
// result) or, without one, the first input as the image to change, the
// rest as references; and the region's mask. A call with no place (a
// member's own) may not name room pictures. false: refused, answered.
func (s *Service) imageRoomInputs(w http.ResponseWriter, r *http.Request, claims auth.Claims, place *mediaPlace, req wire.MediaImageRequest, gen *media.ImageRequest) (*imageEdit, bool) {
	if req.ArtifactID == "" && len(req.Inputs) == 0 && req.Region == nil {
		return nil, true
	}
	if place == nil {
		httpx.Error(w, "artifactId, region and inputs name a room's pictures (an agent's or a room app's call): send the image itself", http.StatusBadRequest)
		return nil, false
	}
	ctx, roomID := r.Context(), place.RoomID
	var edit *imageEdit
	if id := strings.TrimSpace(req.ArtifactID); id != "" {
		e, err := s.readEditTarget(ctx, claims, roomID, id)
		if !imageInputOK(w, err) {
			return nil, false
		}
		edit, gen.Input = &e, &e.Input
	}
	refs, err := s.readImageInputs(ctx, claims, roomID, req.Inputs)
	if !imageInputOK(w, err) {
		return nil, false
	}
	if gen.Input == nil && len(refs) > 0 {
		gen.Input, refs = &refs[0], refs[1:]
	}
	gen.References = refs
	if req.Region != nil {
		if gen.Input == nil {
			httpx.Error(w, "region needs an image to change (artifactId)", http.StatusBadRequest)
			return nil, false
		}
		mask, ok := s.regionMask(ctx, w, claims, roomID, gen.Input.Bytes, *req.Region)
		if !ok {
			return nil, false
		}
		gen.Mask = mask
	}
	return edit, true
}

// regionMask is a region's mask for input: the member's painted mask (an
// attachment of the room, read as the member in claims) when the region
// names one, else the rectangle's. false: refused, answered.
func (s *Service) regionMask(ctx context.Context, w http.ResponseWriter, claims auth.Claims, roomID string, input []byte, region wire.MediaImageRegion) ([]byte, bool) {
	var mask []byte
	var err error
	if id := strings.TrimSpace(region.Mask); id != "" {
		painted, readErr := s.readAttachmentImage(ctx, claims, roomID, id)
		if !imageInputOK(w, readErr) {
			return nil, false
		}
		mask, err = media.PaintedMask(input, painted.Bytes)
	} else {
		mask, err = media.EditMask(input, media.Region{X: region.X, Y: region.Y, W: region.W, H: region.H})
	}
	if err != nil {
		httpx.Error(w, "region: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return mask, true
}

// imageInputOK answers a failed input read: a refused input 400 with its
// sentence, anything else 502.
func imageInputOK(w http.ResponseWriter, err error) bool {
	var refused errImageInput
	switch {
	case err == nil:
		return true
	case errors.As(err, &refused):
		httpx.Error(w, refused.Error(), http.StatusBadRequest)
	default:
		obs.ServerError(w, "read an image input failed", err)
		httpx.Error(w, "reading the image failed: try again", http.StatusBadGateway)
	}
	return false
}

// imageAuditDetail is the audit detail's note on an edit: what kind of
// call it was, never the content.
func imageAuditDetail(gen media.ImageRequest, edit *imageEdit) string {
	var parts []string
	if edit != nil {
		parts = append(parts, "edit")
	}
	if len(gen.Mask) > 0 {
		parts = append(parts, "region")
	}
	if n := len(gen.References); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" references")
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// mustRaw is a map's JSON bytes (a create body).
func mustRaw(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

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
	mimeType := media.SniffImage(b)
	if mimeType == "" {
		return nil, "", errBadDataURL
	}
	return b, mimeType, nil
}

// errBadDataURL: an input image that is not an inline PNG, JPEG or WebP.
var errBadDataURL = errors.New("not an inline image")

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
func (s *Service) auditMediaRun(ctx context.Context, claims auth.Claims, name, kind string, usage wire.AssistUsage, runErr error) {
	modelName := firstNonEmpty(usage.Model, "")
	detail := "media " + name + kind + ", model " + modelName + ", " + itoa(usage.LatencyMs) + " ms"
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
	case errors.Is(err, errVideoFailed):
		return "the provider failed the video"
	case errors.Is(err, errVideoPublish):
		return "publishing into the room failed"
	case errors.Is(err, media.ErrTooLarge):
		return "over the room's file cap"
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
	if errors.Is(err, media.ErrTooLarge) {
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge) // the sizes, said plainly
		return
	}
	httpx.WriteStatus(w, http.StatusBadGateway, "the media provider could not be reached", err)
}

// filenameQuery is the transcription's optional ?filename= (a provider may
// need the extension).
func filenameQuery(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("filename"))
}

// mediaPrompt is a generation's prompt, trimmed: present, valid UTF-8 and
// at most wire.MaxMediaPrompt characters (what names the generation in the
// refusal). false: refused, answered.
func mediaPrompt(w http.ResponseWriter, prompt, what string) (string, bool) {
	prompt = strings.TrimSpace(prompt)
	switch {
	case prompt == "":
		httpx.Error(w, what+" needs a prompt", http.StatusBadRequest)
		return "", false
	case !utf8.ValidString(prompt) || utf8.RuneCountInString(prompt) > wire.MaxMediaPrompt:
		httpx.Error(w, fmt.Sprintf("the prompt is at most %d characters", wire.MaxMediaPrompt), http.StatusBadRequest)
		return "", false
	}
	return prompt, true
}

// timeSinceMs is start's elapsed milliseconds.
func timeSinceMs(s *Service, start time.Time) int64 { return time.Since(start).Milliseconds() }

// base64Of is the bytes' standard base64 (a data URL's body).
func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// pngSize reads a PNG's IHDR dimensions; 0, 0 when the bytes are not a PNG

// itoa is strconv.Itoa, spelled for the audit detail lines.
func itoa(n int64) string { return fmt.Sprintf("%d", n) }

// mediaCapability is the path's capability (a route's {capability}): one
// of media.Capabilities.
func mediaCapability(r *http.Request) (media.Capability, bool) {
	name := strings.TrimSpace(r.PathValue("capability"))
	for _, c := range media.Capabilities {
		if string(c) == name {
			return c, true
		}
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
	case media.CapVideo:
		// Filming one would bill a video: the test reads the provider's
		// model list instead (the address, the key, and the model listed).
		models, err := p.ListModels(ctx)
		out.LatencyMs = time.Since(start).Milliseconds()
		if err != nil {
			out.Error = mediaErrText(err)
			break
		}
		out.OK, out.Reply = true, videoTestReply(p.Model.Model, models)
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
