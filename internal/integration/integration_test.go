//go:build integration

// Package integration runs the Docker-facing code against a real daemon.
//
//	make test-integration
//
// Everything it creates is named "dockpanel-it-…" and removed afterwards. It
// never touches anything else on the daemon.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"dockpanel/internal/actions"
	"dockpanel/internal/hosts"
	"dockpanel/internal/services"
	"dockpanel/internal/store"
	"dockpanel/internal/streams"
	"dockpanel/internal/updates"
)

const (
	baseImage = "alpine:3.19"
	prefix    = "dockpanel-it-"
	socketURL = "unix:///var/run/docker.sock"
)

var ctx = context.Background()

// daemon connects through the project's own Dial, as the panel does.
func daemon(t *testing.T) *client.Client {
	t.Helper()
	// Skip only when there is no daemon to talk to. Once the socket exists,
	// failing to talk to it is exactly what these tests are here to catch.
	if _, err := os.Stat(strings.TrimPrefix(socketURL, "unix://")); err != nil {
		t.Skipf("no Docker socket: %v", err)
	}
	cli, err := hosts.Dial(store.Host{Name: "local", URL: socketURL}, hosts.Options{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { cli.Close() })
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx, client.PingOptions{}); err != nil {
		t.Fatalf("the Docker socket is there but does not answer: %v", err)
	}
	// Registered before any container is created, so it runs after they are
	// removed: an image still used by a container cannot be deleted.
	t.Cleanup(func() {
		imagesMu.Lock()
		ids := testImages[t]
		delete(testImages, t)
		imagesMu.Unlock()
		for _, id := range ids {
			_, _ = cli.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: true})
		}
	})
	ensureImage(t, cli, baseImage)
	return cli
}

// testImages records the images each test made, for removal at its end.
var (
	imagesMu   sync.Mutex
	testImages = map[*testing.T][]string{}
)

func ensureImage(t *testing.T, cli *client.Client, ref string) {
	t.Helper()
	if _, err := cli.ImageInspect(ctx, ref); err == nil {
		return
	}
	resp, err := cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		t.Skipf("cannot pull %s: %v", ref, err)
	}
	defer resp.Close()
	if err := resp.Wait(ctx); err != nil {
		t.Skipf("cannot pull %s: %v", ref, err)
	}
}

func unique(kind string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + kind + "-" + hex.EncodeToString(b)
}

// removeContainer force-removes a container we created, with its anonymous
// volumes. Missing is fine.
func removeContainer(cli *client.Client, name string) {
	_, _ = cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}

// run creates and starts a container and schedules its removal.
func run(t *testing.T, cli *client.Client, name string, cfg *container.Config, hc *container.HostConfig) string {
	t.Helper()
	t.Cleanup(func() { removeContainer(cli, name) })
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: hc})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return created.ID
}

func inspect(t *testing.T, cli *client.Client, ref string) container.InspectResponse {
	t.Helper()
	res, err := cli.ContainerInspect(ctx, ref, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", ref, err)
	}
	return res.Container
}

func sleeper() *container.Config {
	return &container.Config{Image: baseImage, Cmd: []string{"sleep", "3600"}}
}

func TestRealList(t *testing.T) {
	cli := daemon(t)
	project := strings.TrimPrefix(unique("proj"), prefix)
	name := unique("web")
	cfg := sleeper()
	cfg.Labels = map[string]string{
		services.LabelProject: project,
		services.LabelService: "web",
		services.LabelNumber:  "1",
		services.LabelOneOff:  "False",
	}
	id := run(t, cli, name, cfg, nil)

	svc, err := services.Get(ctx, cli, "c~"+project+"~web")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if svc.Name != "web" || svc.Project != project || svc.Running != 1 || svc.Total != 1 || svc.State != services.StateRunning {
		t.Errorf("service = %+v", svc)
	}
	c := svc.Containers[0]
	if c.ID != id || c.Name != name || c.Image != baseImage || c.State != "running" || c.StartedAt.IsZero() || !strings.HasPrefix(c.ImageID, "sha256:") {
		t.Errorf("container = %+v", c)
	}
}

