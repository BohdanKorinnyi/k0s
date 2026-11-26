// Package dockertest provides Fake, an in-memory stand-in for a Docker daemon.
//
// It implements dockerapi.Client. Containers and images are plain values the
// test sets up; calls that change state are recorded in order so a test can
// assert exactly what was done to which container.
package dockertest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Fake is safe for use from several goroutines. Set the exported fields before
// handing it to the code under test.
type Fake struct {
	// Before runs before a call that targets a container. Returning an error
	// fails the call; the container is not changed.
	Before func(method string, c *container.InspectResponse) error
	// After runs after such a call succeeded and may change the container, for
	// example to make a just-started container exit.
	After func(method string, c *container.InspectResponse)
	// OnInspect runs each time a container is inspected, before the copy is
	// taken. Use it to move a container through states over successive polls.
	OnInspect func(c *container.InspectResponse)
	// Version is what ServerVersion reports.
	Version string
	// ExecExitCode is what ExecInspect reports.
	ExecExitCode int
	// ExecHandler serves the far end of an attached exec. The default echoes.
	ExecHandler func(conn net.Conn)
	// Now supplies timestamps for start and stop.
	Now func() time.Time

	mu         sync.Mutex
	calls      []string
	counts     map[string]int
	containers []*container.InspectResponse
	images     map[string]image.InspectResponse
	remote     map[string]string
	remoteErr  map[string]error
	pulls      map[string]image.InspectResponse
	pullErr    map[string]string
	logs       map[string][]byte
	fails      map[string]error
	auth       map[string]string
	execs      map[string]string // exec ID → container ID
	lastExec   client.ExecCreateOptions
	lastLogs   client.ContainerLogsOptions
	serial     int
}

// New returns an empty fake reporting Docker version 27.0.
func New() *Fake {
	return &Fake{
		Version:   "27.0",
		Now:       time.Now,
		counts:    map[string]int{},
		images:    map[string]image.InspectResponse{},
		remote:    map[string]string{},
		remoteErr: map[string]error{},
		pulls:     map[string]image.InspectResponse{},
		pullErr:   map[string]string{},
		logs:      map[string][]byte{},
		fails:     map[string]error{},
		auth:      map[string]string{},
		execs:     map[string]string{},
	}
}

// Running returns a running container with the given name, created from
// imageRef. Tests adjust the returned value before AddContainer.
func Running(name, imageRef string) container.InspectResponse {
	return container.InspectResponse{
		Name:  "/" + name,
		Image: "sha256:" + hashHex("image:"+imageRef),
		State: &container.State{
			Status:    container.StateRunning,
			Running:   true,
			StartedAt: "2026-11-20T10:00:00Z",
		},
		Config:          &container.Config{Image: imageRef, Labels: map[string]string{}},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{}},
	}
}

// Stopped returns a container that exited with exitCode.
func Stopped(name, imageRef string, exitCode int) container.InspectResponse {
	c := Running(name, imageRef)
	c.State = &container.State{
		Status:     container.StateExited,
		ExitCode:   exitCode,
		StartedAt:  "2026-11-20T10:00:00Z",
		FinishedAt: "2026-11-21T10:00:00Z",
	}
	return c
}

// ComposeLabels returns the labels Docker Compose puts on a service's container.
func ComposeLabels(project, service string, number int) map[string]string {
	return map[string]string{
		"com.docker.compose.project":          project,
		"com.docker.compose.service":          service,
		"com.docker.compose.container-number": fmt.Sprint(number),
		"com.docker.compose.oneoff":           "False",
	}
}

// AddContainer stores c, filling in an ID and empty sections when missing, and
// returns its ID.
func (f *Fake) AddContainer(c container.InspectResponse) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(c.Name, "/") {
		c.Name = "/" + c.Name
	}
	if c.ID == "" {
		c.ID = hashHex("container:" + c.Name)
	}
	fillDefaults(&c)
	stored := clone(c)
	f.containers = append(f.containers, &stored)
	return c.ID
}

// Container returns a snapshot of the container with the given name or ID.
func (f *Fake) Container(nameOrID string) (container.InspectResponse, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(nameOrID)
	if c == nil {
		return container.InspectResponse{}, false
	}
	return clone(*c), true
}

// AddImage makes img the image ref points to. It is also reachable by its ID.
func (f *Fake) AddImage(ref string, img image.InspectResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = img
	if img.ID != "" {
		f.images[img.ID] = img
	}
}

