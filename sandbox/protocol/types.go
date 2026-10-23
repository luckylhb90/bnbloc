// Package protocol defines Sandbox Protocol v1, the HTTP+JSON wire contract
// bnbloc uses to drive any conforming sandbox runtime: a local microVM
// sidecar supervised by the desktop, or a remote sandbox service. The
// desktop, the bridge and the conformance suite speak only this contract,
// never a specific VMM.
//
// Model: a MACHINE is one isolated Linux guest (a microVM locally; a remote
// VM elsewhere). bnbloc runs ONE long-lived machine per member computer and
// isolates rooms inside it as SPACES: a space is a room-scoped guest user
// plus its workspace mounts (host directories shared into the guest, or
// guest-local disk directories), default exec environment and an optional
// egress policy. Agent CLIs stay on the host; the commands they run are
// routed into the room's space through the exec endpoints, which translate
// host paths through the space's declared mounts.
//
// The wire format is the explicit camelCase JSON tags in this file; every
// field carries a tag (an untagged field would silently serialize
// PascalCase). docs/architecture/sandbox-protocol.md is the prose contract.
package protocol

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

// ContractV1 is the component contract a sandbox package declares
// (bnbloc.sandbox.package/v1) and that discovery echoes.
const ContractV1 = "bnbloc.sandbox.contract/v1"

const (
	PathPrefix    = "/sandbox/v1"
	PathDiscover  = PathPrefix + "/discover"
	PathHealth    = PathPrefix + "/healthz"
	PathImages    = PathPrefix + "/images"
	PathMachines  = PathPrefix + "/machines"
	PathSnapshots = PathPrefix + "/snapshots"
	PathPools     = PathPrefix + "/pools"

	SupportedMinVersion = "1.0"
	SupportedMaxVersion = "1.0"
)

// Content types of the non-JSON bodies.
const (
	ContentTypeJSON   = "application/json"
	ContentTypeNDJSON = "application/x-ndjson"
	ContentTypeTar    = "application/x-tar"
	ContentTypeOctet  = "application/octet-stream"
)

// Capability names advertised in Discovery.Capabilities. CapabilityExec is
// mandatory for a v1 implementation (it is listed so a future reduced
// profile can omit it); every other capability is optional and gated by
// discovery exactly like Core Protocol optional capabilities: not
// advertising one is conforming, advertising one and failing it is not.
const (
	CapabilityExec = "exec"
	// CapabilityTTY: exec with a pseudo-terminal (tty, rows/cols, resize).
	CapabilityTTY = "tty"
	// CapabilitySpaces: room-scoped spaces (guest user + mounts) and
	// space-level exec/files. Required by bnbloc's shared-machine mode.
	CapabilitySpaces = "spaces"
	// CapabilityShares: host directories shared into the guest at machine
	// creation (virtio-fs or equivalent), mountable into spaces.
	CapabilityShares = "shares"
	CapabilityPorts  = "ports"
	CapabilityDisks  = "disks"
	// CapabilitySnapshots: full (memory + disk) snapshots and restore.
	CapabilitySnapshots    = "snapshots"
	CapabilityPause        = "pause"
	CapabilityEgressPolicy = "egressPolicy"
	// CapabilitySpaceEgress: a per-space egress policy narrower than the
	// machine's.
	CapabilitySpaceEgress = "spaceEgress"
	// CapabilityEgressHosts: egress mode public may carry rules, hosts
	// reachable in addition to the public internet, private addresses
	// included (docs/prd/2026-10-21-sandbox-private-hosts.md).
	CapabilityEgressHosts = "egressHosts"
	CapabilitySecrets     = "secrets"
	CapabilityFork        = "fork"
	CapabilityWarmPool    = "warmPool"
	// CapabilityExecTokens: space-scoped exec tokens (POST
	// .../spaces/{space}/exec-tokens). Required by bnbloc's hybrid execution:
	// the admin bearer never reaches an agent process; a core receives only a
	// short-lived token that can run commands in its own room's space.
	CapabilityExecTokens = "execTokens"
	// CapabilityReadOnlyMounts: a shared space mount with readOnly:true is
	// enforced by the sandbox, not by the guest's goodwill: a write, a create
	// or a delete through it fails in the guest, and the host folder is never
	// touched. A component's mounts (the bridge's mount contributions) are
	// applied only to a backend that advertises it.
	CapabilityReadOnlyMounts = "readOnlyMounts"
	// CapabilityDurableHomes: a space's home outlives its machine. It is the
	// same on every machine created from an image that the space name is
	// ensured on, as the space's uid is, and only a delete with purge
	// removes it (docs/prd/2026-10-21-durable-sandbox-homes.md). A space's
	// commands find tools installed in it by name: PATH starts with its
	// ~/.local/bin, ~/.bun/bin, ~/.cargo/bin and ~/go/bin, and npm's global
	// prefix is ~/.local unless the run sets NPM_CONFIG_PREFIX.
	CapabilityDurableHomes = "durableHomes"
	// CapabilityHostProxy: a machine created with HostPorts reaches those
	// host loopback ports, and nothing else of the host, at
	// Discovery.HostAlias:<port>, whatever its egress mode. The bridge's
	// egress proxy listens there for the guests' commands
	// (docs/architecture/service-components.md).
	CapabilityHostProxy = "hostProxy"
)

