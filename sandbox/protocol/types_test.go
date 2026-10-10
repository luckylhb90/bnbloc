package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// wireTypes is every Sandbox Protocol v1 body type.
var wireTypes = []any{
	ErrorEnvelope{}, ProtocolError{}, Backend{}, Limits{}, Discovery{}, HealthResponse{},
	Image{}, PullImageRequest{}, ListImagesResponse{}, Disk{}, PortRequest{}, PortBinding{},
	EgressRule{}, Egress{}, SecretRequest{}, Secret{}, Labels{}, Share{}, CreateMachineRequest{}, Source{},
	Timings{}, Machine{}, ListMachinesResponse{}, SpaceMount{}, SpaceRequest{}, Space{}, ListSpacesResponse{},
	ExecRequest{}, ExecEvent{}, ResizeRequest{}, SignalRequest{}, RunResult{},
	CreateSnapshotRequest{}, Snapshot{}, ListSnapshotsResponse{}, RestoreRequest{}, ForkRequest{},
	CreatePoolRequest{}, Pool{}, ListPoolsResponse{}, ClaimRequest{},
}

// The wire contract is the explicit camelCase tags: an untagged field would
// silently serialize PascalCase.
func TestWireTypes_everyFieldHasCamelCaseJSONTag(t *testing.T) {
	for _, v := range wireTypes {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				t.Errorf("%s.%s has no json tag", typ.Name(), f.Name)
				continue
			}
			if c := tag[0]; c < 'a' || c > 'z' {
				t.Errorf("%s.%s json tag %q is not camelCase", typ.Name(), f.Name, tag)
			}
		}
	}
}

func TestMachine_marshalsDocumentedKeys(t *testing.T) {
	// Given
	code := 0
	sb := Machine{ID: "s1", Name: "n", Image: "alpine", State: StateRunning, MemoryMiB: 512,
		Ports:  []PortBinding{{Guest: 8080, Protocol: PortTCP, HostIP: "127.0.0.1", HostPort: 40001}},
		Egress: Egress{Mode: EgressNone}, Secrets: []Secret{{Name: "K", Hosts: []string{"api.example.com"}}},
		Source: Source{Kind: SourceSnapshot, SnapshotID: "snap"}, CreatedAtMs: 1, Timings: Timings{RestoreMs: 3},
		Shares: []Share{{Name: "ws", HostPath: "/Users/m/rooms", GuestPath: "/mnt/shares/ws"}}}
	ev := ExecEvent{Type: ExecExit, ExitCode: &code}

	// When
	raw, _ := json.Marshal(sb)
	evRaw, _ := json.Marshal(ev)

	// Then
	for _, key := range []string{`"memoryMiB":512`, `"hostPort":40001`, `"hostIp":"127.0.0.1"`, `"snapshotId":"snap"`, `"restoreMs":3`, `"createdAtMs":1`, `"egress":{"mode":"none"}`, `"hostPath":"/Users/m/rooms"`, `"spaces":0`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("machine JSON %s missing %s", raw, key)
		}
	}
	if strings.Contains(string(raw), "value") {
		t.Errorf("reported secret carries a value field: %s", raw)
	}
	if string(evRaw) != `{"type":"exit","exitCode":0}` {
		t.Errorf("exit event = %s (exit code 0 must be explicit)", evRaw)
	}
}

func TestCompatible_negotiatesByMajorVersion(t *testing.T) {
	for _, tc := range []struct {
		r  ProtocolRange
		ok bool
	}{
		{ProtocolRange{Min: "1.0", Max: "1.0"}, true},
		{ProtocolRange{Min: "1.0", Max: "1.7"}, true},
		{ProtocolRange{Min: "0.9", Max: "1.2"}, true},
		{ProtocolRange{Min: "2.0", Max: "2.1"}, false},
		{ProtocolRange{Min: "0.1", Max: "0.9"}, false},
	} {
		err := Compatible(tc.r)
		if (err == nil) != tc.ok {
			t.Errorf("Compatible(%+v) = %v, want ok=%v", tc.r, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "sandbox protocol version mismatch") {
			t.Errorf("mismatch error = %q", err)
		}
	}
	if err := Compatible(ProtocolRange{Min: "x", Max: "1.0"}); err == nil {
		t.Errorf("malformed range accepted")
	}
}

func TestPaths_escapeIdentifiersAndQueries(t *testing.T) {
	if got := MachineActionPath("a/b", ActionPause); got != "/sandbox/v1/machines/a%2Fb/pause" {
		t.Errorf("action path = %s", got)
	}
	if got := MachineFilesPath("s", "/tmp/a b&c"); got != "/sandbox/v1/machines/s/files?path=%2Ftmp%2Fa+b%26c" {
		t.Errorf("files path = %s", got)
	}
	if got := ListMachinesPath(Labels{}); got != PathMachines {
		t.Errorf("unfiltered list = %s", got)
	}
	if got := ListSpacesPath("m", Labels{Room: "r1", Owner: "o"}); got != "/sandbox/v1/machines/m/spaces?owner=o&room=r1" {
		t.Errorf("filtered list = %s", got)
	}
	if got := SpaceHostFilesPath("m", "room 1", "/Users/x/a"); got != "/sandbox/v1/machines/m/spaces/room%201/files?hostPath=%2FUsers%2Fx%2Fa" {
		t.Errorf("space host files path = %s", got)
	}
	if got := ExecControlPath("m", "e1", "stdin"); got != "/sandbox/v1/machines/m/execs/e1/stdin" {
		t.Errorf("exec control path = %s", got)
	}
	if got := PoolClaimPath("warm"); got != "/sandbox/v1/pools/warm/claim" {
		t.Errorf("claim path = %s", got)
	}
}

func TestKnownCapability(t *testing.T) {
	for _, c := range Capabilities {
		if !KnownCapability(c) {
			t.Errorf("%s not known", c)
		}
	}
	if KnownCapability("interrupt") {
		t.Errorf("core capability accepted as sandbox capability")
	}
}

func TestMapHostPath_longestMountWinsAndDotDotCannotEscape(t *testing.T) {
	mounts := []SpaceMount{
		{Kind: MountShared, HostPath: "/Users/m/rooms/r1", GuestPath: "/workspace"},
		{Kind: MountDisk, HostPath: "/Users/m/rooms/r1/cache", GuestPath: "/cache"},
	}
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"/Users/m/rooms/r1", "/workspace", true},
		{"/Users/m/rooms/r1/", "/workspace", true},
		{"/Users/m/rooms/r1/src/main.go", "/workspace/src/main.go", true},
		{"/Users/m/rooms/r1/cache/x", "/cache/x", true},
		{"/Users/m/rooms/r10/x", "", false},
		{"/Users/m/rooms/r1/../r2/secret", "", false},
		{"relative/path", "", false},
		{"/etc/passwd", "", false},
	} {
		got, ok := MapHostPath(mounts, tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("MapHostPath(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