// SetRemoteDigest sets the digest the registry serves for ref.
func (f *Fake) SetRemoteDigest(ref, dgst string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remote[ref] = dgst
	delete(f.remoteErr, ref)
}

// SetRemoteError makes the registry lookup for ref fail with err.
func (f *Fake) SetRemoteError(ref string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remoteErr[ref] = err
}

// SetPullResult makes a pull of ref bring img to the host.
func (f *Fake) SetPullResult(ref string, img image.InspectResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[ref] = img
}

// SetPullError makes a pull of ref report message as an error in its stream,
// the way Docker reports a pull that fails after it started.
func (f *Fake) SetPullError(ref, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullErr[ref] = message
}

// SetLogs sets the raw bytes ContainerLogs returns for a container.
func (f *Fake) SetLogs(nameOrID string, raw []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(nameOrID); c != nil {
		f.logs[c.ID] = raw
	}
}

// Fail makes every call of method return err.
func (f *Fake) Fail(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails[method] = err
}

// FailFor makes method return err when it targets the named container.
func (f *Fake) FailFor(method, containerName string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails[method+" "+containerName] = err
}

// CallLog returns the state-changing calls made so far, in order.
func (f *Fake) CallLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// ResetCalls forgets the calls recorded so far.
func (f *Fake) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// Count returns how many times method was called, including read-only ones.
func (f *Fake) Count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[method]
}

// LastAuth returns the registry login last sent with a pull or registry lookup
// of ref.
func (f *Fake) LastAuth(ref string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth[ref]
}

// LastExec returns the options of the most recent ExecCreate.
func (f *Fake) LastExec() client.ExecCreateOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastExec
}

// LastLogs returns the options of the most recent ContainerLogs.
func (f *Fake) LastLogs() client.ContainerLogsOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastLogs
}

// ---- dockerapi.Client ----

func (f *Fake) Ping(context.Context, client.PingOptions) (client.PingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["Ping"]++
	return client.PingResult{APIVersion: "1.47"}, f.fails["Ping"]
}

func (f *Fake) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ServerVersion"]++
	if err := f.fails["ServerVersion"]; err != nil {
		return client.ServerVersionResult{}, err
	}
	return client.ServerVersionResult{Version: f.Version, APIVersion: "1.47"}, nil
}

