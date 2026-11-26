package actions

import (
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"dc0s/internal/dockerapi/dockertest"
)

const (
	oldImageID = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	newImageID = "sha256:0000000000000000000000000000000000000000000000000000000000000002"
)

func imageWith(id string, cfg ocispec.ImageConfig, health *dockerspec.HealthcheckConfig) image.InspectResponse {
	return image.InspectResponse{ID: id, Config: &dockerspec.DockerOCIImageConfig{
		ImageConfig:             cfg,
		DockerOCIImageConfigExt: dockerspec.DockerOCIImageConfigExt{Healthcheck: health},
	}}
}

// build runs buildCreate for a container made from an image with imgCfg, after
// adjust has shaped the container.
func build(t *testing.T, imgCfg ocispec.ImageConfig, adjust func(c *container.InspectResponse)) (*container.Config, *container.HostConfig) {
	t.Helper()
	old := dockertest.Running("api-1", "app:develop")
	old.ID = strings.Repeat("ab", 32)
	old.Image = oldImageID
	if adjust != nil {
		adjust(&old)
	}
	opts, _, err := buildCreate(old, imageWith(oldImageID, imgCfg, nil), imageWith(newImageID, ocispec.ImageConfig{}, nil))
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}
	if opts.Name != "api-1" {
		t.Errorf("Name = %q, want api-1", opts.Name)
	}
	if opts.Config.Image != "app:develop" {
		t.Errorf("Image = %q, want the original reference app:develop", opts.Config.Image)
	}
	return opts.Config, opts.HostConfig
}

func TestBuildCreateEnv(t *testing.T) {
	cfg, _ := build(t,
		ocispec.ImageConfig{Env: []string{"PATH=/usr/bin", "FOO=1"}},
		func(c *container.InspectResponse) { c.Config.Env = []string{"PATH=/usr/bin", "APP_ENV=prod", "FOO=1"} })
	if want := []string{"APP_ENV=prod"}; !reflect.DeepEqual(cfg.Env, want) {
		t.Errorf("Env = %v, want %v (what the image supplied must not be pinned)", cfg.Env, want)
	}

	// A variable the user overrode keeps the user's value.
	cfg, _ = build(t,
		ocispec.ImageConfig{Env: []string{"FOO=1"}},
		func(c *container.InspectResponse) { c.Config.Env = []string{"FOO=2"} })
	if want := []string{"FOO=2"}; !reflect.DeepEqual(cfg.Env, want) {
		t.Errorf("Env = %v, want %v", cfg.Env, want)
	}
}

func TestBuildCreateEntrypointCmd(t *testing.T) {
	img := ocispec.ImageConfig{Entrypoint: []string{"/entry.sh"}, Cmd: []string{"serve"}}

	cfg, _ := build(t, img, func(c *container.InspectResponse) {
		c.Config.Entrypoint, c.Config.Cmd = []string{"/entry.sh"}, []string{"serve"}
	})
	if cfg.Entrypoint != nil || cfg.Cmd != nil {
		t.Errorf("both inherited: Entrypoint = %v, Cmd = %v; want both cleared", cfg.Entrypoint, cfg.Cmd)
	}

	cfg, _ = build(t, img, func(c *container.InspectResponse) {
		c.Config.Entrypoint, c.Config.Cmd = []string{"/entry.sh"}, []string{"migrate"}
	})
	if cfg.Entrypoint != nil || !reflect.DeepEqual(cfg.Cmd, []string{"migrate"}) {
		t.Errorf("command overridden: Entrypoint = %v, Cmd = %v; want nil and [migrate]", cfg.Entrypoint, cfg.Cmd)
	}

	// With the entrypoint overridden Docker ignores the image's command, so the
	// command must be kept even though it looks inherited.
	cfg, _ = build(t, img, func(c *container.InspectResponse) {
		c.Config.Entrypoint, c.Config.Cmd = []string{"/custom.sh"}, []string{"serve"}
	})
	if !reflect.DeepEqual(cfg.Entrypoint, []string{"/custom.sh"}) || !reflect.DeepEqual(cfg.Cmd, []string{"serve"}) {
		t.Errorf("entrypoint overridden: Entrypoint = %v, Cmd = %v; want both kept", cfg.Entrypoint, cfg.Cmd)
	}
}

