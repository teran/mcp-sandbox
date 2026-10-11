package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// fakeConn is a no-op net.Conn used to satisfy HijackedResponse.Close().
type fakeConn struct{}

// errReader is a reader that always returns the given error, used to make
// stdcopy.StdCopy fail during an Exec.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func (fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (fakeConn) RemoteAddr() net.Addr             { return fakeAddr{} }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

// recordingConn wraps fakeConn and records every byte written to it, so tests
// can assert the bytes streamed to a docker exec's stdin (used for secret
// delivery, §7.3).
type recordingConn struct {
	fakeConn

	dst *[]byte
}

func (c *recordingConn) Write(b []byte) (int, error) {
	*c.dst = append(*c.dst, b...)
	return len(b), nil
}

type fakeAddr struct{}

func (fakeAddr) Network() string { return "test" }
func (fakeAddr) String() string  { return "test" }

// fakeContainer models a created container.
type fakeContainer struct {
	id         string
	labels     map[string]string
	config     *container.Config
	hostConfig *container.HostConfig
	files      map[string][]byte
}

// execResult is a canned exec outcome the fake returns.
type execResult struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

// fakeClient implements dockerAPI with in-memory behavior, recording calls so
// tests can assert on them. It never talks to a real daemon.
type fakeClient struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	nextID     int

	execResults []execResult
	execErr     error

	execAttachErr  error
	execReaderErr  error
	execInspectErr error
	execExitCode   int
	statsBody      string

	createErr  error
	startErr   error
	removeErr  error
	listErr    error
	inspectErr error
	statsErr   error

	// Image auto-pull simulation (SPEC §7.9). When requirePull is true,
	// ContainerCreate fails with a "No such image" error until the image has
	// been pulled — modelling a daemon with an empty local image cache.
	requirePull bool
	// pullErr makes ImagePull fail, surfacing a pull error to the backend.
	pullErr error
	// pullBodyErr makes the pull stream fail on read, exercising the drain path.
	pullBodyErr error
	// pulledImages records every ref passed to ImagePull in order.
	pulledImages []string
	// pullOptions records every PullOptions passed to ImagePull in order, so
	// tests can assert the RegistryAuth attached for private registries.
	pullOptions []image.PullOptions

	createdHostConfigs []*container.HostConfig
	createdConfigs     []*container.Config
	starts             []string
	removes            []string
	removedOpts        []container.RemoveOptions
	execCreates        []container.ExecOptions
	execAttaches       []string
	execStdin          [][]byte
	inspects           []string
	statsCalls         []string

	// execCtxDeadlines records the deadline of the context seen by each exec
	// API call (create/attach/inspect) in order. A zero time.Time means the
	// context carried no deadline. Lets tests assert that req.Timeout is
	// applied as a context.WithTimeout deadline on the ctx passed to Docker.
	execCtxDeadlines []time.Time
}

// newFakeClient returns an initialized fake client.
func newFakeClient() *fakeClient {
	return &fakeClient{
		containers: make(map[string]*fakeContainer),
	}
}

func (f *fakeClient) ContainerCreate(_ context.Context, config *container.Config, hostConfig *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return container.CreateResponse{}, f.createErr
	}
	if f.requirePull && !f.wasPulledLocked(config.Image) {
		// Mirror the docker daemon: a create for a not-yet-local image fails
		// until it has been pulled.
		return container.CreateResponse{}, fmt.Errorf("Error response from daemon: No such image: %s", config.Image)
	}
	f.nextID++
	id := "cid-" + strconv.Itoa(f.nextID)
	labels := config.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	f.containers[id] = &fakeContainer{
		id:         id,
		labels:     labels,
		config:     config,
		hostConfig: hostConfig,
		files:      map[string][]byte{},
	}
	f.createdConfigs = append(f.createdConfigs, config)
	f.createdHostConfigs = append(f.createdHostConfigs, hostConfig)
	return container.CreateResponse{ID: id}, nil
}

// ImagePull records the pull of a trusted image reference and returns a dummy
// stream that the backend drains and discards (image content is never logged).
func (f *fakeClient) ImagePull(_ context.Context, refStr string, opts image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	f.pulledImages = append(f.pulledImages, refStr)
	f.pullOptions = append(f.pullOptions, opts)
	if f.pullBodyErr != nil {
		return io.NopCloser(errReader{f.pullBodyErr}), nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// wasPulledLocked reports whether the given image ref has been pulled. Callers
// must hold f.mu.
func (f *fakeClient) wasPulledLocked(ref string) bool {
	return slices.Contains(f.pulledImages, ref)
}

// pulledRefs returns a copy of the pulled image references for assertions.
func (f *fakeClient) pulledRefs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.pulledImages))
	copy(out, f.pulledImages)
	return out
}

// pullOpts returns a copy of the recorded PullOptions for assertions.
func (f *fakeClient) pullOpts() []image.PullOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]image.PullOptions, len(f.pullOptions))
	copy(out, f.pullOptions)
	return out
}

func (f *fakeClient) ContainerStart(_ context.Context, containerID string, _ container.StartOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.starts = append(f.starts, containerID)
	if _, ok := f.containers[containerID]; !ok {
		return errors.New("container not found")
	}
	return nil
}

