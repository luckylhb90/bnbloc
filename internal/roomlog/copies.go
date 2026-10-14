package roomlog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Deliverable copies (docs/bugs/2026-10-13-agent-loses-its-workspace.md):
// whatever happens to a workspace, an agent can recover the source of the
// room's deliverables. The pull pass copies each listed deliverable's
// latest version into the workspace, read-only by convention (.room/ is
// bnbloc's: write-denied in sandboxed drives, refused by the cores' agent
// file writes), as its file tree for a site and as one file otherwise:
// DeliverablesDir + <artifact id> + "/". A copy is written only when its
// version changed since the workspace's last pass (CopyState, kept with the
// workspace, not the session), bounded per deliverable and per pass; a
// deliverable over the bound is listed in .room/artifacts.md with why it
// has no copy. To change one, an agent copies it into its own working tree:
// .room/ is never published, synced or packed (core.BnblocOwnedPath).

// DeliverablesDir holds the workspace's copies of the room's deliverables.
const DeliverablesDir = core.RoomContextDir + "deliverables/"

// Copy bounds: one deliverable's version (a site's bundle, a file), and
// what one pass writes (the rest are copied over the next turns).
const (
	MaxDeliverableCopyBytes     = 8 << 20
	MaxDeliverableCopyPassBytes = 32 << 20
)

// CopiedVersion is what a workspace holds of one deliverable: the version
// copied and its file count, or the size of a version too large to copy.
type CopiedVersion struct {
	Version  int   `json:"version"`
	Files    int   `json:"files,omitempty"`
	TooLarge int64 `json:"tooLarge,omitempty"`
}

// CopyState is a workspace's copies by artifact id.
type CopyState map[string]CopiedVersion

// Copy is one listed deliverable's copy as .room/artifacts.md describes
// it: copied (Dir), too large (TooLarge), or not copied this pass
// (Pending: unreadable, or over the pass bound; retried next turn).
type Copy struct {
	Dir      string
	Version  int
	Files    int
	TooLarge int64
	Pending  bool
}

// WriteFile writes one file into the workspace (the core's bytes writer).
type WriteFile func(ctx context.Context, name string, data []byte) error

// CopyDir is where a deliverable's copy lives ("" for an id that cannot
// name a directory).
func CopyDir(artifactID string) string {
	if !copyIDRE.MatchString(artifactID) {
		return ""
	}
	return DeliverablesDir + artifactID + "/"
}

var copyIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// copyBundles holds the site bundles a pass parsed (pinned by their
// version's content hash, the only way a stored bundle is read).
var copyBundles = tarbundle.NewCache(MaxDeliverableCopyBytes)

// CopyDeliverables writes the listed deliverables whose latest version the
// workspace does not hold yet (have) and returns every listed one's copy
// and the workspace's next state. A deliverable not copied this pass keeps
// its previous entry, so the next pass retries it; one no longer listed is
// dropped from the state (its files stay: nothing in .room/ is deleted).
func CopyDeliverables(ctx context.Context, fetch Fetch, room string, list []Deliverable, have CopyState, write WriteFile) (map[string]Copy, CopyState) {
	copies := make(map[string]Copy, len(list))
	next := CopyState{}
	budget := int64(MaxDeliverableCopyPassBytes)
	if len(list) > MaxListedDeliverables {
		// Copies are the newest deliverables'; an older one is read by id.
		list = list[:MaxListedDeliverables]
	}
	for _, d := range list {
		dir := CopyDir(d.ArtifactID)
		if dir == "" {
			copies[d.ArtifactID] = Copy{Pending: true}
			continue
		}
		prev, held := have[d.ArtifactID]
		if held && prev.Version == d.Version {
			next[d.ArtifactID] = prev
			copies[d.ArtifactID] = prev.copy(dir)
			continue
		}
		got, err := copyDeliverable(ctx, fetch, room, d, dir, &budget, write)
		if err != nil {
			// Not copied this pass: what the workspace held stays (an
			// older version's copy is still the source of that version).
			c := Copy{}
			if held {
				next[d.ArtifactID] = prev
				c = prev.copy(dir)
			}
			c.Pending = true
			copies[d.ArtifactID] = c
			continue
		}
		next[d.ArtifactID] = got
		copies[d.ArtifactID] = got.copy(dir)
	}
	return copies, next
}

func (c CopiedVersion) copy(dir string) Copy {
	if c.TooLarge > 0 {
		return Copy{Version: c.Version, TooLarge: c.TooLarge}
	}
	return Copy{Dir: dir, Version: c.Version, Files: c.Files}
}

// errOverPass: the pass's bound is spent; the deliverable waits for the
// next turn.
var errOverPass = fmt.Errorf("deliverable copies: the pass bound is spent")

// copyDeliverable writes one deliverable's latest version under dir.
func copyDeliverable(ctx context.Context, fetch Fetch, room string, d Deliverable, dir string, budget *int64, write WriteFile) (CopiedVersion, error) {
	versionPath := deliverablesPath(room) + "/" + url.PathEscape(d.ArtifactID) + "/versions/" + strconv.Itoa(d.Version)
	raw, err := fetch(ctx, versionPath)
	if err != nil {
		return CopiedVersion{}, err
	}
	var reply wire.ArtifactVersionReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return CopiedVersion{}, err
	}
	v := reply.Version
	size := v.SizeBytes
	if v.Storage != store.ArtifactStorageBlob {
		size = int64(len(v.Content))
	}
	if size > MaxDeliverableCopyBytes {
		return CopiedVersion{Version: d.Version, TooLarge: size}, nil
	}
	if size > *budget {
		return CopiedVersion{}, errOverPass
	}
	*budget -= size
	content := func() ([]byte, error) { return fetch(ctx, versionPath+"/content") }
	if d.Kind == store.ArtifactKindSite {
		b, err := copyBundles.LoadPinned(v.ContentHash, tarbundle.SiteLimits, content)
		if err != nil {
			return CopiedVersion{}, err
		}
		for _, p := range b.Paths() {
			data, _ := b.File(p)
			if err := write(ctx, dir+p, data); err != nil {
				return CopiedVersion{}, err
			}
		}
		return CopiedVersion{Version: d.Version, Files: len(b.Paths())}, nil
	}
	data := []byte(v.Content)
	if v.Storage == store.ArtifactStorageBlob {
		if data, err = content(); err != nil {
			return CopiedVersion{}, err
		}
	}
	if err := write(ctx, dir+copyFileName(d), data); err != nil {
		return CopiedVersion{}, err
	}
	return CopiedVersion{Version: d.Version, Files: 1}, nil
}

// copyFileName is the file a non-site deliverable's copy is written as: its
// file name, else its title's last segment, when either is a plain file
// name; else "content".
func copyFileName(d Deliverable) string {
	for _, name := range []string{d.FileName, d.Title} {
		if name == "" {
			continue
		}
		base := path.Base(name)
		if clean, ok := core.CleanRelPath(base); ok && clean == base && base != "." {
			return base
		}
	}
	return "content"
}