// Capabilities lists every v1 capability name (validation registry).
var Capabilities = []string{
	CapabilityExec, CapabilityTTY, CapabilitySpaces, CapabilityShares, CapabilityPorts, CapabilityDisks,
	CapabilitySnapshots, CapabilityPause, CapabilityEgressPolicy, CapabilitySpaceEgress, CapabilityEgressHosts, CapabilitySecrets,
	CapabilityFork, CapabilityWarmPool, CapabilityExecTokens, CapabilityReadOnlyMounts, CapabilityDurableHomes, CapabilityHostProxy,
}

// KnownCapability reports whether name is a v1 capability.
func KnownCapability(name string) bool {
	for _, c := range Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// ProtocolRange is the shared {min,max} version range shape of every bnbloc
// HTTP protocol.
type ProtocolRange = coreprotocol.ProtocolRange

// Compatible reports whether a peer's advertised range shares a major
// version with this package's supported range (the Core Protocol rule).
func Compatible(remote ProtocolRange) error {
	return coreprotocol.CompatibleRange("sandbox protocol", ProtocolRange{Min: SupportedMinVersion, Max: SupportedMaxVersion}, remote)
}

// --- errors ---

// ErrorCode is a stable, machine-readable error code. Clients branch on the
// code, never on the message.
type ErrorCode string

const (
	ErrorBadRequest         ErrorCode = "bad_request"
	ErrorUnauthorized       ErrorCode = "unauthorized"
	ErrorForbidden          ErrorCode = "forbidden"
	ErrorNotFound           ErrorCode = "not_found"
	ErrorMachineNotFound    ErrorCode = "machine_not_found"
	ErrorSpaceNotFound      ErrorCode = "space_not_found"
	ErrorExecNotFound       ErrorCode = "exec_not_found"
	ErrorImageNotFound      ErrorCode = "image_not_found"
	ErrorSnapshotNotFound   ErrorCode = "snapshot_not_found"
	ErrorPoolNotFound       ErrorCode = "pool_not_found"
	ErrorFileNotFound       ErrorCode = "file_not_found"
	ErrorPathNotMapped      ErrorCode = "path_not_mapped"
	ErrorInvalidState       ErrorCode = "invalid_state"
	ErrorConflict           ErrorCode = "conflict"
	ErrorCapabilityMissing  ErrorCode = "capability_missing"
	ErrorVersionUnsupported ErrorCode = "version_unsupported"
	ErrorLimitExceeded      ErrorCode = "limit_exceeded"
	ErrorImagePullFailed    ErrorCode = "image_pull_failed"
	ErrorTimeout            ErrorCode = "timeout"
	ErrorBackendUnavailable ErrorCode = "backend_unavailable"
	ErrorInternal           ErrorCode = "internal"
)

// ErrorEnvelope is the body of every non-2xx response: {error:{code,message}}.
// It mirrors the Core Protocol envelope shape on purpose (one error idiom
// across bnbloc protocols) but is a distinct contract with its own code set.
type ErrorEnvelope struct {
	Error ProtocolError `json:"error"`
}

// ProtocolError is the envelope's error. Retryable is the implementation's
// optional verdict on repeating the request (absent: core.Retryable decides
// from the status code), the same rule as the Core Protocol's.
type ProtocolError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable *bool     `json:"retryable,omitempty"`
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "sandbox protocol: <nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("sandbox protocol: %s", e.Code)
	}
	return fmt.Sprintf("sandbox protocol: %s: %s", e.Code, e.Message)
}