func TestBuildCreateInheritedScalars(t *testing.T) {
	health := &dockerspec.HealthcheckConfig{Test: []string{"CMD", "true"}, Interval: 5 * time.Second}
	old := dockertest.Running("api-1", "app:develop")
	old.ID = strings.Repeat("ab", 32)
	old.Image = oldImageID
	old.Config.WorkingDir, old.Config.User, old.Config.StopSignal = "/app", "node", "SIGTERM"
	old.Config.Healthcheck = &dockerspec.HealthcheckConfig{Test: []string{"CMD", "true"}, Interval: 5 * time.Second}
	old.Config.OnBuild = []string{"RUN something"}
	oldImg := imageWith(oldImageID, ocispec.ImageConfig{WorkingDir: "/app", User: "node", StopSignal: "SIGTERM"}, health)

	opts, _, err := buildCreate(old, oldImg, imageWith(newImageID, ocispec.ImageConfig{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	c := opts.Config
	if c.WorkingDir != "" || c.User != "" || c.StopSignal != "" || c.Healthcheck != nil || c.OnBuild != nil {
		t.Errorf("inherited values kept: dir=%q user=%q signal=%q health=%v onbuild=%v", c.WorkingDir, c.User, c.StopSignal, c.Healthcheck, c.OnBuild)
	}

	old.Config.WorkingDir, old.Config.User, old.Config.StopSignal = "/srv", "root", "SIGINT"
	old.Config.Healthcheck = &dockerspec.HealthcheckConfig{Test: []string{"CMD", "curl", "-f", "localhost"}}
	opts, _, _ = buildCreate(old, oldImg, imageWith(newImageID, ocispec.ImageConfig{}, nil))
	c = opts.Config
	if c.WorkingDir != "/srv" || c.User != "root" || c.StopSignal != "SIGINT" || c.Healthcheck == nil || c.Healthcheck.Test[1] != "curl" {
		t.Errorf("user-set values lost: dir=%q user=%q signal=%q health=%v", c.WorkingDir, c.User, c.StopSignal, c.Healthcheck)
	}
}

func TestBuildCreateLabels(t *testing.T) {
	cfg, _ := build(t,
		ocispec.ImageConfig{Labels: map[string]string{"org.opencontainers.image.version": "1.0", "maintainer": "ops"}},
		func(c *container.InspectResponse) {
			c.Config.Labels = map[string]string{
				"org.opencontainers.image.version": "1.0", // from the image
				"maintainer":                       "me",  // overridden by the user
				"traefik.enable":                   "true",
				"com.docker.compose.project":       "shop",
				"com.docker.compose.image":         oldImageID,
			}
		})
	want := map[string]string{
		"maintainer":                 "me",
		"traefik.enable":             "true",
		"com.docker.compose.project": "shop",
		"com.docker.compose.image":   newImageID,
	}
	if !reflect.DeepEqual(cfg.Labels, want) {
		t.Errorf("Labels = %v\nwant     %v", cfg.Labels, want)
	}

	// A container that Compose did not create gets no Compose label.
	cfg, _ = build(t, ocispec.ImageConfig{}, func(c *container.InspectResponse) {
		c.Config.Labels = map[string]string{"a": "b"}
	})
	if _, has := cfg.Labels["com.docker.compose.image"]; has {
		t.Error("a Compose label was added to a container without one")
	}
}

func TestBuildCreatePorts(t *testing.T) {
	p80, p443, p9000 := network.MustParsePort("80/tcp"), network.MustParsePort("443/tcp"), network.MustParsePort("9000/tcp")
	cfg, hc := build(t,
		ocispec.ImageConfig{ExposedPorts: map[string]struct{}{"80/tcp": {}, "443/tcp": {}}},
		func(c *container.InspectResponse) {
			c.Config.ExposedPorts = network.PortSet{p80: {}, p443: {}, p9000: {}}
			c.HostConfig.PortBindings = network.PortMap{p80: {{HostPort: "8080"}}}
		})
	want := network.PortSet{p80: {}, p9000: {}} // published, and exposed by the user
	if !reflect.DeepEqual(cfg.ExposedPorts, want) {
		t.Errorf("ExposedPorts = %v, want %v", cfg.ExposedPorts, want)
	}
	if got := hc.PortBindings[p80]; len(got) != 1 || got[0].HostPort != "8080" {
		t.Errorf("PortBindings = %v, want 80/tcp published on 8080", hc.PortBindings)
	}
}

func TestBuildCreateHostname(t *testing.T) {
	cfg, _ := build(t, ocispec.ImageConfig{}, func(c *container.InspectResponse) { c.Config.Hostname = c.ID[:12] })
	if cfg.Hostname != "" {
		t.Errorf("Hostname = %q, want the generated one cleared", cfg.Hostname)
	}
	cfg, _ = build(t, ocispec.ImageConfig{}, func(c *container.InspectResponse) { c.Config.Hostname = "api" })
	if cfg.Hostname != "api" {
		t.Errorf("Hostname = %q, want api kept", cfg.Hostname)
	}
}

func TestBuildCreateAnonymousVolume(t *testing.T) {
	cfg, hc := build(t,
		ocispec.ImageConfig{Volumes: map[string]struct{}{"/data": {}}},
		func(c *container.InspectResponse) {
			c.Config.Volumes = map[string]struct{}{"/data": {}}
			c.HostConfig.Binds = []string{"pgdata:/var/lib/postgresql", "/srv/conf:/etc/app:ro"}
			c.HostConfig.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: "uploads", Target: "/uploads"}}
			c.Mounts = []container.MountPoint{
				{Type: mount.TypeVolume, Name: "abc123", Destination: "/data", RW: true},               // anonymous
				{Type: mount.TypeVolume, Name: "ro456", Destination: "/cache", RW: false},              // anonymous, read-only
				{Type: mount.TypeVolume, Name: "pgdata", Destination: "/var/lib/postgresql", RW: true}, // already a bind
				{Type: mount.TypeVolume, Name: "uploads", Destination: "/uploads", RW: true},           // already a mount
				{Type: mount.TypeBind, Source: "/srv/conf", Destination: "/etc/app"},                   // a host path
			}
		})
	want := []string{"pgdata:/var/lib/postgresql", "/srv/conf:/etc/app:ro", "abc123:/data", "ro456:/cache:ro"}
	if !reflect.DeepEqual(hc.Binds, want) {
		t.Errorf("Binds = %v\nwant    %v", hc.Binds, want)
	}
	if len(cfg.Volumes) != 0 {
		t.Errorf("Volumes = %v, want the image's declaration dropped", cfg.Volumes)
	}
}

func TestBuildCreateNetworks(t *testing.T) {
	old := dockertest.Running("api-1", "app:develop")
	old.ID = strings.Repeat("ab", 32)
	old.Image = oldImageID
	short := old.ID[:12]
	old.HostConfig.NetworkMode = "shop_default"
	old.NetworkSettings.Networks = map[string]*network.EndpointSettings{
		"shop_default": {
			Aliases:    []string{"api", short},
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.20.0.10")},
			DriverOpts: map[string]string{"opt": "1"},
			NetworkID:  "net1", EndpointID: "ep1",
			Gateway: netip.MustParseAddr("172.20.0.1"), IPAddress: netip.MustParseAddr("172.20.0.10"), IPPrefixLen: 16,
			DNSNames: []string{"api-1", "api", short},
		},
		"monitoring": {
			Aliases:   []string{"api"},
			NetworkID: "net2", EndpointID: "ep2",
			Gateway: netip.MustParseAddr("172.21.0.1"), IPAddress: netip.MustParseAddr("172.21.0.7"), IPPrefixLen: 16,
		},
	}

	opts, extra, err := buildCreate(old, imageWith(oldImageID, ocispec.ImageConfig{}, nil), imageWith(newImageID, ocispec.ImageConfig{}, nil))
	if err != nil {
		t.Fatal(err)
	}

	primary := opts.NetworkingConfig.EndpointsConfig
	if len(primary) != 1 || primary["shop_default"] == nil {
		t.Fatalf("create call networks = %v, want only shop_default", primary)
	}
	wantPrimary := &network.EndpointSettings{
		Aliases:    []string{"api"},
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.20.0.10")},
		DriverOpts: map[string]string{"opt": "1"},
	}
	if !reflect.DeepEqual(primary["shop_default"], wantPrimary) {
		t.Errorf("shop_default = %+v\nwant           %+v", primary["shop_default"], wantPrimary)
	}
	if len(extra) != 1 || !reflect.DeepEqual(extra["monitoring"], &network.EndpointSettings{Aliases: []string{"api"}}) {
		t.Errorf("extra networks = %+v, want monitoring with only its alias", extra)
	}
}

func TestBuildCreateHostNetworkHasNoEndpoints(t *testing.T) {
	for _, mode := range []container.NetworkMode{"host", "none"} {
		old := dockertest.Running("api-1", "app:develop")
		old.ID = strings.Repeat("ab", 32)
		old.Image = oldImageID
		old.HostConfig.NetworkMode = mode
		old.NetworkSettings.Networks = map[string]*network.EndpointSettings{string(mode): {NetworkID: "x"}}
		opts, extra, err := buildCreate(old, imageWith(oldImageID, ocispec.ImageConfig{}, nil), imageWith(newImageID, ocispec.ImageConfig{}, nil))
		if err != nil {
			t.Fatal(err)
		}
		if opts.NetworkingConfig != nil && len(opts.NetworkingConfig.EndpointsConfig) != 0 || len(extra) != 0 {
			t.Errorf("mode %s: endpoints = %+v, extra = %+v; want none", mode, opts.NetworkingConfig, extra)
		}
	}
}

func TestBuildCreateDoesNotChangeItsInput(t *testing.T) {
	old := dockertest.Running("api-1", "app:develop")
	old.ID = strings.Repeat("ab", 32)
	old.Image = oldImageID
	old.Config.Env = []string{"PATH=/usr/bin", "A=1"}
	old.Config.Labels = map[string]string{"com.docker.compose.image": oldImageID}
	old.HostConfig.Binds = []string{"a:/a"}
	old.Mounts = []container.MountPoint{{Type: mount.TypeVolume, Name: "anon", Destination: "/data", RW: true}}

	if _, _, err := buildCreate(old, imageWith(oldImageID, ocispec.ImageConfig{Env: []string{"PATH=/usr/bin"}}, nil), imageWith(newImageID, ocispec.ImageConfig{}, nil)); err != nil {
		t.Fatal(err)
	}
	if len(old.Config.Env) != 2 || old.Config.Labels["com.docker.compose.image"] != oldImageID || len(old.HostConfig.Binds) != 1 {
		t.Errorf("buildCreate changed the container it was given: env=%v labels=%v binds=%v", old.Config.Env, old.Config.Labels, old.HostConfig.Binds)
	}
}

func TestRefuseReason(t *testing.T) {
	cases := []struct {
		adjust   func(c *container.InspectResponse)
		contains string
	}{
		{func(c *container.InspectResponse) { c.HostConfig.NetworkMode = "container:abc" }, "another container's network"},
		{func(c *container.InspectResponse) { c.HostConfig.VolumesFrom = []string{"data"} }, "volumes from another container"},
		{func(c *container.InspectResponse) { c.HostConfig.Links = []string{"/db:/api/db"} }, "links"},
		{func(c *container.InspectResponse) { c.HostConfig.AutoRemove = true }, "--rm"},
	}
	for _, c := range cases {
		old := dockertest.Running("api-1", "app:develop")
		c.adjust(&old)
		if got := refuseReason(old); !strings.Contains(got, c.contains) {
			t.Errorf("refuseReason = %q, want it to mention %q", got, c.contains)
		}
	}
	if got := refuseReason(dockertest.Running("api-1", "app:develop")); got != "" {
		t.Errorf("refuseReason for a plain container = %q, want none", got)
	}
}

// ---- Replace against the fake daemon ----

type replaceRig struct {
	f    *dockertest.Fake
	env  Env
	id   string // the container to replace
	temp string // the name it is parked under while being replaced
	rec  *recorder
}

// newRig puts api-1 on a fake host, created from an old image whose tag now
// points to a new one, as after a pull.
func newRig(t *testing.T, adjust func(c *container.InspectResponse)) *replaceRig {
	t.Helper()
	f := dockertest.New()
	f.AddImage("app:develop", imageWith(oldImageID, ocispec.ImageConfig{Env: []string{"PATH=/usr/bin"}}, nil))
	f.AddImage("app:develop", imageWith(newImageID, ocispec.ImageConfig{Env: []string{"PATH=/usr/local/bin"}}, nil))

	c := dockertest.Running("api-1", "app:develop")
	c.Image = oldImageID
	c.Config.Env = []string{"PATH=/usr/bin", "APP_ENV=prod"}
	if adjust != nil {
		adjust(&c)
	}
	id := f.AddContainer(c)
	return &replaceRig{
		f:    f,
		env:  Env{Docker: f, StartGrace: 20 * time.Millisecond, Poll: time.Millisecond, HealthTimeout: 80 * time.Millisecond},
		id:   id,
		temp: "api-1-old-" + id[:8],
		rec:  &recorder{},
	}
}

func (r *replaceRig) replace() (bool, error) {
	return Replace(ctx, r.env, r.id, r.rec.report)
}

func isNew(c *container.InspectResponse) bool { return c.Image == newImageID }

func TestReplaceHappyPath(t *testing.T) {
	r := newRig(t, nil)
	replaced, err := r.replace()
	if err != nil || !replaced {
		t.Fatalf("Replace = %v, %v; want true, nil", replaced, err)
	}
	want := []string{
		"ContainerStop api-1",
		"ContainerRename api-1->" + r.temp,
		"ContainerCreate api-1",
		"ContainerStart api-1",
		"ContainerRemove " + r.temp,
	}
	if got := r.f.CallLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q\nwant    %q", got, want)
	}
	c, ok := r.f.Container("api-1")
	if !ok || c.Image != newImageID || !c.State.Running {
		t.Fatalf("api-1 after replace = %+v, want it running on the new image", c)
	}
	if want := []string{"APP_ENV=prod"}; !reflect.DeepEqual(c.Config.Env, want) {
		t.Errorf("env passed to the new container = %v, want %v", c.Config.Env, want)
	}
	if _, still := r.f.Container(r.temp); still {
		t.Error("the old container was not removed")
	}
}

