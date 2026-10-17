package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/media"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An agent's image call reads room pictures as the job's requester
// (docs/prd/2026-10-17-image-generation-redesign.md §2.2–2.3): the file
// to edit (ArtifactID) and the references (Inputs, room files art_… and
// attachments att_…). Nothing is fetched by URL.

// imageEdit is the room file an edit changes: its id, the version read
// (the publish's base), its name and its bytes.
type imageEdit struct {
	ArtifactID string
	Version    int
	FileName   string
	Input      media.ImageInput
}

// errImageInput is a refused input: its message names it.
type errImageInput struct{ msg string }

func (e errImageInput) Error() string { return e.msg }

// readEditTarget reads the room image to edit, as the member in claims.
func (s *Service) readEditTarget(ctx context.Context, claims auth.Claims, roomID, artifactID string) (imageEdit, error) {
	img, art, err := s.readRoomImage(ctx, claims, roomID, artifactID)
	if err != nil {
		return imageEdit{}, err
	}
	return imageEdit{ArtifactID: art.ArtifactID, Version: art.CurrentVersion, FileName: firstNonEmpty(art.FileName, art.Title), Input: img}, nil
}

// readImageInputs reads the references, as the member in claims: at most
// wire.MaxMediaImageInputs room files and attachments of the room, each a
// PNG, JPEG or WebP image.
func (s *Service) readImageInputs(ctx context.Context, claims auth.Claims, roomID string, ids []string) ([]media.ImageInput, error) {
	if len(ids) > wire.MaxMediaImageInputs {
		return nil, errImageInput{fmt.Sprintf("inputs: at most %d", wire.MaxMediaImageInputs)}
	}
	out := make([]media.ImageInput, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(id, "att_"):
			img, err := s.readAttachmentImage(ctx, claims, roomID, id)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		case id != "":
			img, _, err := s.readRoomImage(ctx, claims, roomID, id)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		}
	}
	return out, nil
}

// readRoomImage reads a room file's current version as the member in
// claims, refusing what is not this room's or not an image.
func (s *Service) readRoomImage(ctx context.Context, claims auth.Claims, roomID, artifactID string) (media.ImageInput, wire.Artifact, error) {
	art, _, err := s.deliverableVersion(ctx, claims, roomID, artifactID)
	if errors.Is(err, errSiteNotFound) {
		return media.ImageInput{}, wire.Artifact{}, errImageInput{fmt.Sprintf("%s is not a file of this room you can read", artifactID)}
	}
	if err != nil {
		return media.ImageInput{}, wire.Artifact{}, err
	}
	path := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts/" + url.PathEscape(art.ArtifactID) + "/versions/" + strconv.Itoa(art.CurrentVersion) + "/content"
	data, err := s.roomBackendBytes(ctx, path, claims, wire.MaxMediaImageBytes)
	if err != nil {
		return media.ImageInput{}, wire.Artifact{}, err
	}
	mimeType := media.SniffImage(data)
	if mimeType == "" {
		return media.ImageInput{}, wire.Artifact{}, errImageInput{fmt.Sprintf("%s is not a PNG, JPEG or WebP image", firstNonEmpty(art.FileName, art.Title, artifactID))}
	}
	return media.ImageInput{Bytes: data, MimeType: mimeType}, art, nil
}

// readAttachmentImage reads a room attachment as the member in claims,
// refusing what is not an image.
func (s *Service) readAttachmentImage(ctx context.Context, claims auth.Claims, roomID, id string) (media.ImageInput, error) {
	data, found, err := s.attachmentRead(ctx, claims, roomID, id, "", wire.MaxMediaImageBytes)
	if err != nil {
		return media.ImageInput{}, err
	}
	mimeType := media.SniffImage(data)
	if !found || mimeType == "" {
		return media.ImageInput{}, errImageInput{fmt.Sprintf("%s is not an image attachment of this room you can read", id)}
	}
	return media.ImageInput{Bytes: data, MimeType: mimeType}, nil
}
