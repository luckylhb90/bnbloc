package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Events a room subscribes to (docs/prd/2026-10-21-room-events.md). Anything
// can tell a room that something happened (the platform, the room itself,
// an outside system through a task's webhook) and a room's task runs
// because of it: a task's event trigger (ScheduleRecurrence with
// RecurEvent) is the subscription. Every event has one shape, the
// CloudEvents 1.0 context attributes (CloudEvent), checked by one
// validator (ValidEvent); its data is always untrusted and a run reads it
// fenced as data.

// CloudEventsVersion is the CloudEvents spec version every event carries.
const CloudEventsVersion = "1.0"

// MaxEventDataBytes bounds an event's data (its compact JSON).
const MaxEventDataBytes = 16 * 1024

// Bounds of an event's context attributes.
const (
	maxEventIDLen      = 256
	maxEventSourceLen  = 256
	maxEventTypeLen    = 128
	maxEventSubjectLen = 256
)

// Event sources: who emitted an event (CloudEvent.Source).
const (
	// SourcePlatform: the platform itself (a deploy, a publish); the
	// gateway records these. A trigger subscribes to it by the same name.
	SourcePlatform = SubscribePlatform
	// SourceRoomPrefix + a room id: what happened in that room (a
	// file, a member, an app's data); realtime emits these.
	SourceRoomPrefix = "bnbloc/room/"
	// SourceWebhookPrefix + a hook id: what an outside system posted
	// to a task's webhook URL (ScheduleHook).
	SourceWebhookPrefix = "webhook/"
	// SourceMemberPrefix + a subject: an event a room's member sent
	// to the room (POST /v1/rooms/{roomId}/events: a script, a CI job with
	// the member's API token, the CLI's bnbloc events send).
	SourceMemberPrefix = "bnbloc/member/"
)

// ReservedEventTypePrefixes are the type prefixes only the platform emits:
// a member's own event (MemberEvent) may not use them, so a task taking
// the platform's or the room's events never runs on a member's imitation.
var ReservedEventTypePrefixes = []string{"platform.", "room.", "component.", "desktop."}

// MemberEvent is the body of POST /v1/rooms/{roomId}/events: what a room's
// member tells the room happened. The server sets the rest of the event
// (source bnbloc/member/<subject>, id, time); the room's tasks whose
// trigger subscribes to SubscribeMember take it.
type MemberEvent struct {
	Type    string          `json:"type"`
	Subject string          `json:"subject,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// MemberEventReply answers a member's event: the event as sent (its id
// and time) and how many of the room's tasks it set to run.
type MemberEventReply struct {
	Event     CloudEvent `json:"event"`
	Triggered int        `json:"triggered"`
	Dropped   int        `json:"dropped,omitempty"`
}

// ReservedEventType reports a type a member may not send.
func ReservedEventType(typ string) bool {
	for _, p := range ReservedEventTypePrefixes {
		if strings.HasPrefix(typ, p) {
			return true
		}
	}
	return false
}

// The sources a task's trigger may subscribe to (ScheduleRecurrence.Source):
// the platform's events, its own room's, what the room's members send it
// (any member: the filter narrows by type and subject), or what is posted
// to its own webhook URL. A subscription never names another room or
// another task's URL: delivery reaches only the tenants and rooms an event
// may reach.
//
//wire:union EventSubscriptionSource
const (
	SubscribePlatform = "bnbloc/platform"
	SubscribeRoom     = "bnbloc/room"
	SubscribeMember   = "bnbloc/member"
	SubscribeWebhook  = "webhook"
)

// EventSubscriptionSources is every source a trigger may name, in the order
// an editor shows them.
var EventSubscriptionSources = []string{SubscribePlatform, SubscribeRoom, SubscribeMember, SubscribeWebhook}

// Platform event types (source SourcePlatform). The gateway records
// each once and hands it to realtime (POST /internal/v1/events).
//
//wire:union PlatformEventType
const (
	// PlatformDeployed: the gateway started on a build whose version
	// differs from the last one recorded (data: version, previous).
	PlatformDeployed = "platform.deployed"
	// ComponentPublished: a component (any kind) or platform content got a
	// new version (subject: its name; data: kind, name, version, rollout).
	ComponentPublished = "component.published"
	// DesktopPublished: a desktop version was activated for the updater
	// (data: version, targets).
	DesktopPublished = "desktop.published"
)

// PlatformEventTypes is the closed list of platform event types, in the
// order an editor shows them.
var PlatformEventTypes = []string{PlatformDeployed, ComponentPublished, DesktopPublished}

// PlatformDeployedData is the data of PlatformDeployed: the build (its
// commit) and the one recorded before it ("" for the first).
type PlatformDeployedData struct {
	Version  string `json:"version"`
	Previous string `json:"previous,omitempty"`
}

// ComponentPublishedData is the data of ComponentPublished (the event's
// subject is the name).
type ComponentPublishedData struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Rollout string `json:"rollout,omitempty"`
}

// DesktopPublishedData is the data of DesktopPublished: the version and
// the targets the updater has a file for.
type DesktopPublishedData struct {
	Version string   `json:"version"`
	Targets []string `json:"targets"`
}

// Room event types (source SourceRoomPrefix + the room). realtime
// emits them from the room's own feed and delivers them only to that
// room's tasks.
//
//wire:union RoomEventType
const (
	// RoomFileAdded: a file (artifact) of the room got a version, its first
	// or a new one (subject: its file name or title; data: artifactId,
	// kind, title, fileName, mimeType, version, by).
	RoomFileAdded = "room.file.added"
	// RoomMemberJoined: a person became a member (subject: their id; data:
	// userId).
	RoomMemberJoined = "room.member.joined"
	// RoomAppDataChanged: an interactive app's data changed, coalesced per
	// app (subject: the app id; data: appId, collection, changes).
	RoomAppDataChanged = "room.app.data.changed"
)

// RoomEventTypes is the closed list of room event types, in the order an
// editor shows them.
var RoomEventTypes = []string{RoomFileAdded, RoomMemberJoined, RoomAppDataChanged}

// RoomFileAddedData is the data of RoomFileAdded: the file, the version it
// got and who published it.
type RoomFileAddedData struct {
	ArtifactID string `json:"artifactId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	FileName   string `json:"fileName,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Version    int    `json:"version"`
	By         string `json:"by"`
}