func TestReplaceAlreadyCurrent(t *testing.T) {
	r := newRig(t, func(c *container.InspectResponse) { c.Image = newImageID })
	replaced, err := r.replace()
	if err != nil || replaced {
		t.Errorf("Replace = %v, %v; want false, nil", replaced, err)
	}
	if calls := r.f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
}

// rolledBack asserts the old container is back under its name, running, on the
// old image, and nothing else is left behind.
func (r *replaceRig) rolledBack(t *testing.T, err error, wantTail []string) {
	t.Helper()
	if err == nil {
		t.Fatal("Replace returned nil, want the failure")
	}
	calls := r.f.CallLog()
	if len(calls) < len(wantTail) || !reflect.DeepEqual(calls[len(calls)-len(wantTail):], wantTail) {
		t.Errorf("calls = %q\nwant them to end with %q", calls, wantTail)
	}
	c, ok := r.f.Container("api-1")
	if !ok || c.ID != r.id || c.Image != oldImageID || !c.State.Running {
		t.Errorf("api-1 after rollback = id %.8s image %s running %v; want the original, running", c.ID, c.Image, c.State != nil && c.State.Running)
	}
	if _, parked := r.f.Container(r.temp); parked {
		t.Errorf("a container is still parked as %s", r.temp)
	}
}