func TestRealRestartStopStart(t *testing.T) {
	cli := daemon(t)
	name := unique("life")
	// A fast stop: sleep ignores SIGTERM as PID 1, which would cost 10s.
	cfg := sleeper()
	cfg.StopSignal = "SIGKILL"
	run(t, cli, name, cfg, nil)
	env := actions.Env{Docker: cli}
	reg := actions.Default()
	do := func(id string) {
		t.Helper()
		svc, err := services.Get(ctx, cli, "s~"+name)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := reg.Get(id)
		if !a.Applies(svc) {
			t.Fatalf("%s does not apply to %+v", id, svc)
		}
		if err := a.Run(ctx, env, actions.Target{Service: svc}, func(actions.Event) {}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	before := inspect(t, cli, name).State.StartedAt
	do("restart")
	after := inspect(t, cli, name)
	if !after.State.Running || after.State.StartedAt == before {
		t.Errorf("after restart: running %v, started %s (was %s)", after.State.Running, after.State.StartedAt, before)
	}
	do("stop")
	if inspect(t, cli, name).State.Running {
		t.Error("still running after stop")
	}
	do("start")
	if !inspect(t, cli, name).State.Running {
		t.Error("not running after start")
	}
}

// commitImage makes an image with the given defaults and points ref at it.
// Committing a throwaway container is the lightest way to get two images
// that differ only in their configuration.
func commitImage(t *testing.T, cli *client.Client, ref string, changes ...string) string {
	t.Helper()
	scratch := unique("commit")
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: scratch, Config: &container.Config{Image: baseImage, Cmd: []string{"true"}}})
	if err != nil {
		t.Fatalf("create scratch container: %v", err)
	}
	defer removeContainer(cli, scratch)
	res, err := cli.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{Reference: ref, Changes: changes})
	if err != nil {
		t.Fatalf("commit %s: %v", ref, err)
	}
	imagesMu.Lock()
	testImages[t] = append(testImages[t], res.ID)
	imagesMu.Unlock()
	img, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		t.Fatalf("inspect %s: %v", ref, err)
	}
	return img.ID
}

func mountAt(c container.InspectResponse, destination string) (name string, found bool) {
	for _, m := range c.Mounts {
		if m.Destination == destination {
			return m.Name, true
		}
	}
	return "", false
}