// RoomMemberJoinedData is the data of RoomMemberJoined.
type RoomMemberJoinedData struct {
	UserID string `json:"userId"`
}

// RoomAppDataChangedData is the data of RoomAppDataChanged: the app, the
// collection of the newest change ("" for a wipe) and how many changes the
// event stands for (writes within one coalescing window are one event).
type RoomAppDataChangedData struct {
	AppID      string `json:"appId"`
	Collection string `json:"collection,omitempty"`
	Changes    int    `json:"changes"`
}

// CloudEvent is one event: the CloudEvents 1.0 context attributes and its
// data (JSON, at most MaxEventDataBytes). Data is the emitter's, untrusted:
// a run reads it fenced as data, never as instructions.
type CloudEvent struct {
	SpecVersion string          `json:"specversion"`
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Time        string          `json:"time"`
	Subject     string          `json:"subject,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// ErrEventDataTooLarge reports data over MaxEventDataBytes.
var ErrEventDataTooLarge = fmt.Errorf("event: data is over %d bytes", MaxEventDataBytes)

// ValidEvent is the one validator of an event: the spec version (1.0,
// filled in when empty), an id, a source and a type (bounded, printable,
// no spaces), a time (RFC 3339, normalized to UTC), an optional subject
// (bounded, printable) and optional data (one JSON value, compacted, at
// most MaxEventDataBytes: ErrEventDataTooLarge). It returns the event
// normalized.
func ValidEvent(e CloudEvent) (CloudEvent, error) {
	switch strings.TrimSpace(e.SpecVersion) {
	case "", CloudEventsVersion:
		e.SpecVersion = CloudEventsVersion
	default:
		return e, fmt.Errorf("event: specversion %q (want %s)", e.SpecVersion, CloudEventsVersion)
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{{"id", e.ID, maxEventIDLen}, {"source", e.Source, maxEventSourceLen}, {"type", e.Type, maxEventTypeLen}} {
		if f.v == "" || len(f.v) > f.max || strings.IndexFunc(f.v, notEventToken) >= 0 {
			return e, fmt.Errorf("event: %s is required (at most %d printable characters, no spaces)", f.name, f.max)
		}
	}
	if len(e.Subject) > maxEventSubjectLen || strings.IndexFunc(e.Subject, unicode.IsControl) >= 0 {
		return e, fmt.Errorf("event: subject is at most %d printable characters", maxEventSubjectLen)
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(e.Time))
	if err != nil {
		return e, errors.New("event: time is required (RFC 3339)")
	}
	e.Time = at.UTC().Format(time.RFC3339Nano)
	if len(bytes.TrimSpace(e.Data)) == 0 {
		e.Data = nil
		return e, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, e.Data); err != nil {
		return e, errors.New("event: data is not JSON")
	}
	if buf.Len() > MaxEventDataBytes {
		return e, ErrEventDataTooLarge
	}
	e.Data = buf.Bytes()
	return e, nil
}

// notEventToken reports a character an id, source or type may not hold.
func notEventToken(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }

// EventTime is an instant as an event's time attribute.
func EventTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// EventDispatch is the gateway's hand-off of a platform event to realtime
// (POST /internal/v1/events): the event and the organizations whose tasks
// it reaches (none: every organization; an organization's own component
// publish names only it).
type EventDispatch struct {
	Event   CloudEvent `json:"event"`
	Tenants []string   `json:"tenants,omitempty"`
	// CauseJob / CauseTenant name the agent job whose grant caused the
	// event (a publish an agent made for its requester), so a scheduled
	// run's own action never triggers its room's tasks; empty for a
	// person's or the platform's own.
	CauseJob    string `json:"causeJob,omitempty"`
	CauseTenant string `json:"causeTenant,omitempty"`
}

// EventDispatchReply is realtime's answer to a dispatched event: how many
// tasks it set to run, and how many it dropped over a rate limit.
type EventDispatchReply struct {
	Triggered int `json:"triggered"`
	Dropped   int `json:"dropped,omitempty"`
}

// Webhook subscriptions: a task whose trigger subscribes to SubscribeWebhook
// has its own URL, HookPathPrefix + its hook id, and a secret shown once
// (ScheduleHookSecret). An outside system signs each request with it.
const (
	// HookPathPrefix opens a task's webhook URL on the gateway (an open
	// route: the signature is the credential).
	HookPathPrefix = "/v1/hooks/"
	// HookSignatureHeader carries "sha256=<hex HMAC-SHA256 of the raw
	// body under the hook's secret>". Same header name as a component's
	// signed call (NotificationSignatureHeader), a different scheme: an
	// outside system signs the body alone, the scheme GitHub uses, so CI
	// and repository hosts can call it as they are.
	HookSignatureHeader = "X-Bnbloc-Signature"
	// GitHubSignatureHeader is GitHub's header for the same scheme.
	GitHubSignatureHeader = "X-Hub-Signature-256"
	// HookEventHeader names the event's type; without it (or
	// GitHubEventHeader) the JSON body's top-level "type" does.
	HookEventHeader = "X-Bnbloc-Event"
	// GitHubEventHeader is GitHub's event name ("push"); the type becomes
	// "com.github.<name>".
	GitHubEventHeader = "X-GitHub-Event"
	// HookDeliveryHeader / GitHubDeliveryHeader name a delivery (the
	// event's id); without one the body's "id", else a fresh one.
	HookDeliveryHeader   = "X-Bnbloc-Delivery"
	GitHubDeliveryHeader = "X-GitHub-Delivery"
)

// ScheduleHook is a task's webhook URL as the room's members see it: its id
// (the URL is HookPathPrefix + HookID on this deployment) and the refused
// requests; never its secret.
type ScheduleHook struct {
	HookID      string `json:"hookId"`
	CreatedAtMs int64  `json:"createdAtMs"`
	// Refused counts requests refused at it (a bad or missing signature,
	// too large, not JSON, no type), LastRefusedAtMs the newest.
	Refused         int64 `json:"refused,omitempty"`
	LastRefusedAtMs int64 `json:"lastRefusedAtMs,omitempty"`
}

// ScheduleHookSecret answers creating or rotating a task's webhook
// (POST /v1/rooms/{roomId}/schedules/{scheduleId}/hook): the hook and its
// secret, shown this once.
type ScheduleHookSecret struct {
	Hook   ScheduleHook `json:"hook"`
	Secret string       `json:"secret"`
}

// HookReply answers a webhook request: how many tasks it set to run (0 or
// 1: the hook's own task, when its trigger takes the event).
type HookReply struct {
	Triggered int `json:"triggered"`
}
