package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/luckylhb90/bnbloc/core"
)

// Client drives one Sandbox Protocol v1 implementation through the shared
// core.REST plumbing: JSON calls and file reads are buffered (per-request
// timeout, bounded body); the streamed bodies (exec NDJSON, file writes, tar
// archives) use the same request builder and error mapping without a
// deadline beyond ctx. The client is stateless: every
// call takes the implementation's Endpoint {BaseURL, Token}. Machine
// arguments accept the machine id or its name.
type Client struct {
	REST core.REST
}

// NewClient returns a client; httpClient nil uses http.DefaultClient.
func NewClient(httpClient *http.Client) *Client {
	return &Client{REST: core.REST{Name: "sandbox", HTTP: httpClient, DecodeError: core.DecodeEnvelope[ProtocolError]()}}
}

// Discover fetches discovery and checks version compatibility.
func (c *Client) Discover(ctx context.Context, ep core.Endpoint) (Discovery, error) {
	var d Discovery
	if err := c.REST.JSON(ctx, http.MethodGet, ep, PathDiscover, nil, &d); err != nil {
		return Discovery{}, err
	}
	if err := Compatible(d.Protocol); err != nil {
		return Discovery{}, err
	}
	return d, nil
}

func (c *Client) PullImage(ctx context.Context, ep core.Endpoint, ref string) (Image, error) {
	var out Image
	err := c.REST.JSON(ctx, http.MethodPost, ep, PathImages, PullImageRequest{Ref: ref}, &out)
	return out, err
}

func (c *Client) ListImages(ctx context.Context, ep core.Endpoint) ([]Image, error) {
	var out ListImagesResponse
	err := c.REST.JSON(ctx, http.MethodGet, ep, PathImages, nil, &out)
	return out.Images, err
}

func (c *Client) CreateMachine(ctx context.Context, ep core.Endpoint, req CreateMachineRequest) (Machine, error) {
	var out Machine
	err := c.REST.JSON(ctx, http.MethodPost, ep, PathMachines, req, &out)
	return out, err
}

// GetMachine looks a machine up by id or name.
func (c *Client) GetMachine(ctx context.Context, ep core.Endpoint, machine string) (Machine, error) {
	var out Machine
	err := c.REST.JSON(ctx, http.MethodGet, ep, MachinePath(machine), nil, &out)
	return out, err
}

func (c *Client) ListMachines(ctx context.Context, ep core.Endpoint, filter Labels) ([]Machine, error) {
	var out ListMachinesResponse
	err := c.REST.JSON(ctx, http.MethodGet, ep, ListMachinesPath(filter), nil, &out)
	return out.Machines, err
}

// Lifecycle runs one lifecycle action (ActionStart/Stop/Pause/Resume).
func (c *Client) Lifecycle(ctx context.Context, ep core.Endpoint, machine, action string) (Machine, error) {
	var out Machine
	err := c.REST.JSON(ctx, http.MethodPost, ep, MachineActionPath(machine, action), struct{}{}, &out)
	return out, err
}

func (c *Client) DeleteMachine(ctx context.Context, ep core.Endpoint, machine string) error {
	return c.REST.JSON(ctx, http.MethodDelete, ep, MachinePath(machine), nil, nil)
}

// EnsureSpace creates or converges a space (idempotent PUT).
func (c *Client) EnsureSpace(ctx context.Context, ep core.Endpoint, machine, space string, req SpaceRequest) (Space, error) {
	var out Space
	err := c.REST.JSON(ctx, http.MethodPut, ep, SpacePath(machine, space), req, &out)
	return out, err
}

func (c *Client) GetSpace(ctx context.Context, ep core.Endpoint, machine, space string) (Space, error) {
	var out Space
	err := c.REST.JSON(ctx, http.MethodGet, ep, SpacePath(machine, space), nil, &out)
	return out, err
}

func (c *Client) ListSpaces(ctx context.Context, ep core.Endpoint, machine string, filter Labels) ([]Space, error) {
	var out ListSpacesResponse
	err := c.REST.JSON(ctx, http.MethodGet, ep, ListSpacesPath(machine, filter), nil, &out)
	return out.Spaces, err
}

// DeleteSpace removes a space: its processes are killed, its bind mounts
// and user removed; purge also deletes its guest-local (disk) directories.
// Host share contents are never deleted.
func (c *Client) DeleteSpace(ctx context.Context, ep core.Endpoint, machine, space string, purge bool) error {
	path := SpacePath(machine, space)
	if purge {
		path += "?purge=1"
	}
	return c.REST.JSON(ctx, http.MethodDelete, ep, path, nil, nil)
}

