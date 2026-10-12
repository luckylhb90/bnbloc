package room

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// errPackage is a package the import refuses (malformed, wrong format, over a
// bound). Its message is shown to the uploader.
type errPackage struct{ msg string }

func (e *errPackage) Error() string { return e.msg }

func badPackage(format string, args ...any) error {
	return &errPackage{msg: fmt.Sprintf(format, args...)}
}

// handleImportRoom creates a NEW room, owned by the caller in the caller's
// tenant, from an uploaded handoff package (multipart field "file"). Nothing in
// the package authorizes anything: the importer is the room's only member (the
// source member list is returned and stored as invite suggestions), the
// imported messages keep their original attribution as history, and their
// rows carry no author, so no one can edit or delete them as "their" message.
//
// The whole package is validated before anything is created; a failure after
// the room exists deletes the room again (best effort).
func (s *Service) handleImportRoom(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if s.Handoff == nil || s.RoomImports == nil || s.Artifacts == nil || s.Channels == nil {
		obs.ServerError(w, "handoff packages are not configured", nil)
		writeErr(w, http.StatusServiceUnavailable, "handoff packages are not configured")
		return
	}
	// The package plus multipart framing; the archive itself is re-checked
	// against maxHandoffPackageBytes below.
	r.Body = http.MaxBytesReader(w, r.Body, maxHandoffPackageBytes+64<<10)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("handoff package exceeds %d MiB", maxHandoffPackageBytes>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, "expected a multipart 'file' field with the handoff package")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	fhs := r.MultipartForm.File["file"]
	if len(fhs) != 1 {
		writeErr(w, http.StatusBadRequest, "expected a multipart 'file' field with the handoff package")
		return
	}
	if fhs[0].Size > maxHandoffPackageBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("handoff package exceeds %d MiB", maxHandoffPackageBytes>>20))
		return
	}
	f, err := fhs[0].Open()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unreadable upload")
		return
	}
	defer f.Close()
	zr, err := zip.NewReader(f, fhs[0].Size)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "the handoff package is not a zip archive")
		return
	}
	pkg, err := readHandoffPackage(zr, s.artifactMaxBytes())
	if err != nil {
		var pe *errPackage
		if errors.As(err, &pe) {
			writeErr(w, http.StatusBadRequest, pe.msg)
			return
		}
		writeErr(w, http.StatusBadRequest, "unreadable handoff package: "+err.Error())
		return
	}
	res, err := s.importPackage(r.Context(), user, pkg)
	if err != nil {
		log.Printf("room: import by %s failed: %v", user, err)
		var se *realtime.StatusError
		if errors.As(err, &se) || errors.Is(err, realtime.ErrHandoffUnconfigured) {
			obs.ServerError(w, "room history service refused the import", err)
			writeErr(w, http.StatusBadGateway, "room history service refused the import")
			return
		}
		writeErr(w, http.StatusInternalServerError, "import failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handoffPackage is a validated package: the manifest plus the archive entries
// the import reads again while writing.
type handoffPackage struct {
	manifest       wire.RoomExportManifest
	channels       []wire.RoomExportChannel
	defaultChannel string // original id of the default channel
	peopleChannel  string // original id of the people chat, "" when none
	channelIDs     map[string]bool
	messages       *zip.File
	messageCount   int
	artifacts      []wire.RoomExportArtifact
	attachments    map[string]wire.RoomExportAttachment // by original id
	attachmentIDs  []string                             // sorted, for a stable import order
	files          map[string]*zip.File
	// artifactMaxBytes caps one blob artifact version (the publish cap).
	artifactMaxBytes int64
}

// readHandoffPackage validates the whole archive without side effects.
func readHandoffPackage(zr *zip.Reader, artifactMaxBytes int64) (*handoffPackage, error) {
	pkg := &handoffPackage{files: map[string]*zip.File{}, attachments: map[string]wire.RoomExportAttachment{}, channelIDs: map[string]bool{}, artifactMaxBytes: artifactMaxBytes}
	var expanded uint64
	for _, f := range zr.File {
		name := f.Name
		if clean, ok := core.CleanRelPath(name); !ok || clean != name {
			return nil, badPackage("invalid archive entry name %q", name)
		}
		if _, dup := pkg.files[name]; dup {
			return nil, badPackage("duplicate archive entry %q", name)
		}
		pkg.files[name] = f
		expanded += f.UncompressedSize64
		if expanded > maxHandoffExpandedBytes {
			return nil, badPackage("handoff package expands beyond %d MiB", maxHandoffExpandedBytes>>20)
		}
	}
	if err := pkg.readManifest(); err != nil {
		return nil, err
	}
	if err := pkg.readAttachments(); err != nil {
		return nil, err
	}
	if err := pkg.readArtifacts(); err != nil {
		return nil, err
	}
	if err := pkg.scanMessages(); err != nil {
		return nil, err
	}
	return pkg, nil
}

// readEntry reads a whole (small) entry, refusing one above max.
func readEntry(f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, badPackage("%s exceeds %d bytes", f.Name, max)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, badPackage("%s: %v", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, badPackage("%s: %v", f.Name, err)
	}
	if int64(len(b)) > max {
		return nil, badPackage("%s exceeds %d bytes", f.Name, max)
	}
	return b, nil
}

func validText(s string, maxRunes int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= maxRunes
}

func (p *handoffPackage) readManifest() error {
	f := p.files["manifest.json"]
	if f == nil {
		return badPackage("manifest.json is missing")
	}
	raw, err := readEntry(f, maxHandoffManifestBytes)
	if err != nil {
		return err
	}
	var head struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return badPackage("manifest.json is not valid JSON")
	}
	if head.Format != wire.RoomExportFormat {
		return badPackage("unsupported package format %q (this server reads %s)", head.Format, wire.RoomExportFormat)
	}
	if err := json.Unmarshal(raw, &p.manifest); err != nil {
		return badPackage("manifest.json does not match %s: %v", wire.RoomExportFormat, err)
	}
	m := &p.manifest
	if err := roomctx.ValidateID(m.Room.ID); err != nil {
		return badPackage("manifest room.id is not a valid room id")
	}
	if !validText(m.Room.Name, 255) || !validText(m.Room.Topic, 500) {
		return badPackage("manifest room name or topic is too long")
	}
	if !validText(m.ExportedBy, 128) {
		return badPackage("manifest exportedBy is invalid")
	}
	if len(m.Channels) > maxHandoffChannels {
		return badPackage("too many channels (max %d)", maxHandoffChannels)
	}
	for _, c := range m.Channels {
		if c.ID == "" || !validText(c.ID, 64) || p.channelIDs[c.ID] {
			return badPackage("manifest channel ids must be unique and non-empty")
		}
		if strings.TrimSpace(c.Name) == "" || !validText(c.Name, 255) {
			return badPackage("manifest channel %q has an invalid name", c.ID)
		}
		p.channelIDs[c.ID] = true
		switch c.Kind {
		case "":
		case wire.RoomExportChannelPeople:
			if c.IsDefault || p.peopleChannel != "" {
				return badPackage("manifest people chat must be unique and not the default channel")
			}
			p.peopleChannel = c.ID
		default:
			return badPackage("manifest channel %q has an unknown kind %q", c.ID, c.Kind)
		}
		if c.IsDefault {
			if p.defaultChannel != "" {
				return badPackage("manifest lists more than one default channel")
			}
			p.defaultChannel = c.ID
		}
		p.channels = append(p.channels, c)
	}
	if len(m.Members) > maxHandoffMembers {
		return badPackage("too many members (max %d)", maxHandoffMembers)
	}
	for _, mem := range m.Members {
		if strings.TrimSpace(mem.UserID) == "" || !validText(mem.UserID, 128) || !validText(mem.Role, 16) {
			return badPackage("manifest member entries need a userId (max 128) and a role")
		}
	}
	return nil
}

func (p *handoffPackage) readAttachments() error {
	for name, f := range p.files {
		if !strings.HasPrefix(name, "attachments/") || !strings.HasSuffix(name, "/attachment.json") {
			continue
		}
		raw, err := readEntry(f, maxHandoffMetaBytes)
		if err != nil {
			return err
		}
		var a wire.RoomExportAttachment
		if err := json.Unmarshal(raw, &a); err != nil {
			return badPackage("%s is not valid JSON", name)
		}
		dir := strings.TrimSuffix(name, "attachment.json")
		if a.ID == "" || !validText(a.ID, 128) || dir != "attachments/"+packageSegment(a.ID)+"/" {
			return badPackage("%s: id does not match its directory", name)
		}
		if _, dup := p.attachments[a.ID]; dup {
			return badPackage("attachment %q is listed twice", a.ID)
		}
		if !strings.HasPrefix(a.File, dir) || p.files[a.File] == nil || a.File == name {
			return badPackage("%s: content file %q is missing", name, a.File)
		}
		if strings.TrimSpace(a.ContentType) == "" || !validText(a.Filename, 512) {
			return badPackage("%s: filename/contentType invalid", name)
		}
		if a.SHA256 != "" {
			sum, err := hashEntry(p.files[a.File])
			if err != nil {
				return err
			}
			if sum != a.SHA256 {
				return badPackage("attachment %q content does not match its sha256", a.ID)
			}
		}
		p.attachments[a.ID] = a
		p.attachmentIDs = append(p.attachmentIDs, a.ID)
	}
	sort.Strings(p.attachmentIDs)
	return nil
}

func hashEntry(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", badPackage("%s: %v", f.Name, err)
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", badPackage("%s: %v", f.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *handoffPackage) readArtifacts() error {
	for name, f := range p.files {
		if !strings.HasPrefix(name, "artifacts/") || !strings.HasSuffix(name, "/artifact.json") {
			continue
		}
		if len(p.artifacts) >= maxHandoffArtifacts {
			return badPackage("too many artifacts (max %d)", maxHandoffArtifacts)
		}
		raw, err := readEntry(f, maxHandoffManifestBytes)
		if err != nil {
			return err
		}
		var a wire.RoomExportArtifact
		if err := json.Unmarshal(raw, &a); err != nil {
			return badPackage("%s is not valid JSON", name)
		}
		dir := strings.TrimSuffix(name, "artifact.json")
		if a.ArtifactID == "" || dir != "artifacts/"+packageSegment(a.ArtifactID)+"/" {
			return badPackage("%s: artifactId does not match its directory", name)
		}
		if !validArtifactKind(a.Kind) {
			return badPackage("artifact %q has an unsupported kind %q", a.ArtifactID, a.Kind)
		}
		if strings.TrimSpace(a.Title) == "" || !validText(a.Title, maxArtifactTitleRunes) || !validText(a.Language, 64) {
			return badPackage("artifact %q has an invalid title or language", a.ArtifactID)
		}
		if !validText(a.FileName, maxArtifactFileNameRunes) || !validMIME(a.MimeType) {
			return badPackage("artifact %q has an invalid file name or mime type", a.ArtifactID)
		}
		if a.ChannelID != "" && !p.channelIDs[a.ChannelID] {
			return badPackage("artifact %q references unknown channel %q", a.ArtifactID, a.ChannelID)
		}
		if len(a.Versions) > maxHandoffArtifactVersions {
			return badPackage("artifact %q has too many versions (max %d)", a.ArtifactID, maxHandoffArtifactVersions)
		}
		sort.Slice(a.Versions, func(i, j int) bool { return a.Versions[i].Version < a.Versions[j].Version })
		prev := 0
		for _, v := range a.Versions {
			if v.Version <= prev {
				return badPackage("artifact %q has duplicate or non-positive versions", a.ArtifactID)
			}
			prev = v.Version
			vf := p.files[v.File]
			if !strings.HasPrefix(v.File, dir) || vf == nil || v.File == name {
				return badPackage("artifact %q v%d: content file %q is missing", a.ArtifactID, v.Version, v.File)
			}
			if !validText(v.Summary, 512) || !validText(v.AuthorUserID, 128) || !validMIME(v.MimeType) {
				return badPackage("artifact %q v%d: summary, author or mime type invalid", a.ArtifactID, v.Version)
			}
			if siteTypeMismatch(a.Kind, v.MimeType) || (a.Kind == store.ArtifactKindSite && v.Storage != store.ArtifactStorageBlob) {
				return badPackage("artifact %q v%d: a site version is a blob of type %s, and only a site carries that type", a.ArtifactID, v.Version, tarbundle.SiteMIME)
			}
			switch v.Storage {
			case "", store.ArtifactStorageInline:
				content, err := readEntry(vf, maxArtifactContentBytes)
				if err != nil {
					return err
				}
				if v.ContentHash != "" && store.HashArtifactContent(string(content)) != v.ContentHash {
					return badPackage("artifact %q v%d content does not match its hash", a.ArtifactID, v.Version)
				}
			case store.ArtifactStorageBlob:
				// Streamed, never buffered: a blob version may be up to the
				// deployment's artifact cap.
				if vf.UncompressedSize64 > uint64(p.artifactMaxBytes) {
					return badPackage("%s exceeds %d bytes", vf.Name, p.artifactMaxBytes)
				}
				sum, err := hashEntry(vf)
				if err != nil {
					return err
				}
				if sum != v.ContentHash {
					return badPackage("artifact %q v%d content does not match its hash", a.ArtifactID, v.Version)
				}
			default:
				return badPackage("artifact %q v%d has an unsupported storage %q", a.ArtifactID, v.Version, v.Storage)
			}
		}
		p.artifacts = append(p.artifacts, a)
	}
	sort.Slice(p.artifacts, func(i, j int) bool { return p.artifacts[i].CreatedAtMs < p.artifacts[j].CreatedAtMs })
	return nil
}

// eachMessage streams messages.jsonl, one validated row at a time.
func (p *handoffPackage) eachMessage(fn func(wire.FeedMessage) error) error {
	rc, err := p.messages.Open()
	if err != nil {
		return badPackage("messages.jsonl: %v", err)
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), maxHandoffLineBytes)
	line := 0
	var prev int64
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var m wire.FeedMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return badPackage("messages.jsonl line %d is not valid JSON", line)
		}
		if !wire.PersistedFeedKind(m.Kind) {
			return badPackage("messages.jsonl line %d: unsupported kind %q", line, m.Kind)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(m.Payload, &obj); err != nil || obj == nil {
			return badPackage("messages.jsonl line %d: payload must be a JSON object", line)
		}
		if m.ChannelID != "" && !p.channelIDs[m.ChannelID] {
			return badPackage("messages.jsonl line %d references unknown channel %q", line, m.ChannelID)
		}
		// The people chat is human-only: an agent reply or an agent-addressed
		// message there is a forged package, never something an export holds.
		if p.peopleChannel != "" && m.ChannelID == p.peopleChannel && agentFeedRow(m) {
			return badPackage("messages.jsonl line %d: agent message in the people chat", line)
		}
		if m.Sequence <= prev {
			return badPackage("messages.jsonl must be in ascending sequence order (line %d)", line)
		}
		prev = m.Sequence
		if err := fn(m); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return badPackage("messages.jsonl line %d exceeds %d bytes", line+1, maxHandoffLineBytes)
		}
		return badPackage("messages.jsonl: %v", err)
	}
	return nil
}