func TestRealReplace(t *testing.T) {
	cli := daemon(t)
	ref := unique("img") + ":latest"
	name := unique("app")
	volume := unique("vol")
	t.Cleanup(func() { _, _ = cli.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: true}) })

	oldImage := commitImage(t, cli, ref, "ENV FLAVOUR=old", `CMD ["sleep","3600"]`, "LABEL built=old", "STOPSIGNAL SIGKILL")
	oldID := run(t, cli, name,
		&container.Config{
			Image:   ref,
			Env:     []string{"APP_ENV=prod"},
			Labels:  map[string]string{"dockpanel.it": "1"},
			Volumes: map[string]struct{}{"/anon": {}},
		},
		&container.HostConfig{
			Binds: []string{volume + ":/named"},
			// An anonymous volume declared through the mount API with no
			// source: the way Docker Compose declares them.
			Mounts:        []mount.Mount{{Type: mount.TypeVolume, Target: "/anon-mount"}},
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		})
	before := inspect(t, cli, name)
	anonBefore, ok := mountAt(before, "/anon")
	if !ok || anonBefore == "" {
		t.Fatalf("the test container has no anonymous volume at /anon: %+v", before.Mounts)
	}
	anonMountBefore, ok := mountAt(before, "/anon-mount")
	if !ok || anonMountBefore == "" {
		t.Fatalf("the test container has no anonymous volume at /anon-mount: %+v", before.Mounts)
	}
	// Put something in each volume: afterwards it must still be there.
	for _, dir := range []string{"/named", "/anon", "/anon-mount"} {
		execIn(t, cli, oldID, "sh", "-c", "echo kept > "+dir+"/marker")
	}
	archBefore := imageArch(t, cli, before.Image)

	// A newer image arrives for the same tag.
	newImage := commitImage(t, cli, ref, "ENV FLAVOUR=new", `CMD ["sleep","3600"]`, "LABEL built=new", "STOPSIGNAL SIGKILL")
	if newImage == oldImage {
		t.Fatal("the two test images are the same")
	}

	var steps []string
	replaced, err := actions.Replace(ctx, actions.Env{Docker: cli, StartGrace: time.Second, Poll: 100 * time.Millisecond}, oldID,
		func(e actions.Event) { steps = append(steps, e.Level+": "+e.Step) })
	if err != nil || !replaced {
		t.Fatalf("Replace = %v, %v\nsteps: %q", replaced, err, steps)
	}

	after := inspect(t, cli, name)
	if after.ID == oldID {
		t.Error("the container was not replaced")
	}
	if after.Image != newImage {
		t.Errorf("image = %s, want the new one %s", after.Image, newImage)
	}
	if !after.State.Running {
		t.Errorf("state = %+v, want running", after.State)
	}
	// What the user set is kept…
	if !slices.Contains(after.Config.Env, "APP_ENV=prod") {
		t.Errorf("env = %v, want APP_ENV=prod kept", after.Config.Env)
	}
	if after.Config.Labels["dockpanel.it"] != "1" {
		t.Errorf("labels = %v, want the user's label kept", after.Config.Labels)
	}
	if after.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Errorf("restart policy = %q", after.HostConfig.RestartPolicy.Name)
	}
	// …and what the old image supplied gives way to the new image's.
	if !slices.Contains(after.Config.Env, "FLAVOUR=new") || slices.Contains(after.Config.Env, "FLAVOUR=old") {
		t.Errorf("env = %v, want the new image's FLAVOUR=new and not the old default", after.Config.Env)
	}
	if after.Config.Labels["built"] != "new" {
		t.Errorf("label built = %q, want the new image's", after.Config.Labels["built"])
	}
	// Data stays: both volumes are the same ones.
	if got, _ := mountAt(after, "/named"); got != volume {
		t.Errorf("named volume = %q, want %q", got, volume)
	}
	if got, _ := mountAt(after, "/anon"); got != anonBefore {
		t.Errorf("anonymous volume = %q, want the same one %q", got, anonBefore)
	}
	if got, _ := mountAt(after, "/anon-mount"); got != anonMountBefore {
		t.Errorf("anonymous volume declared as a mount = %q, want the same one %q", got, anonMountBefore)
	}
	for _, dir := range []string{"/named", "/anon", "/anon-mount"} {
		if got := execIn(t, cli, after.ID, "cat", dir+"/marker"); !strings.Contains(got, "kept") {
			t.Errorf("%s/marker reads %q after the replacement: the data did not come along", dir, got)
		}
	}
	// And it is still built for the same platform.
	if got := imageArch(t, cli, after.Image); got != archBefore {
		t.Errorf("architecture went from %s to %s", archBefore, got)
	}
	// Nothing is left parked.
	if _, err := cli.ContainerInspect(ctx, name+"-old-"+oldID[:8], client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("the old container is still there (inspect err = %v)", err)
	}

	// Asking again is a no-op.
	again, err := actions.Replace(ctx, actions.Env{Docker: cli}, after.ID, func(actions.Event) {})
	if err != nil || again {
		t.Errorf("second Replace = %v, %v; want false, nil", again, err)
	}
}

func TestRealReplaceRollsBack(t *testing.T) {
	cli := daemon(t)
	ref := unique("img") + ":latest"
	name := unique("app")

	commitImage(t, cli, ref, `CMD ["sleep","3600"]`, "STOPSIGNAL SIGKILL")
	oldID := run(t, cli, name, &container.Config{Image: ref, Env: []string{"APP_ENV=prod"}}, nil)

	// The new image is broken: it exits at once.
	commitImage(t, cli, ref, `CMD ["false"]`)

	replaced, err := actions.Replace(ctx, actions.Env{Docker: cli, StartGrace: 2 * time.Second, Poll: 100 * time.Millisecond}, oldID, func(actions.Event) {})
	if err == nil || replaced {
		t.Fatalf("Replace = %v, %v; want the failure reported", replaced, err)
	}
	if !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("err = %v, want it to give the exit code", err)
	}

	back := inspect(t, cli, name)
	if back.ID != oldID || !back.State.Running {
		t.Errorf("after rollback: id %.12s running %v; want the original container %.12s, running", back.ID, back.State.Running, oldID)
	}
	if _, err := cli.ContainerInspect(ctx, name+"-old-"+oldID[:8], client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("a parked container was left behind (inspect err = %v)", err)
	}
}