func TestReplaceStartFails(t *testing.T) {
	r := newRig(t, nil)
	boom := errors.New("driver failed programming external connectivity: port is already allocated")
	r.f.Before = func(method string, c *container.InspectResponse) error {
		if method == "ContainerStart" && isNew(c) {
			return boom
		}
		return nil
	}
	_, err := r.replace()
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the start error", err)
	}
	r.rolledBack(t, err, []string{
		"ContainerRemove api-1",
		"ContainerRename " + r.temp + "->api-1",
		"ContainerStart api-1",
	})
}

func TestReplaceCreateFails(t *testing.T) {
	r := newRig(t, nil)
	r.f.Fail("ContainerCreate", errors.New("invalid mount config"))
	_, err := r.replace()
	r.rolledBack(t, err, []string{
		"ContainerCreate api-1",
		"ContainerRename " + r.temp + "->api-1",
		"ContainerStart api-1",
	})
}

func TestReplaceExitsDuringGrace(t *testing.T) {
	r := newRig(t, nil)
	r.f.After = func(method string, c *container.InspectResponse) {
		if method == "ContainerStart" && isNew(c) {
			c.State.Running, c.State.Status, c.State.ExitCode = false, container.StateExited, 2
		}
	}
	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "exit code 2") {
		t.Errorf("err = %v, want it to give the exit code", err)
	}
	r.rolledBack(t, err, []string{
		"ContainerRemove api-1",
		"ContainerRename " + r.temp + "->api-1",
		"ContainerStart api-1",
	})
}