func (p *handoffPackage) scanMessages() error {
	p.messages = p.files["messages.jsonl"]
	if p.messages == nil {
		return badPackage("messages.jsonl is missing")
	}
	return p.eachMessage(func(wire.FeedMessage) error {
		p.messageCount++
		if p.messageCount > maxHandoffMessages {
			return badPackage("too many messages (max %d)", maxHandoffMessages)
		}
		return nil
	})
}

// importedRoomID derives the new room's id from the source id plus a random
// suffix; the source id is a valid room id, so the result stays valid.
func importedRoomID(source string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	base := source
	for len(base) > 40 {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = "room"
	}
	return base + "-" + hex.EncodeToString(b)
}

// importPackage writes a validated package into a new room.
func (s *Service) importPackage(ctx context.Context, user string, pkg *handoffPackage) (res wire.RoomImportResult, err error) {
	now := s.Now()
	m := pkg.manifest
	name := strings.TrimSpace(m.Room.Name)
	if name == "" {
		name = m.Room.ID
	}
	defName := "general"
	for _, c := range pkg.channels {
		if c.ID == pkg.defaultChannel {
			defName = c.Name
		}
	}
	var roomID string
	var def store.RoomChannel
	for attempt := 0; ; attempt++ {
		roomID = importedRoomID(m.Room.ID)
		def, err = s.createRoom(ctx, store.Room{
			RoomID: roomID, Name: name, Description: m.Room.Topic,
			Visibility: store.VisibilityPrivate, SyncMode: store.NormalizeSyncMode(""),
			CreatedAtMs: now,
		}, user, defName)
		if errors.Is(err, store.ErrRoomExists) && attempt < 3 {
			continue
		}
		if err != nil {
			return res, fmt.Errorf("create room: %w", err)
		}
		break
	}
	defer func() {
		if err != nil {
			if derr := s.Store.DeleteRoom(context.WithoutCancel(ctx), roomID); derr != nil {
				log.Printf("room: import cleanup of %s failed: %v", roomID, derr)
			}
		}
	}()

	// Channels: the default maps onto the new room's default; every other
	// channel is recreated (created by the importer: channel creator is a
	// rename authorization, never taken from the file).
	channelMap := map[string]string{"": def.ChannelID}
	if pkg.defaultChannel != "" {
		channelMap[pkg.defaultChannel] = def.ChannelID
	}
	for _, c := range pkg.channels {
		if c.ID == pkg.defaultChannel {
			continue
		}
		var ch store.RoomChannel
		if c.ID == pkg.peopleChannel {
			ch, err = s.Channels.EnsurePeopleChannel(ctx, roomID, user, now)
		} else {
			ch, err = s.createChannel(ctx, roomID, c.Name, user)
		}
		if err != nil {
			return res, fmt.Errorf("create channel: %w", err)
		}
		channelMap[c.ID] = ch.ChannelID
	}

	if res.ImportedArtifacts, err = s.importArtifacts(ctx, user, roomID, pkg, channelMap); err != nil {
		return res, err
	}
	attMap, skipped, err := s.importAttachments(ctx, user, roomID, pkg)
	if err != nil {
		return res, err
	}
	res.ImportedAttachments = len(attMap)
	res.SkippedAttachments = skipped
	if res.ImportedMessages, err = s.importMessages(ctx, user, roomID, now, pkg, channelMap, attMap); err != nil {
		return res, err
	}

	suggested := make([]wire.RoomExportMember, 0, len(m.Members))
	for _, mem := range m.Members {
		if mem.UserID != user {
			suggested = append(suggested, mem)
		}
	}
	info := wire.RoomHandoffInfo{
		RoomID: roomID, SourceRoomID: m.Room.ID, SourceRoomName: name,
		SourceExportedAtMs: m.ExportedAtMs, SourceExportedBy: m.ExportedBy,
		ImportedBy: user, ImportedAtMs: now, SuggestedMembers: suggested,
	}
	if err = s.RoomImports.PutRoomImport(ctx, info); err != nil {
		return res, fmt.Errorf("record provenance: %w", err)
	}
	res.RoomID, res.Name, res.Handoff = roomID, name, info
	return res, nil
}