// RetryHint implements core.RetryHinter.
func (e *ProtocolError) RetryHint() (retry, stated bool) {
	if e == nil || e.Retryable == nil {
		return false, false
	}
	return *e.Retryable, true
}

// --- discovery ---

// Isolation is the strength of the boundary between guest and host. Policy
// (e.g. "borrowed drives require a VM") keys on it, so an implementation
// must report it truthfully.
type Isolation string

const (
	// IsolationMicroVM is a hardware-virtualized guest kernel (libkrun,
	// Firecracker, Cloud Hypervisor, Virtualization.framework).
	IsolationMicroVM Isolation = "microvm"
	// IsolationVM is a full virtual machine (heavier, same boundary class).
	IsolationVM Isolation = "vm"
	// IsolationOS is an OS-level sandbox (Seatbelt, bubblewrap+Landlock).
	// It is the weak tier.
	IsolationOS Isolation = "os"
)

// Backend describes the implementation's isolation technology.
type Backend struct {
	Name      string    `json:"name"`
	Version   string    `json:"version,omitempty"`
	Isolation Isolation `json:"isolation"`
}

// Limits are the implementation's hard bounds; zero means "not stated".
type Limits struct {
	MaxMachines       int   `json:"maxMachines,omitempty"`
	MaxSpaces         int   `json:"maxSpaces,omitempty"`
	MaxCPUs           int   `json:"maxCpus,omitempty"`
	MaxMemoryMiB      int   `json:"maxMemoryMiB,omitempty"`
	MaxDiskMiB        int   `json:"maxDiskMiB,omitempty"`
	MaxFileBytes      int64 `json:"maxFileBytes,omitempty"`
	MaxRunOutputBytes int64 `json:"maxRunOutputBytes,omitempty"`
	MaxPoolSize       int   `json:"maxPoolSize,omitempty"`
}

// Discovery is GET /sandbox/v1/discover.
type Discovery struct {
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	Contract     string        `json:"contract"`
	Protocol     ProtocolRange `json:"protocol"`
	Capabilities []string      `json:"capabilities"`
	Backend      Backend       `json:"backend"`
	Limits       Limits        `json:"limits,omitempty"`
	// HostAlias is the name a guest reaches the host's HostPorts at
	// (hostProxy; "" without it).
	HostAlias string `json:"hostAlias,omitempty"`
}

