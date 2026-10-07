package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
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

// copyToCall records a CopyToContainer invocation.
type copyToCall struct {
	containerID string
	dst         string
	data        []byte
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
	copyToErr      error
	execInspectErr error
	statsBody      string

	createErr   error
	startErr    error
	removeErr   error
	listErr     error
	inspectErr  error
	statsErr    error
	copyFromErr error
	copyOutErr  error

	createdHostConfigs []*container.HostConfig
	createdConfigs     []*container.Config
	starts             []string
	removes            []string
	removedOpts        []container.RemoveOptions
	execCreates        []container.ExecOptions
	execAttaches       []string
	copyTos            []copyToCall
	readFileBytes      map[string][]byte
	copyOutTar         []byte
	inspects           []string
	statsCalls         []string
}

// newFakeClient returns an initialized fake client.
func newFakeClient() *fakeClient {
	return &fakeClient{
		containers:    make(map[string]*fakeContainer),
		readFileBytes: make(map[string][]byte),
	}
}

func (f *fakeClient) ContainerCreate(_ context.Context, config *container.Config, hostConfig *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return container.CreateResponse{}, f.createErr
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

func (f *fakeClient) ContainerExecCreate(_ context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execErr != nil {
		return container.ExecCreateResponse{}, f.execErr
	}
	f.execCreates = append(f.execCreates, options)
	return container.ExecCreateResponse{ID: "exec-" + containerID}, nil
}

func (f *fakeClient) ContainerExecAttach(_ context.Context, execID string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execAttachErr != nil {
		return types.HijackedResponse{}, f.execAttachErr
	}
	f.execAttaches = append(f.execAttaches, execID)
	if f.execReaderErr != nil {
		return types.HijackedResponse{Conn: &fakeConn{}, Reader: bufio.NewReader(errReader{f.execReaderErr})}, nil
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
	return types.HijackedResponse{Conn: &fakeConn{}, Reader: bufio.NewReader(&buf)}, nil
}

func (f *fakeClient) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execInspectErr != nil {
		return container.ExecInspect{}, f.execInspectErr
	}
	if len(f.execResults) > 0 {
		r := f.execResults[0]
		return container.ExecInspect{ExitCode: r.exitCode}, r.err
	}
	return container.ExecInspect{ExitCode: 0}, nil
}

func (f *fakeClient) CopyFromContainer(_ context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.copyFromErr != nil {
		return nil, container.PathStat{}, f.copyFromErr
	}
	c, ok := f.containers[containerID]
	if !ok {
		return nil, container.PathStat{}, errors.New("no such container")
	}
	if f.copyOutErr != nil {
		return nil, container.PathStat{}, f.copyOutErr
	}
	if data, ok := f.readFileBytes[srcPath]; ok {
		rc, err := singleFileTar("file", data, 0o644)
		return io.NopCloser(rc), container.PathStat{Name: "file", Size: int64(len(data))}, err
	}
	if f.copyOutTar != nil {
		return io.NopCloser(bytes.NewReader(f.copyOutTar)), container.PathStat{Name: "file"}, nil
	}
	_ = c
	return nil, container.PathStat{}, errors.New("no such path")
}

func (f *fakeClient) CopyToContainer(_ context.Context, containerID, dstPath string, content io.Reader, _ container.CopyToContainerOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.copyToErr != nil {
		return f.copyToErr
	}
	if _, ok := f.containers[containerID]; !ok {
		return errors.New("no such container")
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.copyTos = append(f.copyTos, copyToCall{containerID: containerID, dst: dstPath, data: data})
	return nil
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

// mustTarForTest builds a single-file tar for CopyOut test fixtures.
func mustTarForTest(t *testing.T, name, content string) []byte {
	t.Helper()
	rc, err := singleFileTar(name, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