// importArtifacts recreates every artifact with its version history in order.
// Version authors and timestamps are kept as attribution; the artifact's
// creator is the importer.
func (s *Service) importArtifacts(ctx context.Context, user, roomID string, pkg *handoffPackage, channelMap map[string]string) (int, error) {
	for _, a := range pkg.artifacts {
		channelID := ""
		if a.ChannelID != "" {
			channelID = channelMap[a.ChannelID]
		}
		art, err := s.Artifacts.PutArtifact(ctx, store.Artifact{
			ArtifactID: ids.New("art_"), RoomID: roomID, ChannelID: channelID, Kind: a.Kind,
			Title: strings.TrimSpace(a.Title), Language: strings.TrimSpace(a.Language),
			FileName:  sanitizeArtifactFileName(a.FileName),
			CreatedBy: user, CreatedAtMs: a.CreatedAtMs, UpdatedAtMs: a.CreatedAtMs,
		})
		if err != nil {
			return 0, fmt.Errorf("artifact %s: %w", a.ArtifactID, err)
		}
		base := art.CurrentVersion
		for _, v := range a.Versions {
			author := v.AuthorUserID
			if author == "" {
				author = user
			}
			ver, err := s.importArtifactVersion(ctx, user, pkg.files[v.File], v)
			if err != nil {
				return 0, fmt.Errorf("artifact %s v%d: %w", a.ArtifactID, v.Version, err)
			}
			ver.Summary, ver.AuthorUserID, ver.CreatedAtMs = v.Summary, author, v.CreatedAtMs
			updated, _, err := s.Artifacts.PublishArtifactVersion(ctx, art.ArtifactID, ver, base)
			if err != nil {
				return 0, fmt.Errorf("artifact %s v%d: %w", a.ArtifactID, v.Version, err)
			}
			base = updated.CurrentVersion
		}
	}
	return len(pkg.artifacts), nil
}