func (f *Fake) ContainerList(_ context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ContainerList"]++
	if err := f.fails["ContainerList"]; err != nil {
		return client.ContainerListResult{}, err
	}
	var out client.ContainerListResult
	for _, c := range f.containers {
		if !options.All && !c.State.Running {
			continue
		}
		item := container.Summary{
			ID:      c.ID,
			Names:   []string{c.Name},
			Image:   c.Config.Image,
			ImageID: c.Image,
			Labels:  c.Config.Labels,
			State:   c.State.Status,
		}
		item.HostConfig.NetworkMode = string(c.HostConfig.NetworkMode)
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (f *Fake) ContainerInspect(_ context.Context, containerID string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ContainerInspect"]++
	c := f.find(containerID)
	if c == nil {
		return client.ContainerInspectResult{}, noSuchContainer(containerID)
	}
	if err := f.failure("ContainerInspect", c); err != nil {
		return client.ContainerInspectResult{}, err
	}
	if f.OnInspect != nil {
		f.OnInspect(c)
	}
	return client.ContainerInspectResult{Container: clone(*c)}, nil
}

func (f *Fake) ContainerStart(_ context.Context, containerID string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, f.change("ContainerStart", containerID, func(c *container.InspectResponse) error {
		if !c.State.Running {
			f.setRunning(c)
		}
		return nil
	})
}

func (f *Fake) ContainerStop(_ context.Context, containerID string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return client.ContainerStopResult{}, f.change("ContainerStop", containerID, func(c *container.InspectResponse) error {
		if c.State.Running {
			c.State.Running = false
			c.State.Restarting = false
			c.State.Status = container.StateExited
			c.State.FinishedAt = f.Now().UTC().Format(time.RFC3339Nano)
		}
		return nil
	})
}

func (f *Fake) ContainerRestart(_ context.Context, containerID string, _ client.ContainerRestartOptions) (client.ContainerRestartResult, error) {
	return client.ContainerRestartResult{}, f.change("ContainerRestart", containerID, func(c *container.InspectResponse) error {
		f.setRunning(c)
		return nil
	})
}

func (f *Fake) ContainerRemove(_ context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, f.change("ContainerRemove", containerID, func(c *container.InspectResponse) error {
		if c.State.Running && !options.Force {
			return fmt.Errorf("cannot remove container %q: container is running: %w", c.Name, cerrdefs.ErrConflict)
		}
		for i, other := range f.containers {
			if other == c {
				f.containers = append(f.containers[:i], f.containers[i+1:]...)
				break
			}
		}
		return nil
	})
}

func (f *Fake) ContainerRename(_ context.Context, containerID string, options client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ContainerRename"]++
	c := f.find(containerID)
	if c == nil {
		return client.ContainerRenameResult{}, noSuchContainer(containerID)
	}
	newName := strings.TrimPrefix(options.NewName, "/")
	f.calls = append(f.calls, "ContainerRename "+bare(c.Name)+"->"+newName)
	if err := f.failure("ContainerRename", c); err != nil {
		return client.ContainerRenameResult{}, err
	}
	if f.Before != nil {
		if err := f.Before("ContainerRename", c); err != nil {
			return client.ContainerRenameResult{}, err
		}
	}
	if other := f.findByName(newName); other != nil && other != c {
		return client.ContainerRenameResult{}, nameInUse(newName)
	}
	c.Name = "/" + newName
	if f.After != nil {
		f.After("ContainerRename", c)
	}
	return client.ContainerRenameResult{}, nil
}

func (f *Fake) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ContainerCreate"]++
	name := strings.TrimPrefix(options.Name, "/")
	f.calls = append(f.calls, "ContainerCreate "+name)
	if err := f.fails["ContainerCreate"]; err != nil {
		return client.ContainerCreateResult{}, err
	}
	if err := f.fails["ContainerCreate "+name]; err != nil {
		return client.ContainerCreateResult{}, err
	}
	if f.findByName(name) != nil {
		return client.ContainerCreateResult{}, nameInUse(name)
	}

	cfg := &container.Config{}
	if options.Config != nil {
		cfg = clonePtr(options.Config)
	}
	if options.Image != "" {
		cfg.Image = options.Image
	}
	img, ok := f.images[cfg.Image]
	if !ok {
		return client.ContainerCreateResult{}, fmt.Errorf("No such image: %s: %w", cfg.Image, cerrdefs.ErrNotFound)
	}

	f.serial++
	c := container.InspectResponse{
		ID:              hashHex(fmt.Sprintf("created:%d:%s", f.serial, name)),
		Name:            "/" + name,
		Image:           img.ID,
		Created:         f.Now().UTC().Format(time.RFC3339Nano),
		State:           &container.State{Status: container.StateCreated},
		Config:          cfg,
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{}},
	}
	if options.HostConfig != nil {
		c.HostConfig = clonePtr(options.HostConfig)
	}
	if options.NetworkingConfig != nil {
		for netName, ep := range options.NetworkingConfig.EndpointsConfig {
			c.NetworkSettings.Networks[netName] = clonePtr(ep)
		}
	}
	fillDefaults(&c)
	f.containers = append(f.containers, &c)
	return client.ContainerCreateResult{ID: c.ID}, nil
}

func (f *Fake) ContainerLogs(_ context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ContainerLogs"]++
	f.lastLogs = options
	c := f.find(containerID)
	if c == nil {
		return nil, noSuchContainer(containerID)
	}
	if err := f.failure("ContainerLogs", c); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(f.logs[c.ID])), nil
}

func (f *Fake) ImagePull(_ context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ImagePull"]++
	f.calls = append(f.calls, "ImagePull "+ref)
	f.auth[ref] = options.RegistryAuth
	if err := f.fails["ImagePull"]; err != nil {
		return nil, err
	}
	if msg, ok := f.pullErr[ref]; ok {
		return newPullResponse(
			jsonstream.Message{Status: "Pulling from " + ref},
			jsonstream.Message{Error: &jsonstream.Error{Message: msg}},
		), nil
	}
	status := "Status: Image is up to date for " + ref
	if img, ok := f.pulls[ref]; ok {
		if current, had := f.images[ref]; !had || current.ID != img.ID {
			status = "Status: Downloaded newer image for " + ref
		}
		f.images[ref] = img
		if img.ID != "" {
			f.images[img.ID] = img
		}
	}
	return newPullResponse(
		jsonstream.Message{Status: "Pulling from " + ref},
		jsonstream.Message{Status: status},
	), nil
}

