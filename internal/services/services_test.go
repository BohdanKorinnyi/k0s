package services

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"

	"dc0s/internal/dockerapi/dockertest"
)

var ctx = context.Background()

func composeContainer(project, service string, number int, image string) container.InspectResponse {
	c := dockertest.Running(fmt.Sprintf("%s-%s-%d", project, service, number), image)
	c.Config.Labels = dockertest.ComposeLabels(project, service, number)
	return c
}

func list(t *testing.T, f *dockertest.Fake) []Group {
	t.Helper()
	groups, err := List(ctx, f)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return groups
}

func only(t *testing.T, f *dockertest.Fake) Service {
	t.Helper()
	groups := list(t, f)
	if len(groups) != 1 || len(groups[0].Services) != 1 {
		t.Fatalf("want exactly one service, got %+v", groups)
	}
	return groups[0].Services[0]
}

func TestComposeGrouping(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(composeContainer("shop", "api", 2, "shop/api:develop"))
	f.AddContainer(composeContainer("shop", "api", 1, "shop/api:develop"))

	groups := list(t, f)
	if len(groups) != 1 || groups[0].Project != "shop" {
		t.Fatalf("groups = %+v", groups)
	}
	s := only(t, f)
	if s.Key != "c~shop~api" || s.Name != "api" || s.Project != "shop" {
		t.Errorf("key/name/project = %q/%q/%q", s.Key, s.Name, s.Project)
	}
	if s.Running != 2 || s.Total != 2 || s.State != "running" {
		t.Errorf("running/total/state = %d/%d/%q", s.Running, s.Total, s.State)
	}
	if s.Image != "shop/api:develop" || !reflect.DeepEqual(s.Images, []string{"shop/api:develop"}) {
		t.Errorf("image = %q, images = %v", s.Image, s.Images)
	}
	if len(s.Containers) != 2 || s.Containers[0].Name != "shop-api-1" || s.Containers[1].Name != "shop-api-2" {
		t.Errorf("containers = %+v, want shop-api-1 then shop-api-2", s.Containers)
	}
}

func TestContainerFields(t *testing.T) {
	f := dockertest.New()
	c := dockertest.Running("redis-cache", "redis:7.4")
	c.RestartCount = 3
	c.State.StartedAt = "2026-11-20T10:00:00.5Z"
	c.State.Health = &container.Health{Status: container.Healthy}
	id := f.AddContainer(c)

	got := only(t, f).Containers[0]
	want := Container{
		ID:        id,
		Name:      "redis-cache",
		Image:     "redis:7.4",
		ImageID:   c.Image,
		State:     "running",
		Health:    "healthy",
		StartedAt: time.Date(2026, 11, 20, 10, 0, 0, 500_000_000, time.UTC),
		Restarts:  3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("container = %+v\nwant        %+v", got, want)
	}
}

func TestStandalone(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(composeContainer("shop", "api", 1, "shop/api:develop"))
	f.AddContainer(dockertest.Running("redis-cache", "redis:7.4"))

	groups := list(t, f)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	last := groups[1]
	if last.Project != "" || len(last.Services) != 1 {
		t.Fatalf("last group = %+v, want the no-project group with one service", last)
	}
	s := last.Services[0]
	if s.Key != "s~redis-cache" || s.Name != "redis-cache" || s.Project != "" || s.Total != 1 {
		t.Errorf("standalone service = %+v", s)
	}
}

func TestOneOff(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(composeContainer("shop", "api", 1, "shop/api:develop"))
	run := dockertest.Running("shop-api-run-3f2a", "shop/api:develop")
	run.Config.Labels = dockertest.ComposeLabels("shop", "api", 1)
	run.Config.Labels["com.docker.compose.oneoff"] = "True"
	f.AddContainer(run)

	groups := list(t, f)
	if len(groups) != 1 || groups[0].Project != "shop" || len(groups[0].Services) != 2 {
		t.Fatalf("groups = %+v, want one shop group with two rows", groups)
	}
	api, oneoff := groups[0].Services[0], groups[0].Services[1]
	if api.Key != "c~shop~api" || api.Total != 1 {
		t.Errorf("api = %+v, want the service with only its real container", api)
	}
	if oneoff.Key != "s~shop-api-run-3f2a" || oneoff.Name != "shop-api-run-3f2a" || oneoff.Project != "shop" {
		t.Errorf("one-off row = %+v", oneoff)
	}
}

