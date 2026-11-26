package actions

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"dockpanel/internal/dockerapi/dockertest"
	"dockpanel/internal/registryauth"
	"dockpanel/internal/services"
)

const hex64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// pullRig is a two-container service on app:develop whose containers run the
// old image. Unless told otherwise, a pull brings the new image.
type pullRig struct {
	f   *dockertest.Fake
	env Env
	svc services.Service
	ids []string
}

func newPullRig(t *testing.T, pullBringsNew bool) *pullRig {
	t.Helper()
	f := dockertest.New()
	f.AddImage("app:develop", imageWith(oldImageID, ocispec.ImageConfig{}, nil))
	if pullBringsNew {
		f.SetPullResult("app:develop", imageWith(newImageID, ocispec.ImageConfig{}, nil))
	}
	r := &pullRig{f: f, env: Env{Docker: f, StartGrace: 5 * time.Millisecond, Poll: time.Millisecond, HealthTimeout: 50 * time.Millisecond}}
	for i := 1; i <= 2; i++ {
		c := dockertest.Running(fmt.Sprintf("shop-api-%d", i), "app:develop")
		c.Image = oldImageID
		c.Config.Labels = dockertest.ComposeLabels("shop", "api", i)
		r.ids = append(r.ids, f.AddContainer(c))
	}
	svc, err := services.Get(ctx, f, "c~shop~api")
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

func (r *pullRig) run(t *testing.T) (*recorder, error) {
	t.Helper()
	a, ok := Default().Get("pull-restart")
	if !ok {
		t.Fatal("pull-restart is not registered")
	}
	rec := &recorder{}
	return rec, a.Run(ctx, r.env, Target{Service: r.svc}, rec.report)
}

func TestPullRestartIsRegisteredLast(t *testing.T) {
	got := Default().List()
	want := Info{ID: "pull-restart", Label: "Pull & restart", Destructive: true, ContainerLevel: false}
	if len(got) != 4 || got[3] != want {
		t.Errorf("Default().List() = %+v, want four actions ending with %+v", got, want)
	}
}

func TestPullRestart(t *testing.T) {
	r := newPullRig(t, true)
	rec, err := r.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	old1, old2 := "shop-api-1-old-"+r.ids[0][:8], "shop-api-2-old-"+r.ids[1][:8]
	want := []string{
		"ImagePull app:develop",
		"ContainerStop shop-api-1", "ContainerRename shop-api-1->" + old1, "ContainerCreate shop-api-1", "ContainerStart shop-api-1", "ContainerRemove " + old1,
		"ContainerStop shop-api-2", "ContainerRename shop-api-2->" + old2, "ContainerCreate shop-api-2", "ContainerStart shop-api-2", "ContainerRemove " + old2,
	}
	if got := r.f.CallLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q\nwant    %q", got, want)
	}
	for _, name := range []string{"shop-api-1", "shop-api-2"} {
		if c, _ := r.f.Container(name); c.Image != newImageID || !c.State.Running {
			t.Errorf("%s runs %s (running %v), want the new image, running", name, c.Image, c.State.Running)
		}
	}
	if !slices.Contains(rec.steps(), "info: Pulling app:develop") {
		t.Errorf("events = %q, want one announcing the pull", rec.steps())
	}
	if slices.ContainsFunc(rec.events, func(e Event) bool { return e.Step == "Already up to date" }) {
		t.Error("reported \"Already up to date\" although containers were replaced")
	}
}

func TestPullRestartUpToDate(t *testing.T) {
	r := newPullRig(t, false)
	for _, name := range []string{"shop-api-1", "shop-api-2"} {
		_ = name
	}
	// The containers already run what the tag points to.
	r.f.AddImage("app:develop", imageWith(oldImageID, ocispec.ImageConfig{}, nil))
	rec, err := r.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"ImagePull app:develop"}; !reflect.DeepEqual(r.f.CallLog(), want) {
		t.Errorf("calls = %q, want only the pull", r.f.CallLog())
	}
	if !slices.ContainsFunc(rec.events, func(e Event) bool { return e.Step == "Already up to date" && e.Level == LevelInfo }) {
		t.Errorf("events = %q, want \"Already up to date\"", rec.steps())
	}
}

// The tag was pulled earlier by hand, but the containers were never replaced:
// nothing new comes down, yet the containers are behind and must be replaced.
func TestPullRestartReplacesContainersBehindTheLocalTag(t *testing.T) {
	r := newPullRig(t, false)
	r.f.AddImage("app:develop", imageWith(newImageID, ocispec.ImageConfig{}, nil))
	if _, err := r.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c, _ := r.f.Container("shop-api-1"); c.Image != newImageID {
		t.Errorf("shop-api-1 still runs %s", c.Image)
	}
}