func TestReplaceKeepsRestarting(t *testing.T) {
	r := newRig(t, nil)
	r.f.OnInspect = func(c *container.InspectResponse) {
		if isNew(c) && c.State.Running {
			c.RestartCount = 2
		}
	}
	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Errorf("err = %v, want it to say the container keeps restarting", err)
	}
	r.rolledBack(t, err, []string{"ContainerStart api-1"})
}

func TestReplaceUnhealthy(t *testing.T) {
	r := newRig(t, nil)
	r.f.OnInspect = func(c *container.InspectResponse) {
		if isNew(c) && c.State.Running {
			c.State.Health = &container.Health{Status: container.Unhealthy}
		}
	}
	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Errorf("err = %v, want it to say unhealthy", err)
	}
	r.rolledBack(t, err, []string{
		"ContainerRemove api-1",
		"ContainerRename " + r.temp + "->api-1",
		"ContainerStart api-1",
	})
}

func TestReplaceWaitsForHealthy(t *testing.T) {
	r := newRig(t, nil)
	polls := 0
	r.f.OnInspect = func(c *container.InspectResponse) {
		if isNew(c) && c.State.Running {
			polls++
			status := container.Starting
			if polls > 3 {
				status = container.Healthy
			}
			c.State.Health = &container.Health{Status: status}
		}
	}
	replaced, err := r.replace()
	if err != nil || !replaced {
		t.Fatalf("Replace = %v, %v", replaced, err)
	}
	if polls <= 3 {
		t.Errorf("returned after %d polls, before the container was healthy", polls)
	}
}

