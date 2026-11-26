package dockertest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"dc0s/internal/dockerapi"
)

var _ dockerapi.Client = (*Fake)(nil)

var ctx = context.Background()

func TestInspectUnknownIsNotFound(t *testing.T) {
	f := New()
	_, err := f.ContainerInspect(ctx, "nope", client.ContainerInspectOptions{})
	if !cerrdefs.IsNotFound(err) {
		t.Errorf("err = %v, want a not-found error", err)
	}
}

func TestLookupByNameIDAndPrefix(t *testing.T) {
	f := New()
	id := f.AddContainer(Running("api-1", "app:1"))
	for _, ref := range []string{"api-1", "/api-1", id, id[:12]} {
		res, err := f.ContainerInspect(ctx, ref, client.ContainerInspectOptions{})
		if err != nil {
			t.Errorf("inspect %q: %v", ref, err)
			continue
		}
		if res.Container.ID != id || res.Container.Name != "/api-1" {
			t.Errorf("inspect %q returned %s %s", ref, res.Container.ID, res.Container.Name)
		}
	}
}

func TestInspectReturnsACopy(t *testing.T) {
	f := New()
	f.AddContainer(Running("api-1", "app:1"))
	res, _ := f.ContainerInspect(ctx, "api-1", client.ContainerInspectOptions{})
	res.Container.State.Running = false
	res.Container.Config.Env = append(res.Container.Config.Env, "TAMPERED=1")
	again, _ := f.ContainerInspect(ctx, "api-1", client.ContainerInspectOptions{})
	if !again.Container.State.Running || len(again.Container.Config.Env) != 0 {
		t.Errorf("fake state changed through a returned value: %+v", again.Container.State)
	}
}

func TestListRunningOnlyUnlessAll(t *testing.T) {
	f := New()
	f.AddContainer(Running("up", "app:1"))
	f.AddContainer(Stopped("down", "app:1", 137))

	running, _ := f.ContainerList(ctx, client.ContainerListOptions{})
	if len(running.Items) != 1 || running.Items[0].Names[0] != "/up" {
		t.Errorf("running list = %+v", running.Items)
	}
	all, _ := f.ContainerList(ctx, client.ContainerListOptions{All: true})
	if len(all.Items) != 2 {
		t.Errorf("all list has %d items, want 2", len(all.Items))
	}
}