// HasCapability reports whether discovery advertises capability.
func HasCapability(d Discovery, capability string) bool {
	for _, c := range d.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

type HealthResponse struct {
	OK bool `json:"ok"`
}

// --- images ---

type ImageState string

const (
	ImageReady ImageState = "ready"
)

// Image is one prepared (pulled, unpacked, cached) OCI image.
type Image struct {
	Ref       string     `json:"ref"`
	Digest    string     `json:"digest,omitempty"`
	SizeBytes int64      `json:"sizeBytes,omitempty"`
	State     ImageState `json:"state"`
	// PulledAtMs is when the image was prepared on this implementation.
	PulledAtMs int64 `json:"pulledAtMs,omitempty"`
}

// PullImageRequest is POST /sandbox/v1/images: prepare an image by OCI ref
// (synchronous; an already cached ref returns immediately). Refs are OCI
// references only: implementations must refuse host paths.
type PullImageRequest struct {
	Ref string `json:"ref"`
}

type ListImagesResponse struct {
	Images []Image `json:"images"`
}

// --- machines ---

type State string

const (
	StateCreating State = "creating"
	StateRunning  State = "running"
	StatePaused   State = "paused"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Disk is a private block volume (ext4 on a sparse disk image) created with
// the machine and removed with it: fast guest-local storage.
type Disk struct {
	Name      string `json:"name"`
	SizeMiB   int    `json:"sizeMiB"`
	MountPath string `json:"mountPath"`
}

// Share is a host directory shared into the guest at machine creation
// (virtio-fs locally). Guest-side ownership and permission changes are
// virtualized: they never alter the host files' owner or mode. A share is
// mounted root-only; spaces expose sub-directories of it to their user.
type Share struct {
	Name      string `json:"name"`
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath,omitempty"` // default /mnt/shares/<name>
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

const (
	PortTCP = "tcp"
	PortUDP = "udp"
)

// PortRequest asks for a guest port to be published on the host. The host
// side is always chosen by the implementation (loopback, a free port) and
// returned as a PortBinding.
type PortRequest struct {
	Guest    int    `json:"guest"`
	Protocol string `json:"protocol,omitempty"` // tcp (default) | udp
}

// PortBinding is one published guest port.
type PortBinding struct {
	Guest    int    `json:"guest"`
	Protocol string `json:"protocol"`
	HostIP   string `json:"hostIp"`
	HostPort int    `json:"hostPort"`
}

// EgressMode is the outbound network policy. Egress is deny-by-default: an
// omitted policy is EgressNone, on every implementation.
type EgressMode string

const (
	// EgressNone: no outbound network at all (the default).
	EgressNone EgressMode = "none"
	// EgressAllowlist: only the listed rules are reachable.
	EgressAllowlist EgressMode = "allowlist"
	// EgressPublic: the public internet; private/loopback/link-local
	// ranges (including the host) stay unreachable, but for the hosts its
	// rules list (requires egressHosts; without it public takes no rules).
	EgressPublic EgressMode = "public"
)

// EgressRule allows outbound TCP to Host (an exact DNS name, a "*.suffix"
// wildcard of at least two labels, or an IP literal) on Port (0 = any port).
type EgressRule struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}

type Egress struct {
	Mode  EgressMode   `json:"mode"`
	Rules []EgressRule `json:"rules,omitempty"`
}

// SecretRequest binds a host-held secret to the machine. The guest sees the
// env var Name set to an opaque placeholder; the implementation substitutes
// the real Value on the host side only in outbound traffic to Hosts. The
// value is supplied per create by the caller and is never returned by any
// endpoint. Substitution does not open egress: Hosts must also be reachable
// under the machine's egress policy.
type SecretRequest struct {
	Name  string   `json:"name"`
	Value string   `json:"value"`
	Hosts []string `json:"hosts"`
}

// Secret is a secret binding as reported back: never the value.
type Secret struct {
	Name        string   `json:"name"`
	Hosts       []string `json:"hosts"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// Labels tie a resource to its bnbloc owner. On a space the key is
// (room, core, provider) — the bridge's session key; on the shared machine
// only Owner is usually set.
type Labels struct {
	Room     string `json:"room,omitempty"`
	Core     string `json:"core,omitempty"`
	Provider string `json:"provider,omitempty"`
	Owner    string `json:"owner,omitempty"`
}

// CreateMachineRequest is POST /sandbox/v1/machines. The machine boots
// before the response is sent (state running).
type CreateMachineRequest struct {
	// Name is an optional caller-chosen unique name; every {machine} path
	// segment accepts the id or the name.
	Name      string            `json:"name,omitempty"`
	Image     string            `json:"image"`
	CPUs      int               `json:"cpus,omitempty"`
	MemoryMiB int               `json:"memoryMiB,omitempty"`
	Disks     []Disk            `json:"disks,omitempty"`
	Shares    []Share           `json:"shares,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Workdir   string            `json:"workdir,omitempty"`
	Ports     []PortRequest     `json:"ports,omitempty"`
	Egress    *Egress           `json:"egress,omitempty"`
	Secrets   []SecretRequest   `json:"secrets,omitempty"`
	Labels    Labels            `json:"labels,omitempty"`
	// HostPorts are host loopback ports the guest reaches at
	// Discovery.HostAlias (requires hostProxy; at most 4).
	HostPorts []int `json:"hostPorts,omitempty"`
}

type SourceKind string

const (
	SourceImage    SourceKind = "image"
	SourceSnapshot SourceKind = "snapshot"
	SourceFork     SourceKind = "fork"
	SourcePool     SourceKind = "pool"
)

// Source records where a machine came from.
type Source struct {
	Kind       SourceKind `json:"kind"`
	SnapshotID string     `json:"snapshotId,omitempty"`
	MachineID  string     `json:"machineId,omitempty"`
	Pool       string     `json:"pool,omitempty"`
	// Warm is true when a pool claim was served by an already-prepared
	// member (false: the pool was empty and the claim restored on demand).
	Warm bool `json:"warm,omitempty"`
}

// Timings are server-measured durations in milliseconds.
type Timings struct {
	// CreateMs: request received → machine running (create, restore, fork,
	// or the restore behind a cold pool claim).
	CreateMs int64 `json:"createMs,omitempty"`
	// RestoreMs: the snapshot-restore part of CreateMs (snapshot/pool sources).
	RestoreMs int64 `json:"restoreMs,omitempty"`
	// ClaimMs: pool claim request → machine handed over.
	ClaimMs int64 `json:"claimMs,omitempty"`
	// LastOp/LastOpMs: the most recent lifecycle operation and its duration.
	LastOp   string `json:"lastOp,omitempty"`
	LastOpMs int64  `json:"lastOpMs,omitempty"`
}

// Machine is one machine as reported by every machine endpoint.
type Machine struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Image       string        `json:"image"`
	State       State         `json:"state"`
	CPUs        int           `json:"cpus,omitempty"`
	MemoryMiB   int           `json:"memoryMiB,omitempty"`
	Disks       []Disk        `json:"disks,omitempty"`
	Shares      []Share       `json:"shares,omitempty"`
	Ports       []PortBinding `json:"ports,omitempty"`
	Egress      Egress        `json:"egress"`
	Secrets     []Secret      `json:"secrets,omitempty"`
	Labels      Labels        `json:"labels,omitempty"`
	HostPorts   []int         `json:"hostPorts,omitempty"`
	Source      Source        `json:"source"`
	Spaces      int           `json:"spaces"`
	CreatedAtMs int64         `json:"createdAtMs"`
	Timings     Timings       `json:"timings"`
	// Error is the failure reason when State is failed.
	Error string `json:"error,omitempty"`
}

type ListMachinesResponse struct {
	Machines []Machine `json:"machines"`
}

// Lifecycle actions: POST /sandbox/v1/machines/{machine}/{action} answers
// the updated Machine. pause/resume require the pause capability. A start
// re-establishes every space (users, mounts) on the fresh boot.
const (
	ActionStart  = "start"
	ActionStop   = "stop"
	ActionPause  = "pause"
	ActionResume = "resume"
)

// --- spaces ---

type MountKind string

const (
	// MountShared exposes a sub-directory of a machine Share: the same
	// files the host sees, live in both directions (hybrid mode: a host CLI
	// edits, guest commands build). HostPath must lie inside a share.
	MountShared MountKind = "shared"
	// MountDisk is a guest-local directory (fast ext4, clone mode): content
	// moves in and out through the files/archive endpoints. HostPath, when
	// set, declares which host directory the guest copy corresponds to, so
	// host paths still translate.
	MountDisk MountKind = "disk"
)

// SpaceMount maps one host path to one guest path inside a space.
type SpaceMount struct {
	Kind      MountKind `json:"kind"`
	HostPath  string    `json:"hostPath,omitempty"`
	GuestPath string    `json:"guestPath"`
	ReadOnly  bool      `json:"readOnly,omitempty"`
	// Share is the machine share a shared mount resolves into (reported).
	Share string `json:"share,omitempty"`
}

// SpaceRequest is PUT /sandbox/v1/machines/{machine}/spaces/{space}: ensure
// (create or converge) a room-scoped space. Idempotent: repeating the same
// request is a no-op; a changed request converges users and mounts.
type SpaceRequest struct {
	// User is the guest account commands run as (default "sp-<space>"). It
	// is created if missing, with a private home and no sudo.
	User string `json:"user,omitempty"`
	// Workdir is the default exec cwd (default: the first mount's
	// guestPath, else the user's home).
	Workdir string       `json:"workdir,omitempty"`
	Mounts  []SpaceMount `json:"mounts,omitempty"`
	// Env is the default environment of every exec in the space.
	Env map[string]string `json:"env,omitempty"`
	// EnvAllowlist bounds which env names an exec request may pass (exact
	// names, or a prefix ending in "*"). Empty = any name. Names outside
	// the allowlist are dropped, never passed into the guest.
	EnvAllowlist []string `json:"envAllowlist,omitempty"`
	// Egress narrows the machine's egress for this space's processes
	// (requires spaceEgress).
	Egress *Egress `json:"egress,omitempty"`
	Labels Labels  `json:"labels,omitempty"`
}

// Space is one space as reported back.
type Space struct {
	Name         string            `json:"name"`
	MachineID    string            `json:"machineId"`
	User         string            `json:"user"`
	UID          int               `json:"uid"`
	GID          int               `json:"gid"`
	Home         string            `json:"home"`
	Workdir      string            `json:"workdir"`
	Mounts       []SpaceMount      `json:"mounts,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	EnvAllowlist []string          `json:"envAllowlist,omitempty"`
	Egress       *Egress           `json:"egress,omitempty"`
	Labels       Labels            `json:"labels,omitempty"`
	CreatedAtMs  int64             `json:"createdAtMs"`
}

type ListSpacesResponse struct {
	Spaces []Space `json:"spaces"`
}

// --- exec ---

// ExecRequest starts one process. Command is argv (Command[0] is resolved
// through the guest PATH; use ["sh","-c",script] for shell syntax).
//
// At machine level the process runs as User (default root). At space level
// it always runs as the space user, its env is the space env overlaid with
// the allowlisted request env, and its cwd is Workdir (a guest path) or
// HostWorkdir (a host path translated through the space's mounts; outside
// every mount it is path_not_mapped), else the space workdir.
type ExecRequest struct {
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env,omitempty"`
	Workdir     string            `json:"workdir,omitempty"`
	HostWorkdir string            `json:"hostWorkdir,omitempty"`
	User        string            `json:"user,omitempty"`
	// StdinBase64 is the whole stdin, closed after it is written.
	StdinBase64 string `json:"stdinBase64,omitempty"`
	// StdinPipe keeps stdin open for POST .../execs/{execId}/stdin (streamed
	// exec only); exclusive with StdinBase64.
	StdinPipe bool `json:"stdinPipe,omitempty"`
	// TTY allocates a pseudo-terminal of Rows x Cols (requires tty); output
	// then arrives as stdout only.
	TTY  bool `json:"tty,omitempty"`
	Rows int  `json:"rows,omitempty"`
	Cols int  `json:"cols,omitempty"`
	// TimeoutMs kills the process after this long (0 = no timeout for the
	// streamed exec; the implementation's default for run).
	TimeoutMs int64 `json:"timeoutMs,omitempty"`
}

type ExecEventType string

const (
	ExecStarted ExecEventType = "started"
	ExecStdout  ExecEventType = "stdout"
	ExecStderr  ExecEventType = "stderr"
	ExecExit    ExecEventType = "exit"
	ExecError   ExecEventType = "error"
)

// ExecEvent is one line of the POST .../exec NDJSON stream. The stream is
// started (carrying ExecID, the handle for stdin/resize/signal), then any
// number of stdout/stderr chunks, then exactly one terminal event (exit or
// error), after which the response ends.
type ExecEvent struct {
	Type       ExecEventType  `json:"type"`
	ExecID     string         `json:"execId,omitempty"`
	Pid        int            `json:"pid,omitempty"`
	DataBase64 string         `json:"dataBase64,omitempty"`
	ExitCode   *int           `json:"exitCode,omitempty"`
	TimedOut   bool           `json:"timedOut,omitempty"`
	DurationMs int64          `json:"durationMs,omitempty"`
	Error      *ProtocolError `json:"error,omitempty"`
}

// ResizeRequest is POST .../execs/{execId}/resize (tty execs).
type ResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// SignalRequest is POST .../execs/{execId}/signal (a POSIX signal number).
type SignalRequest struct {
	Signal int `json:"signal"`
}

// RunResult is POST .../run: the collected, non-streamed form of exec.
type RunResult struct {
	ExitCode     int    `json:"exitCode"`
	StdoutBase64 string `json:"stdoutBase64"`
	StderrBase64 string `json:"stderrBase64"`
	DurationMs   int64  `json:"durationMs"`
	TimedOut     bool   `json:"timedOut,omitempty"`
	// Truncated is set when output exceeded Limits.MaxRunOutputBytes.
	Truncated bool `json:"truncated,omitempty"`
}

// --- space-scoped exec tokens ---

// ExecTokenRequest is POST /sandbox/v1/machines/{machine}/spaces/{space}/exec-tokens
// (admin bearer only): mint a short-lived bearer scoped to ONE space.
// TTLSeconds 0 = the implementation default; implementations may shorten a
// requested TTL (the answer's ExpiresAtMs is authoritative).
//
// The minted token authorizes ONLY, as that space's user:
//   - POST .../machines/{machine}/spaces/{space}/run and .../exec, and
//   - the control endpoints (POST .../machines/{machine}/execs/{execId}/stdin,
//     resize, signal) of execs started in the same space by any of its exec
//     tokens: a caller renews a long-lived exec's token (a turn that runs
//     for hours), and the renewed token keeps controlling it.
//
// {machine} may be the machine id or name, as everywhere. Every other route
// (discover, machines, other spaces, files, archives, minting, ...) answers
// 403 forbidden for an exec token; an expired or unknown token is 401
// unauthorized. Deleting the space (or its machine) revokes its tokens; a
// space re-ensure keeps them valid.
type ExecTokenRequest struct {
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
}

// ExecToken is the minted space-scoped bearer. Token is a secret: never log it.
type ExecToken struct {
	Token       string `json:"token"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// --- snapshots ---

type SnapshotKind string

// SnapshotFull captures disk + memory + device state (restorable to a
// running machine without a boot).
const SnapshotFull SnapshotKind = "full"

// CreateSnapshotRequest is POST /sandbox/v1/machines/{machine}/snapshots.
// The source keeps the state it had before the capture. An implementation
// that cannot restore some captured resource (e.g. secret bindings) refuses
// the snapshot with capability_missing rather than producing an
// unrestorable one.
type CreateSnapshotRequest struct {
	Name string `json:"name,omitempty"`
}

type Snapshot struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	MachineID   string       `json:"machineId,omitempty"`
	Image       string       `json:"image,omitempty"`
	Kind        SnapshotKind `json:"kind"`
	CPUs        int          `json:"cpus,omitempty"`
	MemoryMiB   int          `json:"memoryMiB,omitempty"`
	Shares      []Share      `json:"shares,omitempty"`
	Spaces      []string     `json:"spaces,omitempty"`
	SizeBytes   int64        `json:"sizeBytes,omitempty"`
	CreatedAtMs int64        `json:"createdAtMs"`
	DurationMs  int64        `json:"durationMs,omitempty"`
}

type ListSnapshotsResponse struct {
	Snapshots []Snapshot `json:"snapshots"`
}

// RestoreRequest is POST /sandbox/v1/snapshots/{id}/restore: a NEW machine
// from a snapshot. Image, CPU, memory, disks, spaces and secret bindings
// come from the snapshot; host-side resources are bound fresh: published
// ports, labels, and shares (each captured share is re-bound to the host
// path given here by name, else to its captured host path). Egress (when
// set) replaces the captured policy.
type RestoreRequest struct {
	Name string `json:"name,omitempty"`
	// CowMemory maps the snapshot memory copy-on-write (fast, shares pages
	// with other restores). Nil means true.
	CowMemory *bool         `json:"cowMemory,omitempty"`
	Ports     []PortRequest `json:"ports,omitempty"`
	Shares    []Share       `json:"shares,omitempty"`
	Egress    *Egress       `json:"egress,omitempty"`
	Labels    Labels        `json:"labels,omitempty"`
}

// ForkRequest is POST /sandbox/v1/machines/{machine}/fork: a NEW machine
// that continues the source's live execution state (copy-on-write disk and
// memory) without a durable snapshot.
type ForkRequest struct {
	Name   string        `json:"name,omitempty"`
	Ports  []PortRequest `json:"ports,omitempty"`
	Labels Labels        `json:"labels,omitempty"`
}

// --- warm pools ---

// CreatePoolRequest is POST /sandbox/v1/pools: keep Size machines restored
// from SnapshotID ready to claim. Ports, Shares and Egress are the
// per-member restore template.
type CreatePoolRequest struct {
	Name       string        `json:"name"`
	SnapshotID string        `json:"snapshotId"`
	Size       int           `json:"size"`
	Ports      []PortRequest `json:"ports,omitempty"`
	Shares     []Share       `json:"shares,omitempty"`
	Egress     *Egress       `json:"egress,omitempty"`
}

type Pool struct {
	Name        string        `json:"name"`
	SnapshotID  string        `json:"snapshotId"`
	Size        int           `json:"size"`
	Ready       int           `json:"ready"`
	Warming     int           `json:"warming"`
	Ports       []PortRequest `json:"ports,omitempty"`
	Egress      Egress        `json:"egress"`
	CreatedAtMs int64         `json:"createdAtMs"`
	// LastError is the most recent refill failure (empty when healthy).
	LastError string `json:"lastError,omitempty"`
}

type ListPoolsResponse struct {
	Pools []Pool `json:"pools"`
}

// ClaimRequest is POST /sandbox/v1/pools/{name}/claim: hand one ready
// member over (it leaves the pool, which refills in the background). An
// empty pool restores one on demand (Source.Warm=false) rather than failing.
type ClaimRequest struct {
	Name   string `json:"name,omitempty"`
	Labels Labels `json:"labels,omitempty"`
}

// --- paths ---

func escape(v string) string { return url.PathEscape(v) }

// MachinePath addresses a machine by id or name.
func MachinePath(machine string) string { return PathMachines + "/" + escape(machine) }

func MachineActionPath(machine, action string) string { return MachinePath(machine) + "/" + action }

func MachineExecPath(machine string) string { return MachinePath(machine) + "/exec" }

func MachineRunPath(machine string) string { return MachinePath(machine) + "/run" }

func MachineForkPath(machine string) string { return MachinePath(machine) + "/fork" }

func MachineSnapshotsPath(machine string) string { return MachinePath(machine) + "/snapshots" }

// MachineFilesPath addresses one guest file (absolute guest path).
func MachineFilesPath(machine, guestPath string) string {
	return MachinePath(machine) + "/files?path=" + url.QueryEscape(guestPath)
}

// MachineArchivePath addresses a guest directory as a tar stream.
func MachineArchivePath(machine, guestDir string) string {
	return MachinePath(machine) + "/archive?path=" + url.QueryEscape(guestDir)
}

// ExecControlPath addresses a running exec's control endpoint
// (stdin | resize | signal).
func ExecControlPath(machine, execID, control string) string {
	return MachinePath(machine) + "/execs/" + escape(execID) + "/" + control
}

func SpacesPath(machine string) string { return MachinePath(machine) + "/spaces" }

func SpacePath(machine, space string) string { return SpacesPath(machine) + "/" + escape(space) }

func SpaceExecPath(machine, space string) string { return SpacePath(machine, space) + "/exec" }

func SpaceRunPath(machine, space string) string { return SpacePath(machine, space) + "/run" }

// SpaceExecTokensPath mints space-scoped exec tokens (admin bearer only).
func SpaceExecTokensPath(machine, space string) string {
	return SpacePath(machine, space) + "/exec-tokens"
}

// SpaceFilesPath addresses a file in a space by guest path; files written
// here are owned by the space user.
func SpaceFilesPath(machine, space, guestPath string) string {
	return SpacePath(machine, space) + "/files?path=" + url.QueryEscape(guestPath)
}

// SpaceHostFilesPath addresses a file in a space by HOST path (translated
// through the space mounts).
func SpaceHostFilesPath(machine, space, hostPath string) string {
	return SpacePath(machine, space) + "/files?hostPath=" + url.QueryEscape(hostPath)
}

func SpaceArchivePath(machine, space, guestDir string) string {
	return SpacePath(machine, space) + "/archive?path=" + url.QueryEscape(guestDir)
}

func SnapshotPath(id string) string { return PathSnapshots + "/" + escape(id) }

func SnapshotRestorePath(id string) string { return SnapshotPath(id) + "/restore" }

func PoolPath(name string) string { return PathPools + "/" + escape(name) }

func PoolClaimPath(name string) string { return PoolPath(name) + "/claim" }

// ListMachinesPath filters the machine list by labels (empty = no filter).
func ListMachinesPath(l Labels) string { return withLabels(PathMachines, l) }

// ListSpacesPath filters a machine's spaces by labels.
func ListSpacesPath(machine string, l Labels) string { return withLabels(SpacesPath(machine), l) }

func withLabels(path string, l Labels) string {
	q := url.Values{}
	for k, v := range map[string]string{"room": l.Room, "core": l.Core, "provider": l.Provider, "owner": l.Owner} {
		if strings.TrimSpace(v) != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// MapHostPath translates a host path through a space's mounts: the longest
// mount whose HostPath contains hostPath wins. Paths are absolute POSIX
// paths, cleaned first (so ".." cannot escape a mount). ok is false when no
// mount maps it. It is the ONE translation rule; implementations and
// wrappers must agree on it.
func MapHostPath(mounts []SpaceMount, hostPath string) (string, bool) {
	if !strings.HasPrefix(hostPath, "/") {
		return "", false
	}
	clean := strings.TrimRight(path.Clean(hostPath), "/")
	best, bestLen := "", -1
	for _, m := range mounts {
		if !strings.HasPrefix(m.HostPath, "/") {
			continue
		}
		root := strings.TrimRight(path.Clean(m.HostPath), "/")
		if clean == root || strings.HasPrefix(clean, root+"/") {
			if len(root) > bestLen {
				best = strings.TrimRight(m.GuestPath, "/") + strings.TrimPrefix(clean, root)
				bestLen = len(root)
			}
		}
	}
	if bestLen < 0 {
		return "", false
	}
	if best == "" {
		best = "/"
	}
	return best, true
}
