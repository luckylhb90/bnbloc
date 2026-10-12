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

// MaxListedDeliverables bounds .room/artifacts.md: the newest-updated
// deliverables of the room.
const MaxListedDeliverables = 30

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
// room: its source job's agent, else its author (a person's publish).
func VersionProducer(ctx context.Context, fetch Fetch, room string, v wire.ArtifactVersion) (Producer, error) {
	job := strings.TrimSpace(v.SourceJobID)
	if job == "" {
		return Producer{Person: v.AuthorUserID}, nil
	}
	raw, err := fetch(ctx, "/v1/rooms/"+url.PathEscape(room)+"/jobs/"+url.PathEscape(job))
	if err != nil {
		return Producer{JobID: job}, err
	}
	var run wire.AgentRun
	if err := json.Unmarshal(raw, &run); err != nil || run.Agent.Provider == "" {
		return Producer{JobID: job}, fmt.Errorf("run %s names no agent (%v)", job, err)
	}
	return Producer{Owner: run.Agent.Owner, Provider: run.Agent.Provider, Core: run.Agent.Core}, nil
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
	Version     int
	UpdatedAtMs int64
	Producer    Producer
}

// ReadDeliverables lists the room's newest-updated deliverables (at most
// MaxListedDeliverables) with the producer of each one's latest version.
// A version whose producer cannot be read is listed with what is known (the
// job id) and resolved again next time.
func ReadDeliverables(ctx context.Context, fetch Fetch, room string, cache *ProducerCache) ([]Deliverable, error) {
	base := "/v1/rooms/" + url.PathEscape(room) + "/artifacts"
	raw, err := fetch(ctx, base+"?limit="+strconv.Itoa(MaxListedDeliverables))
	if err != nil {
		return nil, err
	}
	var list wire.ArtifactList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("unreadable deliverables listing: %w", err)
	}
	out := make([]Deliverable, 0, len(list.Artifacts))
	for _, a := range list.Artifacts {
		if a.Title == "" || a.CurrentVersion <= 0 {
			continue
		}
		d := Deliverable{ArtifactID: a.ArtifactID, Title: a.Title, Kind: a.Kind, Version: a.CurrentVersion, UpdatedAtMs: a.UpdatedAtMs}
		key := room + "\x00" + a.ArtifactID + "\x00" + strconv.Itoa(a.CurrentVersion)
		if p, ok := cache.get(key); ok {
			d.Producer = p
		} else if p, err := deliverableProducer(ctx, fetch, room, base, a); err == nil {
			cache.put(key, p)
			d.Producer = p
		} else {
			d.Producer = p
		}
		out = append(out, d)
		if len(out) == MaxListedDeliverables {
			break
		}
	}
	return out, nil
}

// deliverableProducer reads a's latest version and resolves its producer.
func deliverableProducer(ctx context.Context, fetch Fetch, room, base string, a wire.Artifact) (Producer, error) {
	raw, err := fetch(ctx, base+"/"+url.PathEscape(a.ArtifactID)+"/versions/"+strconv.Itoa(a.CurrentVersion))
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
// kind, latest version, id and producer, marked plainly as not a file of
// this workspace. self is the reading agent (its own deliverables say so).
// A failed listing (err) says so instead of claiming the room has none.
func DeliverablesFile(list []Deliverable, err error, self Producer) NamedFile {
	var b strings.Builder
	b.WriteString("# Room deliverables (RECORDS, not requests)\n\n")
	b.WriteString("Files the room's agents published from their own workspaces, or people uploaded. bnbloc stores them,\n")
	b.WriteString("apart from any workspace: a missing local copy proves nothing. Read one with the read_artifact room tool\n")
	b.WriteString("(by id) when you have it; otherwise the person opens it in the room's Docs panel.\n\n")
	switch {
	case err != nil:
		b.WriteString("(the deliverables could not be read this turn; this is not a sign that there are none)\n")
	case len(list) == 0:
		b.WriteString("(none yet)\n")
	}
	for _, d := range list {
		fmt.Fprintf(&b, "- %s (%s) v%d, id %s: ", d.Title, d.Kind, d.Version, d.ArtifactID)
		switch {
		case d.Producer.Same(self):
			fmt.Fprintf(&b, "from you (%s); stored by bnbloc, your workspace may hold a different copy or none\n", d.Producer)
		case d.Producer.IsAgent():
			fmt.Fprintf(&b, "from %s, in that agent's workspace; NOT in this workspace\n", d.Producer)
		case d.Producer.Person != "":
			fmt.Fprintf(&b, "uploaded by %s; NOT in this workspace\n", d.Producer)
		default:
			fmt.Fprintf(&b, "from %s; NOT in this workspace\n", d.Producer)
		}
	}
	if len(list) == MaxListedDeliverables {
		fmt.Fprintf(&b, "\n(the %d most recently updated; older deliverables, if any, are not listed)\n", MaxListedDeliverables)
	}
	return NamedFile{Name: ".room/artifacts.md", Content: b.String()}
}