func TestReplaceStillStarting(t *testing.T) {
	r := newRig(t, nil)
	r.f.OnInspect = func(c *container.InspectResponse) {
		if isNew(c) && c.State.Running {
			c.State.Health = &container.Health{Status: container.Starting}
		}
	}
	replaced, err := r.replace()
	// A slow start is not rolled back: the container was replaced and kept.
	// But its health was never confirmed, and the caller is told so, so that
	// it does not go on to replace the rest of the service on trust.
	if !replaced || !errors.Is(err, ErrStillStarting) {
		t.Fatalf("Replace = %v, %v; want true with ErrStillStarting", replaced, err)
	}
	if c, _ := r.f.Container("api-1"); c.Image != newImageID || !c.State.Running {
		t.Errorf("api-1 = image %s running %v, want the new image kept running", c.Image, c.State.Running)
	}
	if _, parked := r.f.Container(r.temp); parked {
		t.Error("the old container was left parked")
	}
	warned := slices.ContainsFunc(r.rec.events, func(e Event) bool {
		return e.Level == LevelWarning && strings.Contains(e.Step, "still starting")
	})
	if !warned {
		t.Errorf("events = %q, want a warning that it is still starting", r.rec.steps())
	}
}

func TestReplaceStoppedContainer(t *testing.T) {
	r := newRig(t, func(c *container.InspectResponse) {
		c.State = &container.State{Status: container.StateExited, ExitCode: 0}
	})
	replaced, err := r.replace()
	if err != nil || !replaced {
		t.Fatalf("Replace = %v, %v", replaced, err)
	}
	want := []string{
		"ContainerRename api-1->" + r.temp,
		"ContainerCreate api-1",
		"ContainerRemove " + r.temp,
	}
	if got := r.f.CallLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q\nwant    %q (a stopped container stays stopped)", got, want)
	}
	if c, _ := r.f.Container("api-1"); c.State.Running || c.Image != newImageID {
		t.Errorf("api-1 = running %v on %s; want stopped on the new image", c.State.Running, c.Image)
	}
}