// MintExecToken mints a space-scoped exec token (requires the execTokens
// capability and the admin bearer). ttlSeconds 0 = the implementation default.
func (c *Client) MintExecToken(ctx context.Context, ep core.Endpoint, machine, space string, ttlSeconds int64) (ExecToken, error) {
	var out ExecToken
	err := c.REST.JSON(ctx, http.MethodPost, ep, SpaceExecTokensPath(machine, space), ExecTokenRequest{TTLSeconds: ttlSeconds}, &out)
	return out, err
}

// Run executes a process at machine level and returns its collected output.
func (c *Client) Run(ctx context.Context, ep core.Endpoint, machine string, req ExecRequest) (RunResult, error) {
	return c.run(ctx, ep, MachineRunPath(machine), req)
}

// SpaceRun executes a process in a space (as the space user).
func (c *Client) SpaceRun(ctx context.Context, ep core.Endpoint, machine, space string, req ExecRequest) (RunResult, error) {
	return c.run(ctx, ep, SpaceRunPath(machine, space), req)
}

func (c *Client) run(ctx context.Context, ep core.Endpoint, path string, req ExecRequest) (RunResult, error) {
	var out RunResult
	err := c.REST.JSON(ctx, http.MethodPost, ep, path, req, &out)
	return out, err
}

// Exec starts a machine-level process and calls onEvent for every NDJSON
// event until the terminal one (exit or error), which it also returns.
// Cancelling ctx closes the stream, which kills the process.
func (c *Client) Exec(ctx context.Context, ep core.Endpoint, machine string, req ExecRequest, onEvent func(ExecEvent)) (ExecEvent, error) {
	return c.exec(ctx, ep, MachineExecPath(machine), req, onEvent)
}

// SpaceExec is Exec inside a space (as the space user).
func (c *Client) SpaceExec(ctx context.Context, ep core.Endpoint, machine, space string, req ExecRequest, onEvent func(ExecEvent)) (ExecEvent, error) {
	return c.exec(ctx, ep, SpaceExecPath(machine, space), req, onEvent)
}

func (c *Client) exec(ctx context.Context, ep core.Endpoint, path string, req ExecRequest, onEvent func(ExecEvent)) (ExecEvent, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ExecEvent{}, err
	}
	resp, err := c.stream(ctx, ep, http.MethodPost, path, bytes.NewReader(body), ContentTypeJSON)
	if err != nil {
		return ExecEvent{}, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev ExecEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return ExecEvent{}, fmt.Errorf("decode exec event: %w", err)
		}
		if onEvent != nil {
			onEvent(ev)
		}
		if ev.Type == ExecExit || ev.Type == ExecError {
			return ev, nil
		}
	}
	if err := sc.Err(); err != nil {
		return ExecEvent{}, fmt.Errorf("read exec stream: %w", err)
	}
	return ExecEvent{}, errors.New("exec stream ended without a terminal event")
}