func TestOrdering(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("zeta", "z:1"))
	f.AddContainer(composeContainer("shop", "web", 1, "w:1"))
	f.AddContainer(composeContainer("monitoring", "grafana", 1, "g:1"))
	f.AddContainer(composeContainer("shop", "api", 1, "a:1"))
	f.AddContainer(dockertest.Running("alpha", "a:1"))

	var got []string
	for _, g := range list(t, f) {
		for _, s := range g.Services {
			got = append(got, g.Project+"/"+s.Name)
		}
	}
	want := []string{"monitoring/grafana", "shop/api", "shop/web", "/alpha", "/zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}
}

func TestState(t *testing.T) {
	running := func(n int) container.InspectResponse { return composeContainer("p", "s", n, "app:1") }
	stopped := func(n, code int, finished string) container.InspectResponse {
		c := dockertest.Stopped(fmt.Sprintf("p-s-%d", n), "app:1", code)
		c.Config.Labels = dockertest.ComposeLabels("p", "s", n)
		c.State.FinishedAt = finished
		return c
	}
	unhealthy := func(n int) container.InspectResponse {
		c := running(n)
		c.State.Health = &container.Health{Status: container.Unhealthy}
		return c
	}
	restarting := func(n int) container.InspectResponse {
		c := running(n)
		c.State.Status, c.State.Running, c.State.Restarting = container.StateRestarting, true, true
		return c
	}

	cases := []struct {
		name       string
		containers []container.InspectResponse
		state      string
		running    int
		wantExit   int
	}{
		{"all running", []container.InspectResponse{running(1), running(2)}, "running", 2, 0},
		{"one restarting wins over unhealthy", []container.InspectResponse{unhealthy(1), restarting(2)}, "restarting", 1, 0},
		{"one unhealthy", []container.InspectResponse{running(1), unhealthy(2)}, "unhealthy", 2, 0},
		{"one of two running", []container.InspectResponse{running(1), stopped(2, 1, "2026-11-21T10:00:00Z")}, "partial", 1, 0},
		{"none running reports the latest exit", []container.InspectResponse{
			stopped(1, 1, "2026-11-21T10:00:00Z"),
			stopped(2, 137, "2026-11-22T10:00:00Z"),
		}, "stopped", 0, 137},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := dockertest.New()
			for _, ctr := range c.containers {
				f.AddContainer(ctr)
			}
			s := only(t, f)
			if s.State != c.state || s.Running != c.running || s.Total != len(c.containers) {
				t.Errorf("state/running/total = %q/%d/%d, want %q/%d/%d", s.State, s.Running, s.Total, c.state, c.running, len(c.containers))
			}
			if s.ExitCode != c.wantExit {
				t.Errorf("ExitCode = %d, want %d", s.ExitCode, c.wantExit)
			}
		})
	}
}

// Docker keeps the last health result on a container after it stops. A service
// that is down is "stopped", whatever its last health check said.
func TestStoppedContainerIsNotUnhealthy(t *testing.T) {
	f := dockertest.New()
	c := dockertest.Stopped("db", "postgres:16", 1)
	c.State.Health = &container.Health{Status: container.Unhealthy}
	f.AddContainer(c)

	s := only(t, f)
	if s.State != "stopped" || s.ExitCode != 1 {
		t.Errorf("state = %q (exit %d), want stopped with exit code 1", s.State, s.ExitCode)
	}
	if got := s.Containers[0].Health; got != "" {
		t.Errorf("container health = %q, want none for a container that is not running", got)
	}
}