func TestReplaceRefused(t *testing.T) {
	r := newRig(t, func(c *container.InspectResponse) { c.HostConfig.NetworkMode = "container:abc" })
	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "another container's network") {
		t.Errorf("err = %v, want the reason it cannot be replaced", err)
	}
	if calls := r.f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
}

// Replacing a container gives it a new ID. Another container that joined its
// network by ID would be cut off, so that is refused too.
func TestReplaceRefusedWhenAnotherContainerDependsOnIt(t *testing.T) {
	r := newRig(t, nil)
	sidecar := dockertest.Running("metrics-sidecar", "sidecar:1")
	sidecar.HostConfig.NetworkMode = container.NetworkMode("container:" + r.id)
	r.f.AddContainer(sidecar)

	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "metrics-sidecar") {
		t.Errorf("err = %v, want it to name the dependent container", err)
	}
	if calls := r.f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
}

func TestReplaceOldImageGone(t *testing.T) {
	r := newRig(t, func(c *container.InspectResponse) {
		c.Image = "sha256:00000000000000000000000000000000000000000000000000000000000000ff"
	})
	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "no longer on the host") {
		t.Errorf("err = %v, want it to explain the old image is gone", err)
	}
	if calls := r.f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
}

func TestReplaceExtraNetworks(t *testing.T) {
	r := newRig(t, func(c *container.InspectResponse) {
		c.HostConfig.NetworkMode = "shop_default"
		c.NetworkSettings.Networks = map[string]*network.EndpointSettings{
			"shop_default": {Aliases: []string{"api"}},
			"monitoring":   {Aliases: []string{"api"}},
		}
	})
	if _, err := r.replace(); err != nil {
		t.Fatal(err)
	}
	calls := r.f.CallLog()
	connect := slices.Index(calls, "NetworkConnect monitoring api-1")
	start := slices.Index(calls, "ContainerStart api-1")
	if connect < 0 || start < 0 || connect > start {
		t.Errorf("calls = %q, want NetworkConnect monitoring before ContainerStart", calls)
	}
	c, _ := r.f.Container("api-1")
	if len(c.NetworkSettings.Networks) != 2 {
		t.Errorf("networks on the new container = %v, want both", c.NetworkSettings.Networks)
	}
}

func TestReplaceOldContainerCannotBeRemoved(t *testing.T) {
	r := newRig(t, nil)
	r.f.Before = func(method string, c *container.InspectResponse) error {
		if method == "ContainerRemove" && !isNew(c) {
			return errors.New("device or resource busy")
		}
		return nil
	}
	replaced, err := r.replace()
	if err != nil || !replaced {
		t.Fatalf("Replace = %v, %v; the new container is up, so this is a warning, not a failure", replaced, err)
	}
	warned := slices.ContainsFunc(r.rec.events, func(e Event) bool {
		return e.Level == LevelWarning && strings.Contains(e.Step, r.temp)
	})
	if !warned {
		t.Errorf("events = %q, want a warning naming the leftover container", r.rec.steps())
	}
}

// Compose declares a service's anonymous volumes through the mount API, with
// no source. Copied as it is, such a mount makes Docker create a new, empty
// volume: the service would come back without its data.
func TestBuildCreateAnonymousMount(t *testing.T) {
	_, hc := build(t, ocispec.ImageConfig{}, func(c *container.InspectResponse) {
		c.HostConfig.Mounts = []mount.Mount{
			{Type: mount.TypeVolume, Target: "/var/lib/mysql"},              // anonymous: no source
			{Type: mount.TypeVolume, Source: "uploads", Target: "/uploads"}, // named
			{Type: mount.TypeBind, Source: "/srv/conf", Target: "/etc/app"}, // a host path
			{Type: mount.TypeTmpfs, Target: "/tmp"},                         // not a volume
		}
		c.Mounts = []container.MountPoint{
			{Type: mount.TypeVolume, Name: "f3a9c1anon", Destination: "/var/lib/mysql", RW: true},
			{Type: mount.TypeVolume, Name: "uploads", Destination: "/uploads", RW: true},
			{Type: mount.TypeBind, Source: "/srv/conf", Destination: "/etc/app"},
		}
	})
	want := []mount.Mount{
		{Type: mount.TypeVolume, Source: "f3a9c1anon", Target: "/var/lib/mysql"},
		{Type: mount.TypeVolume, Source: "uploads", Target: "/uploads"},
		{Type: mount.TypeBind, Source: "/srv/conf", Target: "/etc/app"},
		{Type: mount.TypeTmpfs, Target: "/tmp"},
	}
	if !reflect.DeepEqual(hc.Mounts, want) {
		t.Errorf("Mounts = %+v\nwant     %+v", hc.Mounts, want)
	}
	if len(hc.Binds) != 0 {
		t.Errorf("Binds = %v, want none: the volume is already carried by its mount", hc.Binds)
	}
}

