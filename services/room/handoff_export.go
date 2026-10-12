package room

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleExportRoom streams the room's handoff package as a zip. The archive is
// written while it is read (no temp file, no whole-room buffer): the feed is
// paged forward from realtime twice (messages.jsonl, then transcript.md up to
// the same last sequence), referenced attachments are streamed from realtime,
// artifacts come from the local store, and manifest.json — which carries the
// counts — is written last.
func (s *Service) handleExportRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	user := caller(r)
	if user == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	role, ok, err := s.Store.MemberRole(r.Context(), roomID, user)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	if !role.CanInvite() {
		httpx.Error(w, "only the room owner or an admin may export a handoff package", http.StatusForbidden)
		return
	}
	if s.Handoff == nil || s.Artifacts == nil || s.Channels == nil {
		httpx.Unavailable(w, "handoff packages are")
		return
	}
	rm, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		httpx.Error(w, "room not found", http.StatusNotFound)
		return
	}
	// Read the first feed page before committing to a 200: a realtime outage
	// is an HTTP error, not a truncated download.
	first, err := s.Handoff.Feed(r.Context(), user, roomID, 0, handoffFeedPage)
	if err != nil {
		obs.ServerError(w, "room history unavailable", err)
		httpx.Error(w, "room history unavailable", http.StatusBadGateway)
		return
	}
	now := s.Now()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", httpx.ContentDisposition("attachment",
		packageSegment(roomID)+"-handoff-"+time.UnixMilli(now).UTC().Format("20060102")+".zip"))
	zw := zip.NewWriter(w)
	ex := &roomExporter{
		svc: s, ctx: r.Context(), user: user, room: rm, zw: zw, now: now,
		attSeen: map[string]bool{}, agents: map[string]*wire.RoomExportAgent{}, sessions: map[string]*wire.RoomExportAgentSession{},
	}
	if err := ex.run(first); err != nil {
		log.Printf("room: export room=%s aborted: %v", roomID, err)
		// The 200 and part of the archive are already on the wire: abort the
		// connection so the client sees a failed download, never a
		// well-formed archive that silently lacks data.
		panic(http.ErrAbortHandler)
	}
	if err := zw.Close(); err != nil {
		log.Printf("room: export room=%s: finish archive: %v", roomID, err)
	}
}

type roomExporter struct {
	svc  *Service
	ctx  context.Context
	user string
	room store.Room
	zw   *zip.Writer
	now  int64

	channels       []store.RoomChannel
	channelNames   map[string]string
	peopleChannels map[string]bool
	lastSeq        int64
	messageCount   int
	attachments    []attachmentRef
	attSeen        map[string]bool
	agents         map[string]*wire.RoomExportAgent
	agentOrder     []string
	sessions       map[string]*wire.RoomExportAgentSession
	sessionOrder   []string
}

func (e *roomExporter) create(name string) (io.Writer, error) {
	return e.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.UnixMilli(e.now).UTC()})
}

