package roomlog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Agent-facing room deliverables: the room's artifacts as an agent reads
// them, each named by the agent that produced it. A deliverable is a file an
// agent published from ITS OWN workspace (or a person uploaded); an agent
// reading the list must be able to tell that a file another agent produced
// is not in its workspace (docs/bugs/2026-10-12-agent-misreads-other-agents-
// work.md). The producer of a version is its source job's agent (the run
// record, GET /v1/rooms/{id}/jobs/{jobId}), or the person who published it
// when no job did: VersionProducer is the one resolution, used by
// .room/artifacts.md and the room tools' read_artifact alike.

// MaxListedDeliverables bounds what .room/artifacts.md details (producer,
// copy) and what is copied into a workspace: the room's newest-updated
// deliverables. The index lists older ones too, by id
// (docs/prd/2026-10-14-multi-member-rooms.md rule 7), up to
// MaxIndexedDeliverables.
const MaxListedDeliverables = 30

// MaxIndexedDeliverables bounds the deliverables .room/artifacts.md lists.
const MaxIndexedDeliverables = 1000

// deliverablesPage is one page of the index's listing.
const deliverablesPage = 200

// Producer is who produced one deliverable version: an agent (Owner,
// Provider, Core), or a person (Person) for a manual publish.
type Producer struct {
	Owner    string
	Provider string
	Core     string
	Person   string
	// JobID is the source job when its run record could not be read.
	JobID string
}

// IsAgent reports an agent producer.
func (p Producer) IsAgent() bool { return p.Provider != "" }

// Same reports whether p is the agent self (an empty core matches any core:
// a job may name none and run on the default core).
func (p Producer) Same(self Producer) bool {
	return p.IsAgent() && p.Owner == self.Owner && p.Provider == self.Provider &&
		(p.Core == "" || self.Core == "" || p.Core == self.Core)
}

// String renders the producer for an agent: "maya/opencode on core acme", a
// person "maya (a person)".
func (p Producer) String() string {
	switch {
	case p.IsAgent():
		s := AgentSpeaker(p.Owner, p.Provider)
		if p.Core != "" {
			s += " on core " + p.Core
		}
		return s
	case p.Person != "":
		return p.Person + " (a person)"
	case p.JobID != "":
		return "an agent (job " + p.JobID + ")"
	}
	return "unknown"
}

// VersionProducer resolves who produced v, a version of a deliverable of
// room, reading its source job's run record through the gateway
// (ProducerOf).
func VersionProducer(ctx context.Context, fetch Fetch, room string, v wire.ArtifactVersion) (Producer, error) {
	return ProducerOf(ctx, v, func(ctx context.Context, job string) (wire.AgentRunAgent, error) {
		raw, err := fetch(ctx, "/v1/rooms/"+url.PathEscape(room)+"/jobs/"+url.PathEscape(job))
		if err != nil {
			return wire.AgentRunAgent{}, err
		}
		var run wire.AgentRun
		if err := json.Unmarshal(raw, &run); err != nil {
			return wire.AgentRunAgent{}, err
		}
		return run.Agent, nil
	})
}

// ProducerOf is the one producer rule: v's source job's agent, as agentOf
// reads that job's run record, else v's author (a person's publish). A job
// whose agent cannot be read leaves the job id. VersionProducer reads the
// run through the gateway; the room service (a member's apps) reads its
// store.
func ProducerOf(ctx context.Context, v wire.ArtifactVersion, agentOf func(ctx context.Context, jobID string) (wire.AgentRunAgent, error)) (Producer, error) {
	job := strings.TrimSpace(v.SourceJobID)
	if job == "" {
		return Producer{Person: v.AuthorUserID}, nil
	}
	agent, err := agentOf(ctx, job)
	if err != nil {
		return Producer{JobID: job}, err
	}
	if agent.Provider == "" {
		return Producer{JobID: job}, fmt.Errorf("run %s names no agent", job)
	}
	return Producer{Owner: agent.Owner, Provider: agent.Provider, Core: agent.Core}, nil
}