func (f *Fake) ImageInspect(_ context.Context, imageID string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ImageInspect"]++
	if err := f.fails["ImageInspect"]; err != nil {
		return client.ImageInspectResult{}, err
	}
	img, ok := f.images[imageID]
	if !ok {
		return client.ImageInspectResult{}, fmt.Errorf("No such image: %s: %w", imageID, cerrdefs.ErrNotFound)
	}
	return client.ImageInspectResult{InspectResponse: img}, nil
}

func (f *Fake) DistributionInspect(_ context.Context, ref string, options client.DistributionInspectOptions) (client.DistributionInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["DistributionInspect"]++
	f.auth[ref] = options.EncodedRegistryAuth
	if err := f.fails["DistributionInspect"]; err != nil {
		return client.DistributionInspectResult{}, err
	}
	if err := f.remoteErr[ref]; err != nil {
		return client.DistributionInspectResult{}, err
	}
	dgst, ok := f.remote[ref]
	if !ok {
		return client.DistributionInspectResult{}, fmt.Errorf("manifest for %s not found: %w", ref, cerrdefs.ErrNotFound)
	}
	return client.DistributionInspectResult{DistributionInspect: registry.DistributionInspect{
		Descriptor: ocispec.Descriptor{Digest: digest.Digest(dgst)},
	}}, nil
}

func (f *Fake) ExecCreate(_ context.Context, containerID string, options client.ExecCreateOptions) (client.ExecCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ExecCreate"]++
	c := f.find(containerID)
	if c == nil {
		return client.ExecCreateResult{}, noSuchContainer(containerID)
	}
	f.calls = append(f.calls, "ExecCreate "+bare(c.Name))
	if err := f.failure("ExecCreate", c); err != nil {
		return client.ExecCreateResult{}, err
	}
	if !c.State.Running {
		return client.ExecCreateResult{}, fmt.Errorf("container %s is not running: %w", c.ID, cerrdefs.ErrConflict)
	}
	f.serial++
	id := hashHex(fmt.Sprintf("exec:%d", f.serial))
	f.execs[id] = c.ID
	f.lastExec = options
	return client.ExecCreateResult{ID: id}, nil
}