func (e *roomExporter) writeJSON(name string, v any) error {
	fw, err := e.create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(fw)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (e *roomExporter) run(first []wire.FeedMessage) error {
	channels, err := e.svc.Channels.ListChannels(e.ctx, e.room.RoomID)
	if err != nil {
		return fmt.Errorf("channels: %w", err)
	}
	e.channels = channels
	e.channelNames = map[string]string{}
	e.peopleChannels = map[string]bool{}
	for _, c := range e.channels {
		e.channelNames[c.ChannelID] = c.Name
		if c.IsPeople() {
			e.peopleChannels[c.ChannelID] = true
		}
	}
	if err := e.writeMessages(first); err != nil {
		return fmt.Errorf("messages: %w", err)
	}
	if err := e.writeTranscript(); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	attachmentCount, err := e.writeAttachments()
	if err != nil {
		return fmt.Errorf("attachments: %w", err)
	}
	artifactCount, err := e.writeArtifacts()
	if err != nil {
		return fmt.Errorf("artifacts: %w", err)
	}
	m, err := e.manifest(attachmentCount, artifactCount)
	if err != nil {
		return fmt.Errorf("members: %w", err)
	}
	return e.writeJSON("manifest.json", m)
}

// eachFeed pages the room feed forward from the start, calling fn per row.
// first, when non-nil, is the already-fetched first page. bound > 0 stops at
// that sequence (the second pass covers exactly what the first one saw).
func (e *roomExporter) eachFeed(first []wire.FeedMessage, bound int64, fn func(wire.FeedMessage) error) error {
	page := first
	var after int64
	for {
		if page == nil {
			var err error
			if page, err = e.svc.Handoff.Feed(e.ctx, e.user, e.room.RoomID, after, handoffFeedPage); err != nil {
				return err
			}
		}
		for _, m := range page {
			if bound > 0 && m.Sequence > bound {
				return nil
			}
			if err := fn(m); err != nil {
				return err
			}
			after = m.Sequence
		}
		if len(page) < handoffFeedPage {
			return nil
		}
		page = nil
	}
}

func (e *roomExporter) writeMessages(first []wire.FeedMessage) error {
	fw, err := e.create("messages.jsonl")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(fw)
	enc.SetEscapeHTML(false)
	return e.eachFeed(first, 0, func(m wire.FeedMessage) error {
		e.observe(m)
		return enc.Encode(m)
	})
}

// observe accumulates the manifest's derived facts from one feed row: counts,
// referenced attachments, agents (owner/provider/core) and agent sessions.
func (e *roomExporter) observe(m wire.FeedMessage) {
	e.messageCount++
	e.lastSeq = m.Sequence
	p := decodeFeedPayload(m.Payload)
	switch m.Kind {
	case wire.KindChatMessage:
		for _, a := range p.Attachments {
			if a.ID != "" && !e.attSeen[a.ID] {
				e.attSeen[a.ID] = true
				e.attachments = append(e.attachments, a)
			}
		}
		if p.Recipient != nil && p.Recipient.Provider != "" {
			e.noteAgent(p.Recipient.OwnerID, p.Recipient.Provider, p.Recipient.Core)
		}
	case wire.KindAgentMessages:
		sid := p.AgentSessionID
		sess := e.sessions[sid]
		if sess == nil {
			sess = &wire.RoomExportAgentSession{AgentSessionID: sid, ChannelID: m.ChannelID, FirstAtMs: m.AtUnixMs}
			e.sessions[sid] = sess
			e.sessionOrder = append(e.sessionOrder, sid)
		}
		for _, inner := range p.Messages {
			sess.MessageCount++
			at := inner.CreatedAtUnixMs
			if at <= 0 {
				at = m.AtUnixMs
			}
			if sess.FirstAtMs <= 0 || at < sess.FirstAtMs {
				sess.FirstAtMs = at
			}
			if at > sess.LastAtMs {
				sess.LastAtMs = at
			}
			if sess.Owner == "" {
				sess.Owner = inner.Payload.OwnerUserID
			}
			if sess.Provider == "" {
				sess.Provider = inner.Payload.Provider
			}
			if inner.Payload.Provider != "" {
				e.noteAgent(inner.Payload.OwnerUserID, inner.Payload.Provider, "")
			}
		}
	}
}

func (e *roomExporter) noteAgent(owner, provider, core string) {
	key := owner + "\x00" + provider
	a := e.agents[key]
	if a == nil {
		a = &wire.RoomExportAgent{Owner: owner, Provider: provider}
		e.agents[key] = a
		e.agentOrder = append(e.agentOrder, key)
	}
	if a.Core == "" {
		a.Core = core
	}
}

func (e *roomExporter) writeTranscript() error {
	fw, err := e.create("transcript.md")
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(fw)
	name := e.room.Name
	if strings.TrimSpace(name) == "" {
		name = e.room.RoomID
	}
	fmt.Fprintf(bw, "# %s\n\n", name)
	if e.room.Description != "" {
		fmt.Fprintf(bw, "%s\n\n", e.room.Description)
	}
	fmt.Fprintf(bw, "_Handoff transcript of room `%s`, exported %s by %s. %d messages._\n\n",
		e.room.RoomID, transcriptTime(e.now), e.user, e.messageCount)
	if e.messageCount > 0 {
		err = e.eachFeed(nil, e.lastSeq, func(m wire.FeedMessage) error {
			e.renderTranscript(bw, m)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return bw.Flush()
}

func transcriptTime(ms int64) string {
	if ms <= 0 {
		return "unknown time"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC")
}

// renderTranscript writes one feed row: a chat as "**user** · time", an agent
// reply attributed to its owner/provider handle.
func (e *roomExporter) renderTranscript(bw *bufio.Writer, m wire.FeedMessage) {
	where := ""
	if e.peopleChannels[m.ChannelID] {
		where = " · people chat (not visible to agents)"
	} else if len(e.channels) > 1 {
		if n := e.channelNames[m.ChannelID]; n != "" {
			where = " · #" + n
		}
	}
	p := decodeFeedPayload(m.Payload)
	switch m.Kind {
	case wire.KindChatMessage:
		author := p.UserID
		if author == "" {
			author = "unknown"
		}
		to := ""
		if p.Recipient != nil && p.Recipient.Provider != "" {
			to = " → @" + agentHandle(p.Recipient.OwnerID, p.Recipient.Provider)
		}
		fmt.Fprintf(bw, "---\n\n**%s**%s · %s%s\n\n", author, to, transcriptTime(m.AtUnixMs), where)
		if t := strings.TrimSpace(p.Text); t != "" {
			fmt.Fprintf(bw, "%s\n\n", t)
		}
		if len(p.Attachments) > 0 {
			names := make([]string, 0, len(p.Attachments))
			for _, a := range p.Attachments {
				names = append(names, a.Filename)
			}
			fmt.Fprintf(bw, "_Attachments: %s_\n\n", strings.Join(names, ", "))
		}
	case wire.KindAgentMessages:
		for _, inner := range p.Messages {
			at := inner.CreatedAtUnixMs
			if at <= 0 {
				at = m.AtUnixMs
			}
			fmt.Fprintf(bw, "---\n\n**%s** (agent) · %s%s\n\n", agentHandle(inner.Payload.OwnerUserID, inner.Payload.Provider), transcriptTime(at), where)
			if t := strings.TrimSpace(inner.Payload.Text); t != "" {
				fmt.Fprintf(bw, "%s\n\n", t)
			}
		}
	}
}

// writeAttachments streams every attachment the exported messages reference
// from realtime into attachments/<id>/, hashing on the way. A blob realtime no
// longer has (404) is skipped: the package then simply lacks it, and an import
// drops the dangling reference.
func (e *roomExporter) writeAttachments() (int, error) {
	n := 0
	for _, a := range e.attachments {
		rc, err := e.svc.Handoff.OpenAttachment(e.ctx, e.user, e.room.RoomID, a.ID)
		if err != nil {
			var se *realtime.StatusError
			if errors.As(err, &se) && se.Status == http.StatusNotFound {
				log.Printf("room: export room=%s: attachment %s missing, skipped", e.room.RoomID, a.ID)
				continue
			}
			return n, err
		}
		dir := "attachments/" + packageSegment(a.ID) + "/"
		file := dir + packageSegment(a.Filename)
		if file == dir+"attachment.json" {
			file = dir + "_attachment.json"
		}
		fw, err := e.create(file)
		if err != nil {
			rc.Close()
			return n, err
		}
		h := sha256.New()
		size, err := io.Copy(io.MultiWriter(fw, h), rc)
		rc.Close()
		if err != nil {
			return n, err
		}
		if err := e.writeJSON(dir+"attachment.json", wire.RoomExportAttachment{
			ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, Size: size,
			SHA256: hex.EncodeToString(h.Sum(nil)), File: file,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// artifactExt names an artifact version file for a human browsing the archive.
func artifactExt(kind string) string {
	switch kind {
	case store.ArtifactKindMarkdown:
		return ".md"
	case store.ArtifactKindHTML, store.ArtifactKindApp:
		return ".html"
	default:
		return ".txt"
	}
}

// blobFileExt names a blob version file by the artifact's file extension
// (".bin" when it has none usable as an archive name).
func blobFileExt(a store.Artifact) string {
	ext := strings.ToLower(path.Ext(firstNonEmptyString(a.FileName, a.Title)))
	if len(ext) < 2 || len(ext) > 12 || packageSegment(ext[1:]) != ext[1:] {
		return ".bin"
	}
	return ext
}

// writeArtifacts writes every room artifact with its full version history:
// artifacts/<id>/v<N><ext> per version plus artifacts/<id>/artifact.json.
// Blob versions are copied out of the blob store as raw bytes. Thumbnails
// are derived data and are not exported.
func (e *roomExporter) writeArtifacts() (int, error) {
	var arts []store.Artifact
	q := store.ArtifactQuery{RoomID: e.room.RoomID, Limit: 200}
	for len(arts) < maxHandoffArtifacts {
		page, err := e.svc.Artifacts.ListArtifacts(e.ctx, q)
		if err != nil {
			return 0, err
		}
		arts = append(arts, page...)
		if len(page) < q.Limit {
			break
		}
		last := page[len(page)-1]
		q.BeforeUpdatedMs, q.BeforeID = last.UpdatedAtMs, last.ArtifactID
	}
	if len(arts) > maxHandoffArtifacts {
		arts = arts[:maxHandoffArtifacts]
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].CreatedAtMs < arts[j].CreatedAtMs })
	for _, a := range arts {
		versions, err := e.svc.Artifacts.ListArtifactVersions(e.ctx, a.ArtifactID, maxHandoffArtifactVersions)
		if err != nil {
			return 0, err
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
		dir := "artifacts/" + packageSegment(a.ArtifactID) + "/"
		meta := wire.RoomExportArtifact{
			ArtifactID: a.ArtifactID, ChannelID: a.ChannelID, Kind: a.Kind, Title: a.Title, Language: a.Language,
			MimeType: a.MimeType, FileName: a.FileName,
			CurrentVersion: a.CurrentVersion, CreatedBy: a.CreatedBy, CreatedAtMs: a.CreatedAtMs, UpdatedAtMs: a.UpdatedAtMs,
			Versions: make([]wire.RoomExportArtifactVersion, 0, len(versions)),
		}
		for _, v := range versions {
			full, err := e.svc.Artifacts.GetArtifactVersion(e.ctx, a.ArtifactID, v.Version)
			if err != nil {
				return 0, fmt.Errorf("artifact %s v%d: %w", a.ArtifactID, v.Version, err)
			}
			ext := artifactExt(a.Kind)
			if full.Storage == store.ArtifactStorageBlob {
				ext = blobFileExt(a)
			}
			file := fmt.Sprintf("%sv%d%s", dir, v.Version, ext)
			if err := e.writeArtifactVersion(file, full); err != nil {
				return 0, fmt.Errorf("artifact %s v%d: %w", a.ArtifactID, v.Version, err)
			}
			meta.Versions = append(meta.Versions, wire.RoomExportArtifactVersion{
				Version: v.Version, ContentHash: full.ContentHash, Storage: full.Storage,
				MimeType: full.MimeType, SizeBytes: full.SizeBytes,
				SourceJobID: full.SourceJobID, SourceSeq: full.SourceSeq,
				Summary: full.Summary, AuthorUserID: full.AuthorUserID, CreatedAtMs: full.CreatedAtMs, File: file,
			})
		}
		if err := e.writeJSON(dir+"artifact.json", meta); err != nil {
			return 0, err
		}
	}
	return len(arts), nil
}

// writeArtifactVersion writes one version's bytes: inline content, or the
// blob streamed from the blob store (as the exporting user).
func (e *roomExporter) writeArtifactVersion(file string, v store.ArtifactVersion) error {
	fw, err := e.create(file)
	if err != nil {
		return err
	}
	if v.Storage != store.ArtifactStorageBlob {
		_, err = io.WriteString(fw, v.Content)
		return err
	}
	if e.svc.Blobs == nil {
		return fmt.Errorf("artifact blob store not configured")
	}
	rc, _, err := e.svc.Blobs.OpenBlob(e.ctx, v.BlobKey)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(fw, rc)
	return err
}

func (e *roomExporter) manifest(attachmentCount, artifactCount int) (wire.RoomExportManifest, error) {
	m := wire.RoomExportManifest{
		Format:       wire.RoomExportFormat,
		ExportedAtMs: e.now,
		ExportedBy:   e.user,
		Room: wire.RoomExportRoom{
			ID: e.room.RoomID, Name: e.room.Name, Topic: e.room.Description, CreatedAtMs: e.room.CreatedAtMs,
		},
		Channels:        make([]wire.RoomExportChannel, 0, len(e.channels)),
		Members:         []wire.RoomExportMember{},
		Agents:          make([]wire.RoomExportAgent, 0, len(e.agentOrder)),
		AgentSessions:   make([]wire.RoomExportAgentSession, 0, len(e.sessionOrder)),
		MessageCount:    e.messageCount,
		ArtifactCount:   artifactCount,
		AttachmentCount: attachmentCount,
	}
	for _, c := range e.channels {
		ch := wire.RoomExportChannel{ID: c.ChannelID, Name: c.Name, IsDefault: c.IsDefault}
		if c.IsPeople() {
			ch.Kind = wire.RoomExportChannelPeople
		}
		m.Channels = append(m.Channels, ch)
	}
	members, err := e.svc.Store.Members(e.ctx, e.room.RoomID)
	if err != nil {
		return m, err
	}
	for _, mem := range members {
		// An agent host is not a person of the room; an import would make it
		// a human member.
		if mem.AgentHost {
			continue
		}
		m.Members = append(m.Members, wire.RoomExportMember{UserID: mem.UserID, Role: string(mem.Role)})
	}
	for _, k := range e.agentOrder {
		m.Agents = append(m.Agents, *e.agents[k])
	}
	for _, k := range e.sessionOrder {
		m.AgentSessions = append(m.AgentSessions, *e.sessions[k])
	}
	return m, nil
}
