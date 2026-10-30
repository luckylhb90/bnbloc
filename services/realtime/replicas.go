package realtimehttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Replicas without owners (docs/prd/2026-10-30-replicas-without-owners.md):
// any replica serves any room and any member. A room's events reach the
// replicas streaming it over the hub's bus; what else a replica cannot do
// alone crosses the same transport (realtime.Topics):
//
//   - a room event that bears a plugin hook goes to every replica, each
//     delivering it to the plugin streams it holds (topicPlugins);
//   - a catalog hint goes to every replica (topicCatalog);
//   - a member's notification goes to the replicas holding the member's
//     desktop plugin stream (memberTopic), the outcome back to the asker
//     (replyTopic);
//   - a plugin delivery's ack that reached another replica goes to the
//     replica that sent the delivery (replicaTopic), named by the delivery
//     id's prefix;
//
// A budget shared out per replica divides by the live count (count, the
// Redis census).
//
// A member's desktop plugin stream is marked (realtime.Marks) while a
// replica holds it, so any replica can tell whether the member has one.
//
// A nil *Replicas is a replica that is alone.
type Replicas struct {
	topics realtime.Topics
	marks  realtime.Marks
	census func() int
	origin string
	svc    *Service

	mu    sync.Mutex
	calls map[string]chan json.RawMessage
	// held are the members whose desktop streams this replica holds:
	// memberTopic -> the topic's cancel.
	held   map[string]func()
	nextID atomic.Uint64
}

const (
	topicPlugins = "all:plugin-events"
	topicCatalog = "all:catalog"

	// desktopMarkTTL bounds a desktop mark its replica stopped renewing (it
	// died); it is renewed every markRenew.
	markRenew      = 10 * time.Second
	desktopMarkTTL = 3 * markRenew
	// replicaCallTimeout bounds a short call to another replica (an ack).
	replicaCallTimeout = 5 * time.Second
	// notificationCallTimeout outlasts the holder's own outcome wait: the
	// dispatcher reports within the ack timeout plus one expiry sweep.
	notificationCallTimeout = pendingAckTimeout + pendingSweepInterval + 15*time.Second
)

func memberTopic(tenantID, member string) string { return "member:" + tenantID + "/" + member }
func replicaTopic(origin string) string          { return "replica:" + origin }
func replyTopic(origin string) string            { return "reply:" + origin }
func desktopMark(tenantID, member string) string { return "desktop:" + tenantID + "/" + member }

// replicaMsg is every message between replicas; a call carries ID and From
// (its reply goes to From's reply topic with the same ID).
type replicaMsg struct {
	ID           string                   `json:"id,omitempty"`
	From         string                   `json:"from,omitempty"`
	Tenant       string                   `json:"tenant,omitempty"`
	Event        *wire.Event              `json:"event,omitempty"`
	Catalog      *wire.ComponentsChanged  `json:"catalog,omitempty"`
	Notification *wire.PluginNotification `json:"notification,omitempty"`
	Ack          *replicaAck              `json:"ack,omitempty"`
	Reply        json.RawMessage          `json:"reply,omitempty"`
}

type replicaAck struct {
	Subject string                 `json:"subject"`
	ID      string                 `json:"id"`
	Ack     wire.PluginDeliveryAck `json:"ack"`
}

// NewReplicas returns the replicas' shared side over topics and marks, with
// census counting the live replicas (nil topics: a replica alone, nil).
func NewReplicas(topics realtime.Topics, marks realtime.Marks, census func() int) *Replicas {
	if topics == nil {
		return nil
	}
	return &Replicas{topics: topics, marks: marks, census: census, origin: randomOrigin(),
		calls: map[string]chan json.RawMessage{}, held: map[string]func(){}}
}

