package extreg

import (
	"context"
	"encoding/json"
)

const (
	KindAuthProvider Kind = "auth-provider"
	KindNotifier     Kind = "notifier"
	KindWebhook      Kind = "webhook"
	// KindSandbox is a Sandbox Protocol v1 runtime (local microVM sidecar or
	// remote endpoint).
	KindSandbox Kind = "sandbox"
	// KindPreviewer is a static web bundle the web client runs in a
	// sandboxed frame to render artifact files (no process, no endpoint).
	KindPreviewer Kind = "previewer"
	// KindMCP is an MCP tool component: a stdio server the member's desktop
	// materializes and the agent spawns per session, or an http endpoint.
	// Content components require it by name.
	KindMCP Kind = "mcp"
)

type HookEnvelopeContract string

const HookEnvelopeContractV1 HookEnvelopeContract = "bnbloc.hook-envelope/v1"

type KindContract string

const (
	KindContractAuthProviderV1 KindContract = "bnbloc.auth-provider.contract/v1"
	KindContractNotifierV1     KindContract = "bnbloc.notifier.contract/v1"
	KindContractWebhookV1      KindContract = "bnbloc.webhook.contract/v1"
	KindContractSandboxV1      KindContract = "bnbloc.sandbox.contract/v1"
	KindContractPreviewerV1    KindContract = "bnbloc.previewer.contract/v1"
	KindContractMCPV1          KindContract = "bnbloc.mcp.contract/v1"
)

type HookEnvelope struct {
	contract HookEnvelopeContract
	event    string
	version  string
	tenantID string
	roomID   string
	payload  json.RawMessage
}

type HookEnvelopeData struct {
	Contract HookEnvelopeContract
	Event    string
	Version  string
	TenantID string
	RoomID   string
	Payload  json.RawMessage
}

func NewHookEnvelope(data HookEnvelopeData) HookEnvelope {
	return HookEnvelope{contract: data.Contract, event: data.Event, version: data.Version, tenantID: data.TenantID, roomID: data.RoomID, payload: append(json.RawMessage(nil), data.Payload...)}
}

func (e HookEnvelope) Contract() HookEnvelopeContract { return e.contract }

func (e HookEnvelope) Event() string { return e.event }

func (e HookEnvelope) Version() string { return e.version }

func (e HookEnvelope) TenantID() string { return e.tenantID }

func (e HookEnvelope) RoomID() string { return e.roomID }

func (e HookEnvelope) Payload() json.RawMessage { return append(json.RawMessage(nil), e.payload...) }

func (e HookEnvelope) MarshalJSON() ([]byte, error) {
	type wireHookEnvelope struct {
		Contract string          `json:"contract"`
		Event    string          `json:"event"`
		Version  string          `json:"version"`
		TenantID string          `json:"tenantId"`
		RoomID   string          `json:"roomId"`
		Payload  json.RawMessage `json:"payload"`
	}
	return json.Marshal(wireHookEnvelope{
		Contract: string(e.contract),
		Event:    e.event,
		Version:  e.version,
		TenantID: e.tenantID,
		RoomID:   e.roomID,
		Payload:  e.Payload(),
	})
}

type AuthProviderHost interface {
	AuthProviderName() string
	AuthProviderContract() string
}

type WebhookHost interface {
	WebhookName() string
	WebhookContract() string
	DeliverHook(ctx context.Context, envelope HookEnvelope) error
}

func init() {
	RegisterKind(KindDescriptor{Kind: KindAuthProvider, Contract: KindContractAuthProviderV1, Hosts: []Service{ServiceGateway}})
	// Notifiers are driven by realtime's notification outbox worker.
	RegisterKind(KindDescriptor{Kind: KindNotifier, Contract: KindContractNotifierV1, Hosts: []Service{ServiceRealtime}})
	// Sandboxes: the gateway owns the install record and the member catalog;
	// the desktop supervises local sidecars from that catalog, and the bridge
	// will drive the Sandbox Protocol in the next phase. Nothing runs in the
	// gateway itself.
	RegisterKind(KindDescriptor{Kind: KindSandbox, Contract: KindContractSandboxV1, Hosts: []Service{ServiceGateway}})
	// Previewers: the gateway owns the install record, the member catalog
	// and (browser) the bundle serving; the desktop serves bundles from its
	// own scheme. Nothing executes server-side.
	RegisterKind(KindDescriptor{Kind: KindPreviewer, Contract: KindContractPreviewerV1, Hosts: []Service{ServiceGateway}})
	// MCP tools: the gateway owns the install record, the member catalog,
	// org connectors and the per-job resolution; the desktop materializes
	// stdio binaries and the agent runs them. Nothing runs server-side.
	RegisterKind(KindDescriptor{Kind: KindMCP, Contract: KindContractMCPV1, Hosts: []Service{ServiceGateway}})
	RegisterKind(KindDescriptor{Kind: KindWebhook, Contract: KindContractWebhookV1, Hosts: []Service{ServiceRealtime}, HookEnvelopes: true})
}