func (f *Fake) ExecAttach(_ context.Context, execID string, _ client.ExecAttachOptions) (client.ExecAttachResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ExecAttach"]++
	if _, ok := f.execs[execID]; !ok {
		return client.ExecAttachResult{}, fmt.Errorf("No such exec instance: %s: %w", execID, cerrdefs.ErrNotFound)
	}
	if err := f.fails["ExecAttach"]; err != nil {
		return client.ExecAttachResult{}, err
	}
	near, far := net.Pipe()
	handler := f.ExecHandler
	if handler == nil {
		handler = func(conn net.Conn) {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}
	go handler(far)
	return client.ExecAttachResult{HijackedResponse: client.NewHijackedResponse(near, "application/vnd.docker.raw-stream")}, nil
}

func (f *Fake) ExecResize(_ context.Context, execID string, options client.ExecResizeOptions) (client.ExecResizeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ExecResize"]++
	if _, ok := f.execs[execID]; !ok {
		return client.ExecResizeResult{}, fmt.Errorf("No such exec instance: %s: %w", execID, cerrdefs.ErrNotFound)
	}
	f.calls = append(f.calls, fmt.Sprintf("ExecResize %dx%d", options.Width, options.Height))
	return client.ExecResizeResult{}, f.fails["ExecResize"]
}

func (f *Fake) ExecInspect(_ context.Context, execID string, _ client.ExecInspectOptions) (client.ExecInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["ExecInspect"]++
	containerID, ok := f.execs[execID]
	if !ok {
		return client.ExecInspectResult{}, fmt.Errorf("No such exec instance: %s: %w", execID, cerrdefs.ErrNotFound)
	}
	return client.ExecInspectResult{ID: execID, ContainerID: containerID, ExitCode: f.ExecExitCode}, f.fails["ExecInspect"]
}

func (f *Fake) NetworkConnect(_ context.Context, networkID string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts["NetworkConnect"]++
	c := f.find(options.Container)
	if c == nil {
		return client.NetworkConnectResult{}, noSuchContainer(options.Container)
	}
	f.calls = append(f.calls, "NetworkConnect "+networkID+" "+bare(c.Name))
	if err := f.failure("NetworkConnect", c); err != nil {
		return client.NetworkConnectResult{}, err
	}
	ep := &network.EndpointSettings{}
	if options.EndpointConfig != nil {
		ep = clonePtr(options.EndpointConfig)
	}
	c.NetworkSettings.Networks[networkID] = ep
	return client.NetworkConnectResult{}, nil
}

func (f *Fake) Close() error { return nil }

// ---- internals ----

// change runs one state-changing call against a container: record it, apply
// injected failures and the Before hook, mutate, then run the After hook.
func (f *Fake) change(method, containerID string, apply func(*container.InspectResponse) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[method]++
	c := f.find(containerID)
	if c == nil {
		return noSuchContainer(containerID)
	}
	f.calls = append(f.calls, method+" "+bare(c.Name))
	if err := f.failure(method, c); err != nil {
		return err
	}
	if f.Before != nil {
		if err := f.Before(method, c); err != nil {
			return err
		}
	}
	if err := apply(c); err != nil {
		return err
	}
	if f.After != nil {
		f.After(method, c)
	}
	return nil
}

func (f *Fake) failure(method string, c *container.InspectResponse) error {
	if err := f.fails[method]; err != nil {
		return err
	}
	return f.fails[method+" "+bare(c.Name)]
}

func (f *Fake) setRunning(c *container.InspectResponse) {
	c.State.Running = true
	c.State.Restarting = false
	c.State.Status = container.StateRunning
	c.State.ExitCode = 0
	c.State.StartedAt = f.Now().UTC().Format(time.RFC3339Nano)
}

// find resolves a name (with or without the leading slash), a full ID or an ID
// prefix, as Docker does.
func (f *Fake) find(ref string) *container.InspectResponse {
	if ref == "" {
		return nil
	}
	if c := f.findByName(ref); c != nil {
		return c
	}
	for _, c := range f.containers {
		if c.ID == ref {
			return c
		}
	}
	for _, c := range f.containers {
		if strings.HasPrefix(c.ID, ref) {
			return c
		}
	}
	return nil
}

func (f *Fake) findByName(name string) *container.InspectResponse {
	name = "/" + strings.TrimPrefix(name, "/")
	for _, c := range f.containers {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func fillDefaults(c *container.InspectResponse) {
	if c.State == nil {
		c.State = &container.State{Status: container.StateCreated}
	}
	if c.Config == nil {
		c.Config = &container.Config{}
	}
	if c.Config.Labels == nil {
		c.Config.Labels = map[string]string{}
	}
	if c.HostConfig == nil {
		c.HostConfig = &container.HostConfig{}
	}
	if c.NetworkSettings == nil {
		c.NetworkSettings = &container.NetworkSettings{}
	}
	if c.NetworkSettings.Networks == nil {
		c.NetworkSettings.Networks = map[string]*network.EndpointSettings{}
	}
}

// clone deep-copies through JSON, the same form the value has on the wire.
func clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("dockertest: cannot copy %T: %v", v, err))
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		panic(fmt.Sprintf("dockertest: cannot copy %T: %v", v, err))
	}
	return out
}

func clonePtr[T any](v *T) *T {
	out := clone(*v)
	return &out
}

func bare(name string) string { return strings.TrimPrefix(name, "/") }

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func noSuchContainer(ref string) error {
	return fmt.Errorf("No such container: %s: %w", ref, cerrdefs.ErrNotFound)
}

func nameInUse(name string) error {
	return fmt.Errorf("the container name %q is already in use: %w", "/"+name, cerrdefs.ErrConflict)
}

// pullResponse implements client.ImagePullResponse over a fixed message list.
type pullResponse struct {
	messages []jsonstream.Message
	raw      *bytes.Reader
}

func newPullResponse(messages ...jsonstream.Message) *pullResponse {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range messages {
		_ = enc.Encode(m)
	}
	return &pullResponse{messages: messages, raw: bytes.NewReader(buf.Bytes())}
}

func (p *pullResponse) Read(b []byte) (int, error) { return p.raw.Read(b) }
func (p *pullResponse) Close() error               { return nil }

func (p *pullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {
		for _, m := range p.messages {
			if !yield(m, nil) {
				return
			}
		}
	}
}

func (p *pullResponse) Wait(ctx context.Context) error {
	for m, err := range p.JSONMessages(ctx) {
		if err != nil {
			return err
		}
		if m.Error != nil {
			return m.Error
		}
	}
	return nil
}