// start subscribes the replica's topics and begins saying it is alive.
func (p *Replicas) start(svc *Service) error {
	if p == nil {
		return nil
	}
	p.svc = svc
	svc.PluginHooks.idPrefix = p.origin + "."
	svc.PluginHooks.streamsChanged = p.syncMember
	for topic, fn := range map[string]func(replicaMsg){
		topicPlugins:           p.onPluginEvent,
		topicCatalog:           p.onCatalog,
		replicaTopic(p.origin): p.onAck,
		replyTopic(p.origin):   p.onReply,
	} {
		if _, err := p.topics.Subscribe(topic, decodeReplicaMsg(topic, fn), func() {}); err != nil {
			return err
		}
	}
	go p.renew()
	return nil
}

func decodeReplicaMsg(topic string, fn func(replicaMsg)) func([]byte) {
	return func(b []byte) {
		var m replicaMsg
		if err := json.Unmarshal(b, &m); err != nil {
			log.Printf("realtime: replicas: %s: undecodable message: %v", topic, err)
			return
		}
		fn(m)
	}
}

func (p *Replicas) publish(topic string, m replicaMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return p.topics.Publish(topic, b)
}

// ---------------------------------------------------------------- broadcasts

// broadcastPluginEvent hands a hook-bearing room event to every other
// replica for its plugin streams.
func (p *Replicas) broadcastPluginEvent(tenantID string, ev wire.Event) {
	if p == nil {
		return
	}
	ev.Encoded = nil
	if err := p.publish(topicPlugins, replicaMsg{Tenant: tenantID, Event: &ev}); err != nil {
		log.Printf("realtime: replicas: plugin event for room=%s not handed on: %v", ev.RoomID, err)
	}
}

func (p *Replicas) onPluginEvent(m replicaMsg) {
	if m.Event == nil || m.Event.RoomID == "" {
		return
	}
	p.svc.PluginHooks.Deliver(tenant.NewContext(context.Background(), m.Tenant), p.svc.Membership, *m.Event)
}

// broadcastCatalog hands a catalog hint to every other replica.
func (p *Replicas) broadcastCatalog(c wire.ComponentsChanged) {
	if p == nil {
		return
	}
	if err := p.publish(topicCatalog, replicaMsg{Catalog: &c}); err != nil {
		log.Printf("realtime: replicas: catalog hint not handed on: %v", err)
	}
}

func (p *Replicas) onCatalog(m replicaMsg) {
	if m.Catalog != nil {
		p.svc.PluginHooks.componentsChanged(*m.Catalog)
	}
}

// ---------------------------------------------------------------- liveness

// renew renews this replica's desktop marks every markRenew for the
// process's life.
func (p *Replicas) renew() {
	for range time.Tick(markRenew) {
		p.renewMarks()
	}
}

// count is how many replicas are alive, this one included.
func (p *Replicas) count() int {
	if p == nil || p.census == nil {
		return 1
	}
	return p.census()
}

// ---------------------------------------------------------------- calls

var errReplicaCallTimeout = errors.New("no replica answered")

// call publishes m on topic and waits up to timeout for the first reply.
func (p *Replicas) call(topic string, m replicaMsg, timeout time.Duration) (json.RawMessage, error) {
	m.ID, m.From = p.origin+"-"+strconv.FormatUint(p.nextID.Add(1), 36), p.origin
	ch := make(chan json.RawMessage, 1)
	p.mu.Lock()
	p.calls[m.ID] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.calls, m.ID)
		p.mu.Unlock()
	}()
	if err := p.publish(topic, m); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		return nil, errReplicaCallTimeout
	}
}

func (p *Replicas) onReply(m replicaMsg) {
	p.mu.Lock()
	ch := p.calls[m.ID]
	p.mu.Unlock()
	if ch != nil {
		select {
		case ch <- m.Reply:
		default: // answered already (another holder was first)
		}
	}
}

func (p *Replicas) reply(to replicaMsg, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := p.publish(replyTopic(to.From), replicaMsg{ID: to.ID, Reply: b}); err != nil {
		log.Printf("realtime: replicas: reply to %s not sent: %v", to.From, err)
	}
}

// ---------------------------------------------------------------- acks