func TestLifecycleChangesStateAndRecordsCalls(t *testing.T) {
	f := New()
	f.AddContainer(Running("api-1", "app:1"))

	if _, err := f.ContainerStop(ctx, "api-1", client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.Container("api-1"); c.State.Running || c.State.Status != container.StateExited {
		t.Errorf("after stop: %+v", c.State)
	}
	if _, err := f.ContainerStart(ctx, "api-1", client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.Container("api-1"); !c.State.Running || c.State.Status != container.StateRunning {
		t.Errorf("after start: %+v", c.State)
	}
	if _, err := f.ContainerRestart(ctx, "api-1", client.ContainerRestartOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ContainerRename(ctx, "api-1", client.ContainerRenameOptions{NewName: "api-old"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Container("api-1"); ok {
		t.Error("old name still resolves after rename")
	}
	if _, err := f.ContainerRemove(ctx, "api-old", client.ContainerRemoveOptions{}); !cerrdefs.IsConflict(err) {
		t.Errorf("removing a running container without force: err = %v, want a conflict", err)
	}
	if _, err := f.ContainerRemove(ctx, "api-old", client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Container("api-old"); ok {
		t.Error("container still present after remove")
	}

	want := []string{
		"ContainerStop api-1",
		"ContainerStart api-1",
		"ContainerRestart api-1",
		"ContainerRename api-1->api-old",
		"ContainerRemove api-old",
		"ContainerRemove api-old",
	}
	if got := f.CallLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q\nwant    %q", got, want)
	}
}

func TestCreateUsesTheImageTheReferencePointsTo(t *testing.T) {
	f := New()
	f.AddImage("app:1", image.InspectResponse{ID: "sha256:aaa"})
	res, err := f.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   "api-1",
		Config: &container.Config{Image: "app:1", Env: []string{"A=1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := f.Container("api-1")
	if !ok || c.ID != res.ID || c.Image != "sha256:aaa" || c.State.Status != container.StateCreated || c.State.Running {
		t.Errorf("created container = %+v (state %+v)", c, c.State)
	}
	if _, err := f.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "api-1", Config: &container.Config{Image: "app:1"}}); !cerrdefs.IsConflict(err) {
		t.Errorf("creating a second container with the same name: err = %v, want a conflict", err)
	}
	if _, err := f.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "x", Config: &container.Config{Image: "missing:1"}}); !cerrdefs.IsNotFound(err) {
		t.Errorf("creating from an unknown image: err = %v, want not found", err)
	}
}

func TestFailAndFailFor(t *testing.T) {
	f := New()
	f.AddContainer(Running("a", "app:1"))
	f.AddContainer(Running("b", "app:1"))
	boom := errors.New("boom")

	f.FailFor("ContainerRestart", "a", boom)
	if _, err := f.ContainerRestart(ctx, "a", client.ContainerRestartOptions{}); !errors.Is(err, boom) {
		t.Errorf("restart a: err = %v, want boom", err)
	}
	if _, err := f.ContainerRestart(ctx, "b", client.ContainerRestartOptions{}); err != nil {
		t.Errorf("restart b: %v", err)
	}

	f.Fail("ContainerList", boom)
	if _, err := f.ContainerList(ctx, client.ContainerListOptions{}); !errors.Is(err, boom) {
		t.Errorf("list: err = %v, want boom", err)
	}
}

func TestBeforeAndAfterHooks(t *testing.T) {
	f := New()
	f.AddContainer(Stopped("a", "app:1", 0))
	f.After = func(method string, c *container.InspectResponse) {
		if method == "ContainerStart" {
			c.State.Running = false
			c.State.Status = container.StateExited
			c.State.ExitCode = 1
		}
	}
	if _, err := f.ContainerStart(ctx, "a", client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.Container("a"); c.State.Running || c.State.ExitCode != 1 {
		t.Errorf("After hook did not apply: %+v", c.State)
	}

	boom := errors.New("refused")
	f.Before = func(method string, c *container.InspectResponse) error {
		if method == "ContainerStop" {
			return boom
		}
		return nil
	}
	if _, err := f.ContainerStop(ctx, "a", client.ContainerStopOptions{}); !errors.Is(err, boom) {
		t.Errorf("Before hook: err = %v, want refused", err)
	}
}

func TestPullMakesTheNewImageCurrent(t *testing.T) {
	f := New()
	f.AddImage("app:develop", image.InspectResponse{ID: "sha256:old"})
	f.SetPullResult("app:develop", image.InspectResponse{ID: "sha256:new"})

	resp, err := f.ImagePull(ctx, "app:develop", client.ImagePullOptions{RegistryAuth: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	img, err := f.ImageInspect(ctx, "app:develop")
	if err != nil || img.ID != "sha256:new" {
		t.Errorf("after pull image = %q, %v; want sha256:new", img.ID, err)
	}
	if _, err := f.ImageInspect(ctx, "sha256:old"); err != nil {
		t.Errorf("the old image should still be on the host: %v", err)
	}
	if f.LastAuth("app:develop") != "token" {
		t.Errorf("auth = %q, want token", f.LastAuth("app:develop"))
	}

	f.SetPullError("bad:1", "manifest unknown")
	resp, err = f.ImagePull(ctx, "bad:1", client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("ImagePull itself should succeed and report the failure in the stream: %v", err)
	}
	if err := resp.Wait(ctx); err == nil {
		t.Error("Wait returned nil for a pull that failed in the stream")
	}
}

func TestDistributionInspect(t *testing.T) {
	f := New()
	f.SetRemoteDigest("app:develop", "sha256:1111111111111111111111111111111111111111111111111111111111111111")
	res, err := f.DistributionInspect(ctx, "app:develop", client.DistributionInspectOptions{EncodedRegistryAuth: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Descriptor.Digest.String() != "sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("digest = %s", res.Descriptor.Digest)
	}
	if f.Count("DistributionInspect") != 1 || f.LastAuth("app:develop") != "abc" {
		t.Errorf("count = %d, auth = %q", f.Count("DistributionInspect"), f.LastAuth("app:develop"))
	}
	if _, err := f.DistributionInspect(ctx, "unknown:1", client.DistributionInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("unknown reference: err = %v, want not found", err)
	}
}

func TestExecEchoesByDefault(t *testing.T) {
	f := New()
	f.AddContainer(Running("a", "app:1"))
	created, err := f.ExecCreate(ctx, "a", client.ExecCreateOptions{Cmd: []string{"sh"}, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.LastExec(); !reflect.DeepEqual(got.Cmd, []string{"sh"}) || !got.TTY {
		t.Errorf("LastExec = %+v", got)
	}
	att, err := f.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	defer att.Close()
	if _, err := att.Conn.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := att.Reader.Read(buf); err != nil || string(buf) != "hi\n" {
		t.Errorf("read %q, %v; want the echoed input", buf, err)
	}
	if _, err := f.ExecResize(ctx, created.ID, client.ExecResizeOptions{Height: 40, Width: 120}); err != nil {
		t.Fatal(err)
	}
	if got := f.CallLog(); !reflect.DeepEqual(got, []string{"ExecCreate a", "ExecResize 120x40"}) {
		t.Errorf("calls = %q", got)
	}
}