// ProducerCache remembers resolved producers per room, artifact and version
// (a published version never changes), so a turn's .room/artifacts.md costs
// one listing once every listed version was resolved. Bounded: it starts
// over past maxCachedProducers. A nil cache caches nothing.
type ProducerCache struct {
	mu sync.Mutex
	m  map[string]Producer
}

const maxCachedProducers = 4096

func (c *ProducerCache) get(key string) (Producer, bool) {
	if c == nil {
		return Producer{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[key]
	return p, ok
}

func (c *ProducerCache) put(key string, p Producer) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= maxCachedProducers {
		c.m = map[string]Producer{}
	}
	c.m[key] = p
}

// Deliverable is one room deliverable at its latest version.
type Deliverable struct {
	ArtifactID  string
	Title       string
	Kind        string
	FileName    string
	Version     int
	UpdatedAtMs int64
	Producer    Producer
}

// ReadDeliverables lists the room's deliverables, newest-updated first (at
// most MaxIndexedDeliverables); the newest MaxListedDeliverables carry the
// producer of their latest version (an older one is listed by id only). A
// version whose producer cannot be read is listed with what is known (the
// job id) and resolved again next time.
func ReadDeliverables(ctx context.Context, fetch Fetch, room string, cache *ProducerCache) ([]Deliverable, error) {
	list, err := listAllDeliverables(ctx, fetch, room)
	if err != nil {
		return nil, err
	}
	out := make([]Deliverable, 0, len(list))
	for i, a := range list {
		if i < MaxListedDeliverables {
			out = append(out, deliverableOf(ctx, fetch, room, a, cache))
			continue
		}
		out = append(out, Deliverable{ArtifactID: a.ArtifactID, Title: a.Title, Kind: a.Kind, FileName: a.FileName, Version: a.CurrentVersion, UpdatedAtMs: a.UpdatedAtMs})
	}
	return out, nil
}

// listAllDeliverables pages through the room's deliverables that have a
// title and a version, newest-updated first, up to MaxIndexedDeliverables.
func listAllDeliverables(ctx context.Context, fetch Fetch, room string) ([]wire.Artifact, error) {
	var out []wire.Artifact
	cursor := ""
	for len(out) < MaxIndexedDeliverables {
		q := url.Values{"limit": {strconv.Itoa(deliverablesPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		raw, err := fetch(ctx, deliverablesPath(room)+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var list wire.ArtifactList
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("unreadable deliverables listing: %w", err)
		}
		for _, a := range list.Artifacts {
			if a.Title == "" || a.CurrentVersion <= 0 {
				continue
			}
			out = append(out, a)
			if len(out) == MaxIndexedDeliverables {
				break
			}
		}
		if list.NextCursor == "" || list.NextCursor == cursor {
			break
		}
		cursor = list.NextCursor
	}
	return out, nil
}

// listDeliverables is the room's newest-updated deliverables (at most
// MaxListedDeliverables) that have a title and a version: the one listing
// .room/artifacts.md and .room/apps.md read.
func listDeliverables(ctx context.Context, fetch Fetch, room string) ([]wire.Artifact, error) {
	raw, err := fetch(ctx, deliverablesPath(room)+"?limit="+strconv.Itoa(MaxListedDeliverables))
	if err != nil {
		return nil, err
	}
	var list wire.ArtifactList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("unreadable deliverables listing: %w", err)
	}
	out := make([]wire.Artifact, 0, len(list.Artifacts))
	for _, a := range list.Artifacts {
		if a.Title == "" || a.CurrentVersion <= 0 {
			continue
		}
		out = append(out, a)
		if len(out) == MaxListedDeliverables {
			break
		}
	}
	return out, nil
}

func deliverablesPath(room string) string { return "/v1/rooms/" + url.PathEscape(room) + "/artifacts" }

// deliverableOf is a listed artifact with the producer of its latest
// version, from the cache when it was resolved before.
func deliverableOf(ctx context.Context, fetch Fetch, room string, a wire.Artifact, cache *ProducerCache) Deliverable {
	d := Deliverable{ArtifactID: a.ArtifactID, Title: a.Title, Kind: a.Kind, FileName: a.FileName, Version: a.CurrentVersion, UpdatedAtMs: a.UpdatedAtMs}
	key := room + "\x00" + a.ArtifactID + "\x00" + strconv.Itoa(a.CurrentVersion)
	if p, ok := cache.get(key); ok {
		d.Producer = p
	} else if p, err := deliverableProducer(ctx, fetch, room, a); err == nil {
		cache.put(key, p)
		d.Producer = p
	} else {
		d.Producer = p
	}
	return d
}

// deliverableProducer reads a's latest version and resolves its producer.
func deliverableProducer(ctx context.Context, fetch Fetch, room string, a wire.Artifact) (Producer, error) {
	raw, err := fetch(ctx, deliverablesPath(room)+"/"+url.PathEscape(a.ArtifactID)+"/versions/"+strconv.Itoa(a.CurrentVersion))
	if err != nil {
		return Producer{}, err
	}
	var reply wire.ArtifactVersionReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Producer{}, err
	}
	return VersionProducer(ctx, fetch, room, reply.Version)
}

// DeliverablesFile renders .room/artifacts.md: each deliverable with its
// kind, latest version, id and producer, and where its copy is in this
// workspace (copies, CopyDeliverables) or why it has none. self is the
// reading agent (its own deliverables say so). A failed listing (err) says
// so instead of claiming the room has none.
func DeliverablesFile(list []Deliverable, err error, self Producer, copies map[string]Copy) NamedFile {
	var b strings.Builder
	b.WriteString(RecordsHeader("Room deliverables"))
	b.WriteString("\n")
	b.WriteString("Files the room's agents published from their own workspaces, or people uploaded. bnbloc stores them\n")
	b.WriteString("and keeps a read-only copy of each below in " + DeliverablesDir + "<id>/, refreshed every turn: to change\n")
	b.WriteString("one, copy it into your own working tree first. A version without a copy here: the read_artifact room\n")
	b.WriteString("tool (by id) when you have it, else the person opens it in the room's Docs panel.\n\n")
	switch {
	case err != nil:
		b.WriteString("(the deliverables could not be read this turn; this is not a sign that there are none)\n")
	case len(list) == 0:
		b.WriteString("(none yet)\n")
	}
	for i, d := range list {
		if i == MaxListedDeliverables {
			b.WriteString("\nOlder:\n")
		}
		if i >= MaxListedDeliverables {
			fmt.Fprintf(&b, "- %s (%s) v%d, id %s\n", d.Title, d.Kind, d.Version, d.ArtifactID)
			continue
		}
		fmt.Fprintf(&b, "- %s (%s) v%d, id %s: ", d.Title, d.Kind, d.Version, d.ArtifactID)
		switch {
		case d.Producer.Same(self):
			fmt.Fprintf(&b, "from you (%s)", d.Producer)
		case d.Producer.IsAgent():
			fmt.Fprintf(&b, "from %s, whose source is in that agent's workspace", d.Producer)
		case d.Producer.Person != "":
			fmt.Fprintf(&b, "uploaded by %s", d.Producer)
		default:
			fmt.Fprintf(&b, "from %s", d.Producer)
		}
		b.WriteString("; " + copyNote(copies[d.ArtifactID], d.Version) + "\n")
	}
	if len(list) == MaxIndexedDeliverables {
		fmt.Fprintf(&b, "\n(the %d most recently updated; older deliverables, if any, are not listed)\n", MaxIndexedDeliverables)
	}
	return NamedFile{Name: ".room/artifacts.md", Content: b.String()}
}

// copyNote says where a deliverable's copy is, or why there is none.
func copyNote(c Copy, latest int) string {
	switch {
	case c.TooLarge > 0:
		return fmt.Sprintf("no copy: %d bytes, over the %d-byte copy bound", c.TooLarge, MaxDeliverableCopyBytes)
	case c.Dir != "" && c.Version == latest:
		return "copy: " + c.Dir
	case c.Dir != "":
		return fmt.Sprintf("copy of v%d (not yet the latest): %s", c.Version, c.Dir)
	}
	return "no copy in this workspace this turn"
}