// importArtifactVersion turns one packaged version into a version to
// publish: inline text as-is, or the bytes re-stored in the blob store (as
// the importing user) with the hash re-checked against the stored blob.
// Source job/message links are not imported: they name the source room's
// jobs and sequences.
func (s *Service) importArtifactVersion(ctx context.Context, user string, f *zip.File, v wire.RoomExportArtifactVersion) (store.ArtifactVersion, error) {
	if v.Storage != store.ArtifactStorageBlob {
		content, err := readEntry(f, maxArtifactContentBytes)
		if err != nil {
			return store.ArtifactVersion{}, err
		}
		return store.ArtifactVersion{Content: string(content), MimeType: store.MediaType(v.MimeType)}, nil
	}
	if s.Blobs == nil {
		return store.ArtifactVersion{}, fmt.Errorf("artifact blob store not configured")
	}
	rc, err := f.Open()
	if err != nil {
		return store.ArtifactVersion{}, badPackage("%s: %v", f.Name, err)
	}
	defer rc.Close()
	ref, err := s.Blobs.PutBlob(ctx, user, rc)
	if err != nil {
		return store.ArtifactVersion{}, err
	}
	if ref.SHA256 != v.ContentHash {
		return store.ArtifactVersion{}, badPackage("%s does not match its hash", f.Name)
	}
	mimeType := store.MediaType(v.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return store.ArtifactVersion{
		Storage: store.ArtifactStorageBlob, BlobKey: ref.Key, ContentHash: ref.SHA256,
		SizeBytes: ref.Size, MimeType: mimeType,
	}, nil
}

// validMIME accepts an empty or a well-formed, bounded media type.
func validMIME(m string) bool {
	if m == "" {
		return true
	}
	if len(m) > 128 {
		return false
	}
	_, _, err := mime.ParseMediaType(m)
	return err == nil
}

// importAttachments re-uploads every packaged attachment through realtime's
// member upload route (type gate, sniffing, size cap, content-addressed blob
// store). An attachment realtime refuses (4xx) is skipped and reported; a
// backend failure aborts the import.
func (s *Service) importAttachments(ctx context.Context, user, roomID string, pkg *handoffPackage) (map[string]attachmentRef, []string, error) {
	out := map[string]attachmentRef{}
	skipped := []string{}
	for _, id := range pkg.attachmentIDs {
		a := pkg.attachments[id]
		rc, err := pkg.files[a.File].Open()
		if err != nil {
			return nil, nil, badPackage("%s: %v", a.File, err)
		}
		newID, err := s.Handoff.UploadAttachment(ctx, user, roomID, a.Filename, a.ContentType, rc)
		rc.Close()
		if err != nil {
			var se *realtime.StatusError
			if errors.As(err, &se) && se.Status >= 400 && se.Status < 500 {
				log.Printf("room: import into %s: attachment %s refused (%d), skipped", roomID, id, se.Status)
				skipped = append(skipped, id)
				continue
			}
			return nil, nil, fmt.Errorf("attachment %s: %w", id, err)
		}
		out[id] = attachmentRef{ID: newID, Filename: a.Filename, ContentType: a.ContentType, Size: a.Size}
	}
	return out, skipped, nil
}

// importMessages streams messages.jsonl into realtime in bounded batches,
// rewriting room-local references (channel ids, attachment ids, the agent
// payload's workspace/channel) onto the new room.
func (s *Service) importMessages(ctx context.Context, user, roomID string, now int64, pkg *handoffPackage, channelMap map[string]string, attMap map[string]attachmentRef) (int, error) {
	batch := wire.RoomImportBatch{FromRoomID: pkg.manifest.Room.ID, FromRoomName: pkg.manifest.Room.Name, ImportedAtMs: now}
	size := 0
	total := 0
	flush := func() error {
		if len(batch.Messages) == 0 {
			return nil
		}
		reply, err := s.Handoff.ImportMessages(ctx, user, roomID, batch)
		if err != nil {
			return err
		}
		total += reply.Imported
		batch.Messages = batch.Messages[:0]
		size = 0
		return nil
	}
	// Stay well under realtime's per-request cap: the JSON envelope re-encodes
	// each row, so budget half of it.
	budget := wire.MaxImportBatchBytes / 2
	err := pkg.eachMessage(func(m wire.FeedMessage) error {
		row, err := rewriteImportedMessage(m, roomID, channelMap, attMap)
		if err != nil {
			return err
		}
		n := len(row.Payload) + 256
		if len(batch.Messages) >= wire.MaxImportBatchMessages || (len(batch.Messages) > 0 && size+n > budget) {
			if err := flush(); err != nil {
				return err
			}
		}
		batch.Messages = append(batch.Messages, row)
		size += n
		return nil
	})
	if err != nil {
		return total, err
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

// rewriteImportedMessage maps one package row onto the new room: its channel,
// the attachment ids a chat embeds (dropping ones that were not imported), and
// an agent batch's workspaceId/channelId. Every other payload field —
// including the original attribution — is kept verbatim.
func rewriteImportedMessage(m wire.FeedMessage, roomID string, channelMap map[string]string, attMap map[string]attachmentRef) (wire.FeedMessage, error) {
	m.ChannelID = channelMap[m.ChannelID]
	var p map[string]json.RawMessage
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return m, badPackage("message %d payload: %v", m.Sequence, err)
	}
	// A package of an imported room carries the previous marker; realtime
	// stamps the current one.
	delete(p, "imported")
	switch m.Kind {
	case wire.KindChatMessage:
		if raw, ok := p["attachments"]; ok {
			var refs []attachmentRef
			_ = json.Unmarshal(raw, &refs)
			kept := make([]attachmentRef, 0, len(refs))
			for _, a := range refs {
				if n, ok := attMap[a.ID]; ok {
					kept = append(kept, n)
				}
			}
			if len(kept) == 0 {
				delete(p, "attachments")
			} else {
				b, _ := json.Marshal(kept)
				p["attachments"] = b
			}
		}
	case wire.KindAgentMessages:
		if _, ok := p["workspaceId"]; ok {
			b, _ := json.Marshal(roomID)
			p["workspaceId"] = b
		}
		if _, ok := p["channelId"]; ok {
			b, _ := json.Marshal(m.ChannelID)
			p["channelId"] = b
		}
	}
	out, err := json.Marshal(p)
	if err != nil {
		return m, err
	}
	m.Payload = out
	return m, nil
}