func TestMixedImages(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(composeContainer("p", "s", 1, "app:2"))
	f.AddContainer(composeContainer("p", "s", 2, "app:1"))
	s := only(t, f)
	if s.Image != "" || !reflect.DeepEqual(s.Images, []string{"app:1", "app:2"}) {
		t.Errorf("image = %q, images = %v; want \"\" and [app:1 app:2]", s.Image, s.Images)
	}
}

func TestTotals(t *testing.T) {
	f := dockertest.New()
	a := composeContainer("p", "s", 1, "app:1")
	a.RestartCount = 3
	a.State.StartedAt = "2026-11-20T10:00:00Z"
	b := composeContainer("p", "s", 2, "app:1")
	b.RestartCount = 11
	b.State.StartedAt = "2026-11-23T08:30:00Z"
	c := dockertest.Stopped("p-s-3", "app:1", 0)
	c.Config.Labels = dockertest.ComposeLabels("p", "s", 3)
	c.State.StartedAt = "2026-11-25T00:00:00Z" // later, but not running: must not count
	f.AddContainer(a)
	f.AddContainer(b)
	f.AddContainer(c)

	s := only(t, f)
	if s.Restarts != 14 {
		t.Errorf("Restarts = %d, want 14", s.Restarts)
	}
	if want := time.Date(2026, 11, 23, 8, 30, 0, 0, time.UTC); !s.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, want)
	}
}

func TestStoppedServiceHasZeroStartedAt(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Stopped("job", "app:1", 0))
	if s := only(t, f); !s.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero for a stopped service", s.StartedAt)
	}
}

func TestVanishedContainer(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("stays", "app:1"))
	f.AddContainer(dockertest.Running("goes", "app:1"))
	// The container is listed, then removed before it can be inspected.
	f.FailFor("ContainerInspect", "goes", fmt.Errorf("No such container: goes: %w", cerrdefs.ErrNotFound))

	s := only(t, f)
	if s.Name != "stays" {
		t.Errorf("service = %+v, want only the container that still exists", s)
	}
}

func TestInspectErrorIsReturned(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("a", "app:1"))
	boom := errors.New("connection reset")
	f.FailFor("ContainerInspect", "a", boom)
	if _, err := List(ctx, f); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the inspect error", err)
	}
}

func TestListError(t *testing.T) {
	f := dockertest.New()
	boom := errors.New("cannot connect to the Docker daemon")
	f.Fail("ContainerList", boom)
	if _, err := List(ctx, f); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the list error", err)
	}
}

func TestEmptyHost(t *testing.T) {
	groups := list(t, dockertest.New())
	if len(groups) != 0 {
		t.Errorf("groups = %+v, want none", groups)
	}
}

func TestManyContainers(t *testing.T) {
	f := dockertest.New()
	for i := 1; i <= 40; i++ {
		f.AddContainer(composeContainer("big", "worker", i, "app:1"))
	}
	s := only(t, f)
	if s.Total != 40 || s.Containers[0].Name != "big-worker-1" || s.Containers[39].Name != "big-worker-40" {
		t.Errorf("total = %d, first = %s, last = %s", s.Total, s.Containers[0].Name, s.Containers[39].Name)
	}
}

func TestFindAndGet(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(composeContainer("shop", "api", 1, "app:1"))
	f.AddContainer(dockertest.Running("redis-cache", "redis:7.4"))

	groups := list(t, f)
	if s, ok := Find(groups, "s~redis-cache"); !ok || s.Name != "redis-cache" {
		t.Errorf("Find(s~redis-cache) = %+v, %v", s, ok)
	}
	if _, ok := Find(groups, "c~shop~nope"); ok {
		t.Error("Find reported a service that does not exist")
	}
	s, err := Get(ctx, f, "c~shop~api")
	if err != nil || s.Name != "api" {
		t.Errorf("Get(c~shop~api) = %+v, %v", s, err)
	}
	if _, err := Get(ctx, f, "c~shop~nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown: err = %v, want ErrNotFound", err)
	}
}