// WriteStdin feeds a running exec's piped stdin; eof closes it.
func (c *Client) WriteStdin(ctx context.Context, ep core.Endpoint, machine, execID string, data []byte, eof bool) error {
	path := ExecControlPath(machine, execID, "stdin")
	if eof {
		path += "?eof=1"
	}
	resp, err := c.stream(ctx, ep, http.MethodPost, path, bytes.NewReader(data), ContentTypeOctet)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Signal delivers a POSIX signal to a running exec.
func (c *Client) Signal(ctx context.Context, ep core.Endpoint, machine, execID string, signal int) error {
	return c.REST.JSON(ctx, http.MethodPost, ep, ExecControlPath(machine, execID, "signal"), SignalRequest{Signal: signal}, nil)
}

// PutFile writes one guest file at machine level (mode 0 = 0644).
func (c *Client) PutFile(ctx context.Context, ep core.Endpoint, machine, guestPath string, content []byte, mode uint32) error {
	return c.putFile(ctx, ep, MachineFilesPath(machine, guestPath), content, mode)
}

// PutSpaceFile writes a file owned by the space user. filePath is a guest
// path, or a host path when FilesPath was built with SpaceHostFilesPath.
func (c *Client) PutSpaceFile(ctx context.Context, ep core.Endpoint, filesPath string, content []byte, mode uint32) error {
	return c.putFile(ctx, ep, filesPath, content, mode)
}

func (c *Client) putFile(ctx context.Context, ep core.Endpoint, path string, content []byte, mode uint32) error {
	if mode != 0 {
		path += fmt.Sprintf("&mode=%o", mode)
	}
	resp, err := c.stream(ctx, ep, http.MethodPut, path, bytes.NewReader(content), ContentTypeOctet)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) GetFile(ctx context.Context, ep core.Endpoint, machine, guestPath string) ([]byte, error) {
	return c.GetFileAt(ctx, ep, MachineFilesPath(machine, guestPath))
}

// GetFileAt reads a file through any files path (machine or space).
func (c *Client) GetFileAt(ctx context.Context, ep core.Endpoint, filesPath string) ([]byte, error) {
	return c.REST.DoRaw(ctx, http.MethodGet, ep, filesPath, nil, false)
}

// PutArchive extracts a tar stream into a guest directory (created if
// missing) through any archive path (machine or space).
func (c *Client) PutArchive(ctx context.Context, ep core.Endpoint, archivePath string, tar io.Reader) error {
	resp, err := c.stream(ctx, ep, http.MethodPut, archivePath, tar, ContentTypeTar)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// GetArchive streams a guest directory as tar; the caller closes the reader.
func (c *Client) GetArchive(ctx context.Context, ep core.Endpoint, archivePath string) (io.ReadCloser, error) {
	resp, err := c.stream(ctx, ep, http.MethodGet, archivePath, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) CreateSnapshot(ctx context.Context, ep core.Endpoint, machine string, req CreateSnapshotRequest) (Snapshot, error) {
	var out Snapshot
	err := c.REST.JSON(ctx, http.MethodPost, ep, MachineSnapshotsPath(machine), req, &out)
	return out, err
}

func (c *Client) ListSnapshots(ctx context.Context, ep core.Endpoint) ([]Snapshot, error) {
	var out ListSnapshotsResponse
	err := c.REST.JSON(ctx, http.MethodGet, ep, PathSnapshots, nil, &out)
	return out.Snapshots, err
}

func (c *Client) DeleteSnapshot(ctx context.Context, ep core.Endpoint, id string) error {
	return c.REST.JSON(ctx, http.MethodDelete, ep, SnapshotPath(id), nil, nil)
}

func (c *Client) RestoreSnapshot(ctx context.Context, ep core.Endpoint, id string, req RestoreRequest) (Machine, error) {
	var out Machine
	err := c.REST.JSON(ctx, http.MethodPost, ep, SnapshotRestorePath(id), req, &out)
	return out, err
}

func (c *Client) Fork(ctx context.Context, ep core.Endpoint, machine string, req ForkRequest) (Machine, error) {
	var out Machine
	err := c.REST.JSON(ctx, http.MethodPost, ep, MachineForkPath(machine), req, &out)
	return out, err
}

func (c *Client) CreatePool(ctx context.Context, ep core.Endpoint, req CreatePoolRequest) (Pool, error) {
	var out Pool
	err := c.REST.JSON(ctx, http.MethodPost, ep, PathPools, req, &out)
	return out, err
}

func (c *Client) GetPool(ctx context.Context, ep core.Endpoint, name string) (Pool, error) {
	var out Pool
	err := c.REST.JSON(ctx, http.MethodGet, ep, PoolPath(name), nil, &out)
	return out, err
}

func (c *Client) DeletePool(ctx context.Context, ep core.Endpoint, name string) error {
	return c.REST.JSON(ctx, http.MethodDelete, ep, PoolPath(name), nil, nil)
}

func (c *Client) Claim(ctx context.Context, ep core.Endpoint, pool string, req ClaimRequest) (Machine, error) {
	var out Machine
	err := c.REST.JSON(ctx, http.MethodPost, ep, PoolClaimPath(pool), req, &out)
	return out, err
}

// stream issues a request whose body (request and/or response) is not a
// buffered JSON document, through the shared REST request and error mapping.
// No client-side deadline is applied beyond ctx: an exec or archive may
// legitimately outlive any fixed bound.
func (c *Client) stream(ctx context.Context, ep core.Endpoint, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := c.REST.Request(ctx, method, ep, path, body, contentType)
	if err != nil {
		return nil, err
	}
	return c.REST.Send(req)
}

// ErrorCodeOf returns the protocol error code of err ("" when err is not a
// protocol error).
func ErrorCodeOf(err error) ErrorCode {
	var pe *ProtocolError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}
