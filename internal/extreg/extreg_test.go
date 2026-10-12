package extreg

import (
	"encoding/json"
	"testing"
)

func TestReregisterInstalledThenUnregister(t *testing.T) {
	if err := Reregister(Extension{Kind: "testkind", Name: "c", Backend: BackendManifest}); err != nil {
		t.Fatalf("Reregister installed: %v", err)
	}
	if _, ok := Get("testkind", "c"); !ok {
		t.Fatal("installed extension not found")
	}
	if err := Unregister("testkind", "c"); err != nil {
		t.Fatalf("Unregister installed: %v", err)
	}
	if _, ok := Get("testkind", "c"); ok {
		t.Fatal("unregistered extension still present")
	}
}

func TestListFiltersByKind(t *testing.T) {
	_ = Reregister(Extension{Kind: "k1", Name: "x", Backend: BackendManifest})
	_ = Reregister(Extension{Kind: "k2", Name: "y", Backend: BackendManifest})
	t.Cleanup(func() { _ = Unregister("k1", "x"); _ = Unregister("k2", "y") })
	if got := List("k1"); len(got) != 1 || got[0].Name != "x" {
		t.Fatalf("List(k1) = %+v; want [x]", got)
	}
}

func TestSetInstance(t *testing.T) {
	t.Cleanup(func() { _ = Unregister("testkind", "d") })
	_ = Reregister(Extension{Kind: "testkind", Name: "d", Backend: BackendManifest})
	SetInstance("testkind", "d", "live")
	if e, _ := Get("testkind", "d"); e.Instance.(string) != "live" {
		t.Fatalf("Instance = %v; want live", e.Instance)
	}
}

func TestPrivateCapabilityKindDescriptors_areGatewayAndRealtimeHosted(t *testing.T) {
	// Given the private-capability host descriptors registered by extreg init.
	tests := []struct {
		name              string
		kind              Kind
		wantContract      KindContract
		wantHosts         []Service
		wantHookEnvelopes bool
	}{
		{
			name:         "auth provider",
			kind:         KindAuthProvider,
			wantContract: KindContractAuthProviderV1,
			wantHosts:    []Service{ServiceGateway},
		},
		{
			name:         "notifier",
			kind:         KindNotifier,
			wantContract: KindContractNotifierV1,
			wantHosts:    []Service{ServiceRealtime},
		},
		{
			name:              "webhook",
			kind:              KindWebhook,
			wantContract:      KindContractWebhookV1,
			wantHosts:         []Service{ServiceRealtime},
			wantHookEnvelopes: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			desc, ok := Descriptor(tt.kind)

			// Then
			if !ok {
				t.Fatalf("descriptor for %s not registered", tt.kind)
			}
			if !sameServices(desc.Hosts, tt.wantHosts) {
				t.Fatalf("hosts = %+v; want %+v", desc.Hosts, tt.wantHosts)
			}
			if desc.Contract != tt.wantContract {
				t.Fatalf("contract = %q; want %q", desc.Contract, tt.wantContract)
			}
			if desc.HookEnvelopes != tt.wantHookEnvelopes {
				t.Fatalf("HookEnvelopes = %v; want %v", desc.HookEnvelopes, tt.wantHookEnvelopes)
			}
		})
	}
}

func TestDescriptor_returnsImmutableHostSnapshot(t *testing.T) {
	// Given
	desc, ok := Descriptor(KindAuthProvider)
	if !ok {
		t.Fatal("auth-provider descriptor must be registered")
	}
	desc.Hosts[0] = ServiceRealtime

	// When
	got, ok := Descriptor(KindAuthProvider)

	// Then
	if !ok {
		t.Fatal("auth-provider descriptor must remain registered")
	}
	if len(got.Hosts) != 1 || got.Hosts[0] != ServiceGateway {
		t.Fatalf("hosts = %+v; want immutable [gateway]", got.Hosts)
	}
}

func TestHookEnvelope_marshalPreservesVersionedImmutableSourcePayload(t *testing.T) {
	// Given an immutable source payload snapshot for a private webhook delivery.
	payload := json.RawMessage(`{"text":"hello","sequence":7}`)
	env := NewHookEnvelope(HookEnvelopeData{
		Contract: HookEnvelopeContractV1,
		Event:    "chat_message",
		Version:  "1",
		TenantID: "tenant-a",
		RoomID:   "room-1",
		Payload:  payload,
	})
	payload[9] = 'X'

	// When
	raw, err := json.Marshal(env)

	// Then
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var got struct {
		Contract string          `json:"contract"`
		Event    string          `json:"event"`
		Version  string          `json:"version"`
		TenantID string          `json:"tenantId"`
		RoomID   string          `json:"roomId"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if got.Contract != string(HookEnvelopeContractV1) || got.Event != "chat_message" || got.Version != "1" || got.TenantID != "tenant-a" || got.RoomID != "room-1" {
		t.Fatalf("metadata = %+v; want versioned chat_message envelope", got)
	}
	if string(got.Payload) != `{"text":"hello","sequence":7}` {
		t.Fatalf("payload = %s; want original immutable snapshot", got.Payload)
	}
	if string(env.Payload()) != `{"text":"hello","sequence":7}` {
		t.Fatalf("payload getter = %s; want original immutable snapshot", env.Payload())
	}
}

func sameServices(got, want []Service) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
