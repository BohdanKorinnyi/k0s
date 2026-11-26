package actions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"dockpanel/internal/dockerapi/dockertest"
	"dockpanel/internal/services"
)

var ctx = context.Background()

// shopAPI puts a two-container Compose service on the fake host and returns
// it as the services package sees it.
func shopAPI(t *testing.T, f *dockertest.Fake, states ...string) services.Service {
	t.Helper()
	if len(states) == 0 {
		states = []string{"running", "running"}
	}
	for i, state := range states {
		name := fmt.Sprintf("shop-api-%d", i+1)
		var c container.InspectResponse
		switch state {
		case "running":
			c = dockertest.Running(name, "shop/api:develop")
		case "stopped":
			c = dockertest.Stopped(name, "shop/api:develop", 1)
		case "restarting":
			c = dockertest.Running(name, "shop/api:develop")
			c.State.Status, c.State.Restarting = container.StateRestarting, true
		default:
			t.Fatalf("unknown state %q", state)
		}
		c.Config.Labels = dockertest.ComposeLabels("shop", "api", i+1)
		f.AddContainer(c)
	}
	svc, err := services.Get(ctx, f, "c~shop~api")
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type recorder struct{ events []Event }

func (r *recorder) report(e Event) { r.events = append(r.events, e) }

func (r *recorder) steps() []string {
	var out []string
	for _, e := range r.events {
		out = append(out, e.Level+": "+e.Step)
	}
	return out
}

func run(t *testing.T, id string, f *dockertest.Fake, target Target) (*recorder, error) {
	t.Helper()
	a, ok := Default().Get(id)
	if !ok {
		t.Fatalf("no action %q", id)
	}
	rec := &recorder{}
	return rec, a.Run(ctx, Env{Docker: f}, target, rec.report)
}

func TestDefaultActions(t *testing.T) {
	got := Default().List()
	want := []Info{
		{ID: "restart", Label: "Restart", Destructive: false, ContainerLevel: true},
		{ID: "start", Label: "Start", Destructive: false, ContainerLevel: true},
		{ID: "stop", Label: "Stop", Destructive: true, ContainerLevel: true},
	}
	if len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Errorf("Default().List() = %+v\nwant it to start with %+v", got, want)
	}
}

func TestApplies(t *testing.T) {
	cases := []struct {
		states []string
		want   []string
	}{
		{[]string{"running", "running"}, []string{"restart", "stop"}},
		{[]string{"running", "stopped"}, []string{"restart", "start", "stop"}},
		{[]string{"stopped", "stopped"}, []string{"restart", "start"}},
		// A crash-looping container is not "running", but it can be stopped.
		{[]string{"restarting"}, []string{"restart", "stop"}},
	}
	for _, c := range cases {
		svc := shopAPI(t, dockertest.New(), c.states...)
		var got []string
		for _, id := range Default().Applicable(svc) {
			if id == "restart" || id == "start" || id == "stop" {
				got = append(got, id)
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("states %v: applicable = %v, want %v", c.states, got, c.want)
		}
	}
}

func TestRestartService(t *testing.T) {
	f := dockertest.New()
	svc := shopAPI(t, f)
	rec, err := run(t, "restart", f, Target{Service: svc})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"ContainerRestart shop-api-1", "ContainerRestart shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want %q", f.CallLog(), want)
	}
	if want := []string{"info: Restarting shop-api-1", "info: Restarting shop-api-2"}; !reflect.DeepEqual(rec.steps(), want) {
		t.Errorf("events = %q, want %q", rec.steps(), want)
	}
}

func TestRestartOneContainer(t *testing.T) {
	f := dockertest.New()
	svc := shopAPI(t, f)
	if _, err := run(t, "restart", f, Target{Service: svc, ContainerID: svc.Containers[1].ID}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"ContainerRestart shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want %q", f.CallLog(), want)
	}

	f.ResetCalls()
	_, err := run(t, "restart", f, Target{Service: svc, ContainerID: "not-in-this-service"})
	if err == nil {
		t.Error("want an error for a container that is not part of the service")
	}
	if len(f.CallLog()) != 0 {
		t.Errorf("calls = %q, want none", f.CallLog())
	}
}

func TestStopsAtFirstFailure(t *testing.T) {
	f := dockertest.New()
	svc := shopAPI(t, f)
	boom := errors.New("OCI runtime error")
	f.FailFor("ContainerRestart", "shop-api-1", boom)

	_, err := run(t, "restart", f, Target{Service: svc})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "shop-api-1") {
		t.Errorf("err = %v, want the Docker error naming shop-api-1", err)
	}
	if want := []string{"ContainerRestart shop-api-1"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want %q (the second container untouched)", f.CallLog(), want)
	}
}

func TestStartSkipsRunning(t *testing.T) {
	f := dockertest.New()
	svc := shopAPI(t, f, "running", "stopped")
	rec, err := run(t, "start", f, Target{Service: svc})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"ContainerStart shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want %q", f.CallLog(), want)
	}
	if want := []string{"info: shop-api-1 is already running", "info: Starting shop-api-2"}; !reflect.DeepEqual(rec.steps(), want) {
		t.Errorf("events = %q, want %q", rec.steps(), want)
	}
}

func TestStopSkipsStopped(t *testing.T) {
	f := dockertest.New()
	svc := shopAPI(t, f, "stopped", "running")
	rec, err := run(t, "stop", f, Target{Service: svc})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"ContainerStop shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want %q", f.CallLog(), want)
	}
	if want := []string{"info: shop-api-1 is already stopped", "info: Stopping shop-api-2"}; !reflect.DeepEqual(rec.steps(), want) {
		t.Errorf("events = %q, want %q", rec.steps(), want)
	}
}

func TestCancelledContextStopsBetweenContainers(t *testing.T) {
	f := dockertest.New()
	svc := shopAPI(t, f)
	cctx, cancel := context.WithCancel(ctx)
	f.After = func(string, *container.InspectResponse) { cancel() }

	a, _ := Default().Get("restart")
	err := a.Run(cctx, Env{Docker: f}, Target{Service: svc}, func(Event) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := len(f.CallLog()); n != 1 {
		t.Errorf("calls = %q, want only the first container touched", f.CallLog())
	}
}

type fakeAction struct{ id string }

func (a fakeAction) Info() Info                                       { return Info{ID: a.id, Label: a.id} }
func (a fakeAction) Applies(services.Service) bool                    { return true }
func (a fakeAction) Run(context.Context, Env, Target, Progress) error { return nil }

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAction{"one"})
	r.Register(fakeAction{"two"})

	if got, ok := r.Get("two"); !ok || got.Info().ID != "two" {
		t.Errorf("Get(two) = %v, %v", got, ok)
	}
	if _, ok := r.Get("three"); ok {
		t.Error("Get reported an action that was never registered")
	}
	if got := r.List(); len(got) != 2 || got[0].ID != "one" || got[1].ID != "two" {
		t.Errorf("List = %+v, want registration order", got)
	}
	if got := r.Applicable(services.Service{}); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Errorf("Applicable = %v", got)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAction{"one"})
	defer func() {
		if recover() == nil {
			t.Error("registering a second action with the same ID did not panic")
		}
	}()
	r.Register(fakeAction{"one"})
}