// remoteAck resolves a delivery another replica sent (its id's prefix names
// the replica): found reports that it knew the delivery.
func (p *Replicas) remoteAck(subject, id string, ack wire.PluginDeliveryAck) bool {
	if p == nil {
		return false
	}
	origin, _, ok := strings.Cut(id, ".")
	if !ok || origin == p.origin {
		return false
	}
	r, err := p.call(replicaTopic(origin), replicaMsg{Ack: &replicaAck{Subject: subject, ID: id, Ack: ack}}, replicaCallTimeout)
	if err != nil {
		log.Printf("realtime: replicas: ack of delivery %s not resolved by its replica: %v", id, err)
		return false
	}
	var found bool
	_ = json.Unmarshal(r, &found)
	return found
}

func (p *Replicas) onAck(m replicaMsg) {
	if m.Ack == nil {
		return
	}
	p.reply(m, p.svc.PluginHooks.ack(context.Background(), m.Ack.Subject, m.Ack.ID, m.Ack.Ack))
}

// ---------------------------------------------------------------- members

// syncMember follows the dispatcher's streams of a member: while this
// replica holds a desktop plugin stream of the member it receives the
// member's notifications and marks the desktop.
func (p *Replicas) syncMember(tenantID, member string) {
	topic := memberTopic(tenantID, member)
	has := p.svc.PluginHooks.HasDesktop(tenantID, member)
	p.mu.Lock()
	cancel, held := p.held[topic]
	switch {
	case has && !held:
		p.held[topic] = func() {} // claimed; the subscription follows
		p.mu.Unlock()
		c, err := p.topics.Subscribe(topic, decodeReplicaMsg(topic, func(m replicaMsg) { p.onNotification(tenantID, m) }), func() {})
		if err != nil {
			log.Printf("realtime: replicas: member %s: %v (their notifications reach this desktop only from this replica)", member, err)
			c = func() {}
		}
		p.mu.Lock()
		p.held[topic] = c
		p.mu.Unlock()
		p.mark(tenantID, member)
	case !has && held:
		delete(p.held, topic)
		p.mu.Unlock()
		cancel()
		if p.marks != nil {
			_ = p.marks.Unmark(desktopMark(tenantID, member), p.origin)
		}
	default:
		p.mu.Unlock()
	}
}

func (p *Replicas) mark(tenantID, member string) {
	if p.marks == nil {
		return
	}
	if err := p.marks.Mark(desktopMark(tenantID, member), p.origin, desktopMarkTTL); err != nil {
		log.Printf("realtime: replicas: desktop mark of %s: %v", member, err)
	}
}

func (p *Replicas) renewMarks() {
	p.mu.Lock()
	var held []string
	for topic := range p.held {
		held = append(held, strings.TrimPrefix(topic, "member:"))
	}
	p.mu.Unlock()
	for _, tm := range held {
		if t, m, ok := strings.Cut(tm, "/"); ok {
			p.mark(t, m)
		}
	}
}

func (p *Replicas) onNotification(tenantID string, m replicaMsg) {
	if m.Notification == nil {
		return
	}
	p.svc.PluginHooks.DeliverNotification(tenantID, *m.Notification, func(delivered bool) { p.reply(m, delivered) })
}

// hasDesktop reports whether a replica holds a desktop stream of the member.
func (p *Replicas) hasDesktop(tenantID, member string) bool {
	if p.marks == nil {
		return false
	}
	ok, err := p.marks.Marked(desktopMark(tenantID, member))
	if err != nil {
		log.Printf("realtime: replicas: desktop mark of %s unreadable (server notifiers will deliver): %v", member, err)
	}
	return ok
}

// deliverNotification hands the notification to the replicas holding the
// member's desktop and reports the first outcome; none answering is false.
func (p *Replicas) deliverNotification(tenantID string, n wire.PluginNotification, done func(bool)) {
	go func() {
		r, err := p.call(memberTopic(tenantID, n.UserID), replicaMsg{Notification: &n}, notificationCallTimeout)
		var delivered bool
		if err == nil {
			_ = json.Unmarshal(r, &delivered)
		} else {
			log.Printf("realtime: replicas: notification for member=%s: %v", n.UserID, err)
		}
		done(delivered)
	}()
}

// randomOrigin names this replica on the bus.
func randomOrigin() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