func TestPullRestartPullError(t *testing.T) {
	r := newPullRig(t, true)
	r.f.SetPullError("app:develop", "manifest for app:develop not found: manifest unknown")
	_, err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") || !strings.Contains(err.Error(), "app:develop") {
		t.Errorf("err = %v, want the registry's message and the reference", err)
	}
	if want := []string{"ImagePull app:develop"}; !reflect.DeepEqual(r.f.CallLog(), want) {
		t.Errorf("calls = %q, want only the pull (nothing replaced)", r.f.CallLog())
	}
}

func TestPullRestartPullRefused(t *testing.T) {
	r := newPullRig(t, true)
	boom := errors.New("Cannot connect to the Docker daemon")
	r.f.Fail("ImagePull", boom)
	if _, err := r.run(t); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the Docker error", err)
	}
}

func TestPullRestartStopsAtFailure(t *testing.T) {
	r := newPullRig(t, true)
	r.f.Before = func(method string, c *container.InspectResponse) error {
		if method == "ContainerStart" && c.Image == newImageID {
			return errors.New("port is already allocated")
		}
		return nil
	}
	_, err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "shop-api-1") {
		t.Errorf("err = %v, want the failure naming shop-api-1", err)
	}
	if c, _ := r.f.Container("shop-api-1"); c.ID != r.ids[0] || !c.State.Running {
		t.Error("shop-api-1 was not put back")
	}
	if c, _ := r.f.Container("shop-api-2"); c.ID != r.ids[1] || c.Image != oldImageID {
		t.Error("shop-api-2 was touched after the first replacement failed")
	}
	for _, call := range r.f.CallLog() {
		if strings.Contains(call, "shop-api-2") {
			t.Errorf("call %q: the second container must be left alone", call)
		}
	}
}

func TestPullRestartSendsRegistryLogin(t *testing.T) {
	r := newPullRig(t, true)
	r.f.AddImage("ghcr.io/acme/api:develop", imageWith(oldImageID, ocispec.ImageConfig{}, nil))
	c := dockertest.Running("private-1", "ghcr.io/acme/api:develop")
	c.Image = oldImageID
	r.f.AddContainer(c)
	svc, err := services.Get(ctx, r.f, "s~private-1")
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	r.env.Auth = registryauth.Static{List: func() []registryauth.Credential {
		return []registryauth.Credential{{Address: "ghcr.io", Username: "bot", Password: "pw"}}
	}}
	if _, err := r.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.f.LastAuth("ghcr.io/acme/api:develop") == "" {
		t.Error("the pull carried no login although one is saved for ghcr.io")
	}
}

func TestPullRestartMixedImagesPullsEachOnce(t *testing.T) {
	f := dockertest.New()
	for i, ref := range []string{"app:1", "app:2", "app:1"} {
		f.AddImage(ref, imageWith(oldImageID, ocispec.ImageConfig{}, nil))
		c := dockertest.Running(fmt.Sprintf("p-s-%d", i+1), ref)
		c.Image = oldImageID
		c.Config.Labels = dockertest.ComposeLabels("p", "s", i+1)
		f.AddContainer(c)
	}
	svc, _ := services.Get(ctx, f, "c~p~s")
	a, _ := Default().Get("pull-restart")
	if err := a.Run(ctx, Env{Docker: f}, Target{Service: svc}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ImagePull app:1", "ImagePull app:2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want each reference pulled once", f.CallLog())
	}
}

func TestPullRestartApplies(t *testing.T) {
	a, _ := Default().Get("pull-restart")
	tagged := services.Service{Total: 1, Containers: []services.Container{{Image: "app:develop"}}}
	pinned := services.Service{Total: 2, Containers: []services.Container{
		{Image: "app@sha256:" + hex64}, {Image: "sha256:" + hex64},
	}}
	mixed := services.Service{Total: 2, Containers: []services.Container{
		{Image: "app@sha256:" + hex64}, {Image: "app:develop"},
	}}
	if !a.Applies(tagged) || !a.Applies(mixed) {
		t.Error("pull-restart should apply when at least one container uses a tag")
	}
	if a.Applies(pinned) {
		t.Error("pull-restart should not apply when every reference is pinned")
	}
	if a.Applies(services.Service{}) {
		t.Error("pull-restart should not apply to a service with no containers")
	}
}