// A container run for another platform (amd64 on an arm64 host) must stay on
// that platform, not quietly become native.
func TestBuildCreatePlatform(t *testing.T) {
	old := dockertest.Running("api-1", "app:develop")
	old.ID = strings.Repeat("ab", 32)
	old.Image = oldImageID
	oldImg := imageWith(oldImageID, ocispec.ImageConfig{}, nil)
	oldImg.Os, oldImg.Architecture, oldImg.Variant = "linux", "arm", "v7"

	opts, _, err := buildCreate(old, oldImg, imageWith(newImageID, ocispec.ImageConfig{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if p := opts.Platform; p == nil || p.OS != "linux" || p.Architecture != "arm" || p.Variant != "v7" {
		t.Errorf("Platform = %+v, want linux/arm/v7 as the old image", opts.Platform)
	}

	// An image that does not say what it is for leaves the choice to Docker.
	opts, _, _ = buildCreate(old, imageWith(oldImageID, ocispec.ImageConfig{}, nil), imageWith(newImageID, ocispec.ImageConfig{}, nil))
	if opts.Platform != nil {
		t.Errorf("Platform = %+v, want none when the old image has no platform recorded", opts.Platform)
	}
}

// The wait for "healthy" must be at least as long as the container's own
// health schedule needs to reach a verdict, or a failing check would be
// mistaken for a slow start.
func TestHealthBudget(t *testing.T) {
	floor := 2 * time.Minute
	cases := []struct {
		name string
		hc   *dockerspec.HealthcheckConfig
		want time.Duration
	}{
		{"no health check", nil, floor},
		{"Docker's defaults: 3 tries of 30s+30s", &dockerspec.HealthcheckConfig{Test: []string{"CMD", "true"}}, 185 * time.Second},
		{"a quick check stays at the floor", &dockerspec.HealthcheckConfig{Interval: 2 * time.Second, Timeout: time.Second, Retries: 3}, floor},
		{"a long start period is added", &dockerspec.HealthcheckConfig{StartPeriod: 5 * time.Minute, Interval: 10 * time.Second, Timeout: 5 * time.Second, Retries: 2}, 5*time.Minute + 35*time.Second},
		{"never beyond the cap", &dockerspec.HealthcheckConfig{StartPeriod: time.Hour}, maxHealthWait},
	}
	for _, c := range cases {
		if got := healthBudget(c.hc, floor); got != c.want {
			t.Errorf("%s: healthBudget = %v, want %v", c.name, got, c.want)
		}
	}
}

// Replacing the panel's own container would stop the panel halfway through:
// nothing would be left to create the replacement.
func TestReplaceRefusesThePanelItself(t *testing.T) {
	r := newRig(t, nil)
	r.env.SelfContainerID = r.id[:12] // as a container knows itself: by its short ID
	_, err := r.replace()
	if err == nil || !strings.Contains(err.Error(), "the panel's own container") {
		t.Errorf("err = %v, want a refusal explaining it is the panel itself", err)
	}
	if calls := r.f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
	// Another container is not affected by the setting.
	r2 := newRig(t, nil)
	r2.env.SelfContainerID = strings.Repeat("9", 12)
	if replaced, err := r2.replace(); err != nil || !replaced {
		t.Errorf("another container: Replace = %v, %v", replaced, err)
	}
}