func (f *fakeClient) ContainerList(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []container.Summary
	for _, c := range f.containers {
		if matchesFilter(c.labels, options.Filters) {
			out = append(out, container.Summary{ID: c.id, Labels: c.labels})
		}
	}
	return out, nil
}

func matchesFilter(labels map[string]string, args filters.Args) bool {
	// Only the "label" filter is used by the backend. Each value is a
	// "key=value" string.
	for _, spec := range args.Get("label") {
		k, v, _ := strings.Cut(spec, "=")
		if val, ok := labels[k]; !ok || val != v {
			return false
		}
	}
	return true
}

func (f *fakeClient) ContainerInspect(_ context.Context, containerID string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects = append(f.inspects, containerID)
	if f.inspectErr != nil {
		return container.InspectResponse{}, f.inspectErr
	}
	c, ok := f.containers[containerID]
	if !ok {
		return container.InspectResponse{}, errors.New("no such container")
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: containerID, State: &container.State{Running: true}},
		Config:            c.config,
	}, nil
}

func (f *fakeClient) ContainerStats(_ context.Context, containerID string, _ bool) (container.StatsResponseReader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statsCalls = append(f.statsCalls, containerID)
	if f.statsErr != nil {
		return container.StatsResponseReader{}, f.statsErr
	}
	if _, ok := f.containers[containerID]; !ok {
		return container.StatsResponseReader{}, errors.New("no such container")
	}
	body := f.statsBody
	if body == "" {
		body = `{"name":"x","cpu_stats":{},"memory_stats":{}}`
	}
	return container.StatsResponseReader{Body: io.NopCloser(bytes.NewBufferString(body)), OSType: "linux"}, nil
}

func (f *fakeClient) ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execErr != nil {
		return container.ExecCreateResponse{}, f.execErr
	}
	f.execCreates = append(f.execCreates, options)
	f.execCtxDeadlines = append(f.execCtxDeadlines, execDeadlineFromContext(ctx))
	return container.ExecCreateResponse{ID: "exec-" + containerID}, nil
}

func (f *fakeClient) ContainerExecAttach(ctx context.Context, execID string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execAttachErr != nil {
		return types.HijackedResponse{}, f.execAttachErr
	}
	f.execAttaches = append(f.execAttaches, execID)
	f.execCtxDeadlines = append(f.execCtxDeadlines, execDeadlineFromContext(ctx))
	// Capture bytes written to the exec's stdin (secrets are delivered this
	// way) so tests can assert the value landed on stdin, not argv.
	f.execStdin = append(f.execStdin, nil)
	idx := len(f.execStdin) - 1
	if f.execReaderErr != nil {
		return types.HijackedResponse{Conn: &recordingConn{dst: &f.execStdin[idx]}, Reader: bufio.NewReader(errReader{f.execReaderErr})}, nil
	}
	var buf bytes.Buffer
	if len(f.execResults) > 0 {
		r := f.execResults[0]
		f.execResults = f.execResults[1:]
		if r.stdout != "" {
			w := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
			_, _ = w.Write([]byte(r.stdout))
		}
		if r.stderr != "" {
			w := stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
			_, _ = w.Write([]byte(r.stderr))
		}
	}
	return types.HijackedResponse{Conn: &recordingConn{dst: &f.execStdin[idx]}, Reader: bufio.NewReader(&buf)}, nil
}

func (f *fakeClient) ContainerExecInspect(ctx context.Context, _ string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execInspectErr != nil {
		return container.ExecInspect{}, f.execInspectErr
	}
	f.execCtxDeadlines = append(f.execCtxDeadlines, execDeadlineFromContext(ctx))
	if f.execExitCode != 0 {
		return container.ExecInspect{ExitCode: f.execExitCode}, nil
	}
	if len(f.execResults) > 0 {
		r := f.execResults[0]
		return container.ExecInspect{ExitCode: r.exitCode}, r.err
	}
	return container.ExecInspect{ExitCode: 0}, nil
}

func (f *fakeClient) ContainerRemove(_ context.Context, containerID string, options container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removes = append(f.removes, containerID)
	f.removedOpts = append(f.removedOpts, options)
	delete(f.containers, containerID)
	return nil
}

// assertHostConfigUnchanged validates the fake recorded the expected immutable
// isolation values on the most recent create.
func (f *fakeClient) lastHostConfig() *container.HostConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createdHostConfigs) == 0 {
		return nil
	}
	return f.createdHostConfigs[len(f.createdHostConfigs)-1]
}

func (f *fakeClient) lastConfig() *container.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createdConfigs) == 0 {
		return nil
	}
	return f.createdConfigs[len(f.createdConfigs)-1]
}

// execDeadlineFromContext returns the deadline carried by ctx, or the zero
// time.Time when ctx has no deadline.
func execDeadlineFromContext(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

// lastExecDeadline returns the deadline the most recent exec API call saw, or
// the zero time.Time when no exec call recorded a deadline (or there was none).
func (f *fakeClient) lastExecDeadline() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execCtxDeadlines) == 0 {
		return time.Time{}
	}
	return f.execCtxDeadlines[len(f.execCtxDeadlines)-1]
}