func TestPullRestartSkipsPinnedContainers(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:develop", imageWith(oldImageID, ocispec.ImageConfig{}, nil))
	f.SetPullResult("app:develop", imageWith(newImageID, ocispec.ImageConfig{}, nil))
	tagged := dockertest.Running("p-s-1", "app:develop")
	tagged.Image = oldImageID
	tagged.Config.Labels = dockertest.ComposeLabels("p", "s", 1)
	pinned := dockertest.Running("p-s-2", "app@sha256:"+hex64)
	pinned.Image = oldImageID
	pinned.Config.Labels = dockertest.ComposeLabels("p", "s", 2)
	f.AddContainer(tagged)
	pinnedID := f.AddContainer(pinned)

	svc, _ := services.Get(ctx, f, "c~p~s")
	a, _ := Default().Get("pull-restart")
	env := Env{Docker: f, StartGrace: 5 * time.Millisecond, Poll: time.Millisecond}
	if err := a.Run(ctx, env, Target{Service: svc}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.Container("p-s-2"); c.ID != pinnedID {
		t.Error("the pinned container was replaced")
	}
	if c, _ := f.Container("p-s-1"); c.Image != newImageID {
		t.Error("the tagged container was not replaced")
	}
}

func TestPullRestartRejectsSingleContainerTarget(t *testing.T) {
	r := newPullRig(t, true)
	a, _ := Default().Get("pull-restart")
	err := a.Run(ctx, r.env, Target{Service: r.svc, ContainerID: r.ids[0]}, func(Event) {})
	if err == nil {
		t.Error("want an error: pulling changes the image for the whole service")
	}
	if len(r.f.CallLog()) != 0 {
		t.Errorf("calls = %q, want none", r.f.CallLog())
	}
}

// If a replacement's health cannot be confirmed in time, it is left running —
// but the rest of the service is not replaced on trust. A bad image must not
// be able to take every container of a service.
func TestPullRestartStopsWhenHealthIsNotConfirmed(t *testing.T) {
	r := newPullRig(t, true)
	r.f.OnInspect = func(c *container.InspectResponse) {
		if c.Image == newImageID && c.State.Running {
			c.State.Health = &container.Health{Status: container.Starting}
		}
	}
	_, err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "shop-api-1") || !strings.Contains(err.Error(), "shop-api-2") {
		t.Fatalf("err = %v, want it to say shop-api-1 is unconfirmed and shop-api-2 was left alone", err)
	}
	if c, _ := r.f.Container("shop-api-1"); c.Image != newImageID {
		t.Error("shop-api-1 was rolled back; a slow start should be kept")
	}
	if c, _ := r.f.Container("shop-api-2"); c.ID != r.ids[1] || c.Image != oldImageID {
		t.Error("shop-api-2 was replaced although the first replacement was never confirmed healthy")
	}
}

func TestPullRestartLastContainerStillStartingIsOnlyAWarning(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:develop", imageWith(oldImageID, ocispec.ImageConfig{}, nil))
	f.SetPullResult("app:develop", imageWith(newImageID, ocispec.ImageConfig{}, nil))
	c := dockertest.Running("solo", "app:develop")
	c.Image = oldImageID
	f.AddContainer(c)
	f.OnInspect = func(c *container.InspectResponse) {
		if c.Image == newImageID && c.State.Running {
			c.State.Health = &container.Health{Status: container.Starting}
		}
	}
	svc, _ := services.Get(ctx, f, "s~solo")
	a, _ := Default().Get("pull-restart")
	rec := &recorder{}
	env := Env{Docker: f, StartGrace: 5 * time.Millisecond, Poll: time.Millisecond, HealthTimeout: 40 * time.Millisecond}
	if err := a.Run(ctx, env, Target{Service: svc}, rec.report); err != nil {
		t.Fatalf("Run = %v; with nothing left to replace, an unconfirmed start is a warning, not a failure", err)
	}
	if !slices.ContainsFunc(rec.events, func(e Event) bool { return e.Level == LevelWarning }) {
		t.Errorf("events = %q, want a warning", rec.steps())
	}
}

// The pull asks for the platform the containers already run, so an amd64
// container on an arm64 host is not handed the native image.
func TestPullRestartKeepsThePlatform(t *testing.T) {
	r := newPullRig(t, true)
	old := imageWith(oldImageID, ocispec.ImageConfig{}, nil)
	old.Os, old.Architecture = "linux", "amd64"
	r.f.AddImage(oldImageID, old)

	if _, err := r.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := r.f.LastPull("app:develop").Platforms
	if len(got) != 1 || got[0].OS != "linux" || got[0].Architecture != "amd64" {
		t.Errorf("pull platforms = %+v, want linux/amd64", got)
	}
}

// Asked to update the panel itself, say so at once — before pulling, which for
// a locally built panel image fails with a message about the registry that
// has nothing to do with the real reason.
func TestPullRestartRefusesThePanelItselfBeforePulling(t *testing.T) {
	r := newPullRig(t, true)
	r.env.SelfContainerID = r.ids[1][:12]
	_, err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "the panel's own container") {
		t.Fatalf("err = %v, want a refusal explaining it is the panel itself", err)
	}
	if calls := r.f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want nothing pulled or replaced", calls)
	}
}