func TestRealLogs(t *testing.T) {
	cli := daemon(t)
	name := unique("logs")
	id := run(t, cli, name, &container.Config{
		Image:      baseImage,
		Cmd:        []string{"sh", "-c", "echo to-stdout; echo to-stderr >&2; sleep 3600"},
		StopSignal: "SIGKILL",
	}, nil)

	var lines []streams.LogLine
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		lines = lines[:0]
		if err := streams.ReadLogs(ctx, cli, id, name, streams.LogOptions{Tail: "all"}, func(l streams.LogLine) { lines = append(lines, l) }); err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if len(lines) >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	byStream := map[string]string{}
	for _, l := range lines {
		byStream[l.Stream] = l.Text
		if l.Container != name {
			t.Errorf("line container = %q, want %q", l.Container, name)
		}
		if _, err := time.Parse(time.RFC3339Nano, l.Time); err != nil {
			t.Errorf("line time %q is not a timestamp", l.Time)
		}
	}
	if byStream["stdout"] != "to-stdout" || byStream["stderr"] != "to-stderr" {
		t.Errorf("lines = %+v, want one on each stream", lines)
	}

	// Following ends when asked to.
	fctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	seen := make(chan struct{}, 8)
	go func() {
		done <- streams.ReadLogs(fctx, cli, id, name, streams.LogOptions{Tail: "1", Follow: true}, func(streams.LogLine) { seen <- struct{}{} })
	}()
	select {
	case <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("following delivered nothing")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("follow ended with %v, want nil after cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("following did not stop after its context ended")
	}
}

func TestRealShell(t *testing.T) {
	cli := daemon(t)
	name := unique("shell")
	cfg := sleeper()
	cfg.StopSignal = "SIGKILL"
	id := run(t, cli, name, cfg, nil)

	s, err := streams.StartShell(ctx, cli, id, 100, 30)
	if err != nil {
		t.Fatalf("StartShell: %v", err)
	}
	defer s.Close()

	// "hi-2" appears only in the output: the typed command has no "2" there.
	if _, err := s.Write([]byte("echo hi-$((1+1)); stty size\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var out strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	for !(strings.Contains(out.String(), "hi-2") && strings.Contains(out.String(), "30 100")) && time.Now().Before(deadline) {
		n, err := s.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(out.String(), "hi-2") {
		t.Fatalf("shell output %q does not contain the command's result", out.String())
	}
	if !strings.Contains(out.String(), "30 100") {
		t.Errorf("shell output %q: the terminal is not 30 rows by 100 columns", out.String())
	}

	if err := s.Resize(ctx, 132, 50); err != nil {
		t.Errorf("Resize: %v", err)
	}

	if _, err := s.Write([]byte("exit 3\n")); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Read(buf); err != nil {
			break
		}
	}
	code, err := s.ExitCode(ctx)
	if code != 3 || err != nil {
		t.Errorf("ExitCode = %d, %v; want 3", code, err)
	}

	// A stopped container has no shell to offer.
	if _, err := cli.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := streams.StartShell(ctx, cli, id, 80, 24); !errors.Is(err, streams.ErrNotRunning) {
		t.Errorf("StartShell on a stopped container: err = %v, want ErrNotRunning", err)
	}
}

func TestRealUpdateCheck(t *testing.T) {
	cli := daemon(t)
	checker := updates.NewChecker(nil, time.Now)

	r := checker.Check(ctx, cli, "local", []string{baseImage}, true)[baseImage]
	switch r.Status {
	case updates.UpToDate, updates.Available:
		if !strings.HasPrefix(r.LocalImageID, "sha256:") {
			t.Errorf("LocalImageID = %q", r.LocalImageID)
		}
	case updates.Unknown:
		// The registry could not be reached from here; that is the network's
		// doing, not the code's.
		t.Skipf("registry check undecided: %s", r.Reason)
	default:
		t.Errorf("result = %+v", r)
	}

	// An image that only exists here is reported as such, without a registry call failing.
	ref := unique("img") + ":latest"
	commitImage(t, cli, ref, `CMD ["true"]`)
	if r := checker.Check(ctx, cli, "local", []string{ref}, true)[ref]; r.Status != updates.Unknown || r.Reason != updates.ReasonBuiltLocally {
		t.Errorf("local-only image: %+v, want unknown / built locally", r)
	}
}

// execIn runs a command in a container and returns what it printed.
func execIn(t *testing.T, cli *client.Client, containerID string, cmd ...string) string {
	t.Helper()
	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: cmd, AttachStdout: true, AttachStderr: true, TTY: true})
	if err != nil {
		t.Fatalf("exec %v: %v", cmd, err)
	}
	attached, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		t.Fatalf("exec %v: %v", cmd, err)
	}
	defer attached.Close()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := attached.Reader.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return out.String()
}

func imageArch(t *testing.T, cli *client.Client, imageID string) string {
	t.Helper()
	img, err := cli.ImageInspect(ctx, imageID)
	if err != nil {
		t.Fatalf("inspect image %s: %v", imageID, err)
	}
	return img.Os + "/" + img.Architecture
}
