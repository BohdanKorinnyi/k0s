package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// The label in which Docker Compose records the image a container was made
// from. Compose replaces a container whose label no longer matches the
// service's image, so it must follow the container to its new image.
const composeImageLabel = "com.docker.compose.image"

// Defaults for Env fields left at zero.
// ErrStillStarting is returned by Replace, together with true, when the new
// container was kept but its health check had not passed by the time limit.
// The container is not rolled back — it may only be slow — but nothing has
// vouched for it, and a caller replacing several containers should stop.
var ErrStillStarting = errors.New("its health check has not passed yet")

// maxHealthWait bounds how long a single replacement is waited for, whatever
// its health schedule says.
const maxHealthWait = 10 * time.Minute

const (
	defaultHealthTimeout = 2 * time.Minute
	defaultStartGrace    = 3 * time.Second
	defaultPoll          = 500 * time.Millisecond
	rollbackTimeout      = time.Minute
)

// Replace swaps one container for a new one made from the image its reference
// points to now, keeping its name and everything the user configured. It
// reports false when the container already runs that image, and true with
// ErrStillStarting when the replacement was kept without a passed health check.
//
// The old container is parked under another name until the new one is up. If
// the new one cannot be created, fails to start, or turns unhealthy, it is
// removed and the old one is put back and started again.
func Replace(ctx context.Context, env Env, containerID string, report Progress) (bool, error) {
	cli := env.Docker
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	old := inspected.Container
	name := strings.TrimPrefix(old.Name, "/")
	if old.Config == nil || old.HostConfig == nil || old.State == nil {
		return false, fmt.Errorf("%s: Docker returned an incomplete description of the container", name)
	}

	if err := refuseSelf(env, old.ID, name); err != nil {
		return false, err
	}
	if reason := refuseReason(old); reason != "" {
		return false, fmt.Errorf("%s cannot be replaced automatically: %s", name, reason)
	}
	if dependent, err := dependentOn(ctx, env, old); err != nil {
		return false, err
	} else if dependent != "" {
		return false, fmt.Errorf("%s cannot be replaced automatically: container %s uses its network, and would be cut off; recreate them together with Docker Compose", name, dependent)
	}

	newImg, err := cli.ImageInspect(ctx, old.Config.Image)
	if err != nil {
		return false, fmt.Errorf("read image %s: %w", old.Config.Image, err)
	}
	if newImg.ID == old.Image {
		return false, nil
	}
	oldImg, err := cli.ImageInspect(ctx, old.Image)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, fmt.Errorf("%s cannot be replaced automatically: the image it was created from is no longer on the host, so its own settings cannot be told apart from that image's defaults", name)
		}
		return false, fmt.Errorf("read the image %s was created from: %w", name, err)
	}

	opts, extraNetworks, err := buildCreate(old, oldImg.InspectResponse, newImg.InspectResponse)
	if err != nil {
		return false, err
	}

	wasUp := old.State.Running || old.State.Restarting || old.State.Paused
	parked := name + "-old-" + old.ID[:min(8, len(old.ID))]

	if wasUp {
		report(Event{Step: "Stopping " + name, Level: LevelInfo})
		if _, err := cli.ContainerStop(ctx, old.ID, client.ContainerStopOptions{}); err != nil {
			return false, fmt.Errorf("stop %s: %w", name, err)
		}
	}
	if _, err := cli.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: parked}); err != nil {
		err = fmt.Errorf("set %s aside: %w", name, err)
		if wasUp {
			if _, startErr := cli.ContainerStart(ctx, old.ID, client.ContainerStartOptions{}); startErr != nil {
				err = fmt.Errorf("%w; starting it again also failed: %v", err, startErr)
			}
		}
		return false, err
	}

	// From here on the old container is parked: any failure must put it back.
	newID := ""
	rollback := func(cause error) error {
		report(Event{Step: "Putting the previous " + name + " back", Level: LevelWarning})
		// The caller's context may be the reason we are here; use our own.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()

		var problems []string
		if newID != "" {
			if _, err := cli.ContainerRemove(rctx, newID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
				problems = append(problems, "remove the new container: "+err.Error())
			}
		}
		if _, err := cli.ContainerRename(rctx, old.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
			problems = append(problems, fmt.Sprintf("rename %s back: %v", parked, err))
		} else if wasUp {
			if _, err := cli.ContainerStart(rctx, old.ID, client.ContainerStartOptions{}); err != nil {
				problems = append(problems, "start it again: "+err.Error())
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("%w; and restoring the previous container failed (%s) — it may be left as %s", cause, strings.Join(problems, "; "), parked)
		}
		return cause
	}

	report(Event{Step: "Creating " + name + " from the new image", Level: LevelInfo})
	created, err := cli.ContainerCreate(ctx, opts)
	if err != nil {
		return false, rollback(fmt.Errorf("create %s: %w", name, err))
	}
	newID = created.ID

	for _, netName := range slices.Sorted(maps.Keys(extraNetworks)) {
		if _, err := cli.NetworkConnect(ctx, netName, client.NetworkConnectOptions{Container: newID, EndpointConfig: extraNetworks[netName]}); err != nil {
			return false, rollback(fmt.Errorf("connect %s to network %s: %w", name, netName, err))
		}
	}

	confirmed := true
	if wasUp {
		report(Event{Step: "Starting " + name, Level: LevelInfo})
		if _, err := cli.ContainerStart(ctx, newID, client.ContainerStartOptions{}); err != nil {
			return false, rollback(fmt.Errorf("start %s: %w", name, err))
		}
		confirmed, err = waitUntilUp(ctx, env, newID, name, report)
		if err != nil {
			return false, rollback(err)
		}
	}

	// Volumes are kept: the anonymous ones now belong to the new container.
	if _, err := cli.ContainerRemove(ctx, old.ID, client.ContainerRemoveOptions{}); err != nil {
		report(Event{Step: fmt.Sprintf("%s is replaced, but the previous container could not be removed and is left as %s: %v", name, parked, err), Level: LevelWarning})
	} else {
		report(Event{Step: "Replaced " + name, Level: LevelInfo})
	}
	if !confirmed {
		return true, ErrStillStarting
	}
	return true, nil
}

// healthBudget is how long to wait for a container's health check to reach a
// verdict: at least floor, and at least as long as the container's own
// schedule needs to declare it unhealthy (start period, then the retries).
// Shorter than that, a check that is failing would be taken for a slow start.
func healthBudget(hc *container.HealthConfig, floor time.Duration) time.Duration {
	if hc == nil {
		return floor
	}
	// Docker's defaults for whatever the container does not set.
	interval, timeout, retries := hc.Interval, hc.Timeout, hc.Retries
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if retries <= 0 {
		retries = 3
	}
	needed := hc.StartPeriod + time.Duration(retries)*(interval+timeout) + 5*time.Second
	return min(max(floor, needed), maxHealthWait)
}

// refuseSelf returns an error when the container is the one the panel itself
// runs in. Replacing it would stop the panel between "stop the old one" and
// "start the new one", with nothing left to finish the job.
func refuseSelf(env Env, containerID, name string) error {
	if env.SelfContainerID == "" || !strings.HasPrefix(containerID, env.SelfContainerID) {
		return nil
	}
	return fmt.Errorf("%s is the panel's own container: stopping it would stop the panel halfway through the replacement. Update the panel from outside instead (run its new image, or `docker compose up -d --build`)", name)
}

// waitUntilUp watches a just-started container until it can be trusted: it
// stays running for a moment, or its health check passes. It reports false,
// without an error, when the health check was still undecided at the limit.
func waitUntilUp(ctx context.Context, env Env, id, name string, report Progress) (bool, error) {
	healthTimeout, grace, poll := env.HealthTimeout, env.StartGrace, env.Poll
	if healthTimeout <= 0 {
		healthTimeout = defaultHealthTimeout
	}
	if grace <= 0 {
		grace = defaultStartGrace
	}
	if poll <= 0 {
		poll = defaultPoll
	}

	started := time.Now()
	announced := false
	for {
		res, err := env.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return false, fmt.Errorf("check %s after starting it: %w", name, err)
		}
		c := res.Container
		if c.State == nil {
			return false, fmt.Errorf("check %s after starting it: no state reported", name)
		}
		if !c.State.Running {
			detail := fmt.Sprintf("exit code %d", c.State.ExitCode)
			if c.State.Error != "" {
				detail += ": " + c.State.Error
			}
			return false, fmt.Errorf("the new %s stopped right after starting (%s)", name, detail)
		}
		if c.State.Restarting || c.RestartCount > 0 {
			return false, fmt.Errorf("the new %s keeps crashing and being restarted", name)
		}
		limit := healthTimeout
		if c.Config != nil {
			limit = healthBudget(c.Config.Healthcheck, healthTimeout)
		}

		elapsed := time.Since(started)
		health := container.NoHealthcheck
		if c.State.Health != nil && c.State.Health.Status != "" {
			health = c.State.Health.Status
		}
		switch health {
		case container.NoHealthcheck:
			if elapsed >= grace {
				return true, nil
			}
		case container.Healthy:
			return true, nil
		case container.Unhealthy:
			return false, fmt.Errorf("the new %s became unhealthy", name)
		default: // starting
			if !announced {
				report(Event{Step: "Waiting for " + name + " to become healthy", Level: LevelInfo})
				announced = true
			}
			if elapsed >= limit {
				report(Event{Step: fmt.Sprintf("%s is still starting after %s; left running on the new image", name, limit.Round(time.Second)), Level: LevelWarning})
				return false, nil
			}
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// refuseReason says why a container cannot be reproduced faithfully, or "".
// Refusing with a reason is better than producing a container that silently
// differs.
func refuseReason(old container.InspectResponse) string {
	hc := old.HostConfig
	if hc == nil {
		return ""
	}
	switch {
	case hc.NetworkMode.IsContainer():
		return "it shares another container's network, and that link is tied to that container's ID"
	case len(hc.VolumesFrom) > 0:
		return "it takes volumes from another container"
	case len(hc.Links) > 0:
		return "it uses legacy container links"
	case hc.AutoRemove:
		return "it was started with --rm, so it would vanish when stopped and could not be put back"
	}
	return ""
}

// dependentOn returns the name of a container that has joined old's network
// by ID. Replacing old would give it a new ID and strand that container.
func dependentOn(ctx context.Context, env Env, old container.InspectResponse) (string, error) {
	listed, err := env.Docker.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return "", err
	}
	oldName := strings.TrimPrefix(old.Name, "/")
	for _, item := range listed.Items {
		target, ok := strings.CutPrefix(item.HostConfig.NetworkMode, "container:")
		if !ok || target == "" || item.ID == old.ID {
			continue
		}
		if target == old.ID || target == oldName || (len(target) >= 12 && strings.HasPrefix(old.ID, target)) {
			if len(item.Names) > 0 {
				return strings.TrimPrefix(item.Names[0], "/"), nil
			}
			return item.ID[:min(12, len(item.ID))], nil
		}
	}
	return "", nil
}

// buildCreate works out the settings for the replacement container.
//
// What Docker reports for a container mixes two things: what the user set, and
// what the image supplied by default. Only the first may be carried over. If
// the image's defaults were copied too, the new image's command, environment
// and labels would never take effect — the container would run the new files
// with the old image's configuration.
//
// The second return value holds the networks beyond the first, which have to
// be connected after creation.
func buildCreate(old container.InspectResponse, oldImg, newImg image.InspectResponse) (client.ContainerCreateOptions, map[string]*network.EndpointSettings, error) {
	if old.Config == nil || old.HostConfig == nil {
		return client.ContainerCreateOptions{}, nil, errors.New("incomplete container description")
	}
	cfg, err := deepCopy(old.Config)
	if err != nil {
		return client.ContainerCreateOptions{}, nil, err
	}
	hc, err := deepCopy(old.HostConfig)
	if err != nil {
		return client.ContainerCreateOptions{}, nil, err
	}
	img := &dockerspec.DockerOCIImageConfig{}
	if oldImg.Config != nil {
		img = oldImg.Config
	}

	// Environment: drop entries the image supplied unchanged.
	cfg.Env = slices.DeleteFunc(cfg.Env, func(e string) bool { return slices.Contains(img.Env, e) })
	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}

	// Entrypoint and command. When the entrypoint was overridden Docker does
	// not fall back to the image's command, so the command must stay as it is
	// even if it happens to equal the image's.
	if slices.Equal(cfg.Entrypoint, img.Entrypoint) {
		cfg.Entrypoint = nil
		if slices.Equal(cfg.Cmd, img.Cmd) {
			cfg.Cmd = nil
		}
	}

	if cfg.WorkingDir == img.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.User == img.User {
		cfg.User = ""
	}
	if cfg.StopSignal == img.StopSignal {
		cfg.StopSignal = ""
	}
	if reflect.DeepEqual(cfg.Healthcheck, img.Healthcheck) {
		cfg.Healthcheck = nil
	}
	if slices.Equal(cfg.Shell, img.Shell) {
		cfg.Shell = nil
	}
	cfg.OnBuild = nil // only ever comes from an image

	// Labels: drop the image's, and point Compose's record at the new image.
	maps.DeleteFunc(cfg.Labels, func(k, v string) bool {
		imgValue, fromImage := img.Labels[k]
		return fromImage && imgValue == v
	})
	if _, compose := cfg.Labels[composeImageLabel]; compose {
		cfg.Labels[composeImageLabel] = newImg.ID
	}

	// Exposed ports: drop the image's, but anything published must stay.
	maps.DeleteFunc(cfg.ExposedPorts, func(p network.Port, _ struct{}) bool {
		_, fromImage := img.ExposedPorts[p.String()]
		_, published := hc.PortBindings[p]
		return fromImage && !published
	})
	for p := range hc.PortBindings {
		if cfg.ExposedPorts == nil {
			cfg.ExposedPorts = network.PortSet{}
		}
		cfg.ExposedPorts[p] = struct{}{}
	}
	if len(cfg.ExposedPorts) == 0 {
		cfg.ExposedPorts = nil
	}

	// Volume declarations from the image are re-supplied by the new image.
	maps.DeleteFunc(cfg.Volumes, func(path string, _ struct{}) bool {
		_, fromImage := img.Volumes[path]
		return fromImage
	})
	if len(cfg.Volumes) == 0 {
		cfg.Volumes = nil
	}

	// A hostname equal to the short ID was generated, not chosen.
	if len(old.ID) >= 12 && cfg.Hostname == old.ID[:12] {
		cfg.Hostname = ""
	}

	// Anonymous volumes have no name in the settings, only in the running
	// container. Left that way the new container would be given fresh, empty
	// ones — the service would come back without its data. Name them.
	//
	// First the ones declared through the mount API with no source, which is
	// how Compose declares a service's anonymous volumes: give each mount the
	// name of the volume the old container actually has there.
	inUse := map[string]string{}
	for _, m := range old.Mounts {
		if m.Type == mount.TypeVolume && m.Name != "" {
			inUse[m.Destination] = m.Name
		}
	}
	for i := range hc.Mounts {
		if m := &hc.Mounts[i]; m.Type == mount.TypeVolume && m.Source == "" {
			m.Source = inUse[m.Target]
		}
	}
	// Then the ones that appear in no setting at all (the image's VOLUME
	// lines, or `-v /path`): bind them by name.
	for _, m := range old.Mounts {
		if m.Type != mount.TypeVolume || m.Name == "" || mountConfigured(hc, m.Destination) {
			continue
		}
		bind := m.Name + ":" + m.Destination
		if !m.RW {
			bind += ":ro"
		}
		hc.Binds = append(hc.Binds, bind)
	}

	primary, extra := endpoints(old)
	opts := client.ContainerCreateOptions{
		Name:       strings.TrimPrefix(old.Name, "/"),
		Config:     cfg,
		HostConfig: hc,
	}
	if len(primary) > 0 {
		opts.NetworkingConfig = &network.NetworkingConfig{EndpointsConfig: primary}
	}
	// Stay on the platform the container was running, e.g. amd64 under
	// emulation on an arm64 host; otherwise Docker would pick the native one.
	opts.Platform = platformOf(oldImg)
	return opts, extra, nil
}

// platformOf returns the platform an image was built for, or nil when the
// image does not say.
func platformOf(img image.InspectResponse) *ocispec.Platform {
	if img.Os == "" || img.Architecture == "" {
		return nil
	}
	return &ocispec.Platform{OS: img.Os, Architecture: img.Architecture, Variant: img.Variant}
}

// mountConfigured reports whether the settings already say what is mounted at
// the destination, as a bind ("source:destination[:options]") or a mount.
func mountConfigured(hc *container.HostConfig, destination string) bool {
	for _, b := range hc.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 && parts[1] == destination {
			return true
		}
	}
	for _, m := range hc.Mounts {
		if m.Target == destination {
			return true
		}
	}
	return false
}

// endpoints splits a container's networks into the one to create it on and
// the rest. Only what the user chose is kept for each: fixed addresses,
// aliases, links and driver options. Addresses, gateways and IDs that Docker
// assigned are left for Docker to assign again.
func endpoints(old container.InspectResponse) (primary, extra map[string]*network.EndpointSettings) {
	mode := old.HostConfig.NetworkMode
	if mode.IsHost() || mode.IsNone() || mode.IsContainer() {
		return nil, nil
	}
	if old.NetworkSettings == nil || len(old.NetworkSettings.Networks) == 0 {
		return nil, nil
	}
	nets := old.NetworkSettings.Networks
	names := slices.Sorted(maps.Keys(nets))

	primaryName := ""
	if mode.IsUserDefined() {
		want := mode.UserDefined()
		if _, ok := nets[want]; ok {
			primaryName = want
		} else {
			for _, n := range names {
				if nets[n] != nil && nets[n].NetworkID == want {
					primaryName = n
					break
				}
			}
		}
	} else if _, ok := nets["bridge"]; ok {
		primaryName = "bridge"
	}
	if primaryName == "" {
		primaryName = names[0]
	}

	shortID := ""
	if len(old.ID) >= 12 {
		shortID = old.ID[:12]
	}
	for _, n := range names {
		ep := nets[n]
		if ep == nil {
			ep = &network.EndpointSettings{}
		}
		clean := &network.EndpointSettings{GwPriority: ep.GwPriority}
		if ep.IPAMConfig != nil {
			clean.IPAMConfig = ep.IPAMConfig.Copy()
		}
		if len(ep.Links) > 0 {
			clean.Links = slices.Clone(ep.Links)
		}
		if len(ep.DriverOpts) > 0 {
			clean.DriverOpts = maps.Clone(ep.DriverOpts)
		}
		// Aliases exist only on user-defined networks. Docker adds the short
		// ID itself; carrying the old one over would leave a stale name.
		if n != "bridge" {
			for _, a := range ep.Aliases {
				if a != shortID {
					clean.Aliases = append(clean.Aliases, a)
				}
			}
		}
		if n == primaryName {
			primary = map[string]*network.EndpointSettings{n: clean}
			continue
		}
		if extra == nil {
			extra = map[string]*network.EndpointSettings{}
		}
		extra[n] = clean
	}
	return primary, extra
}

// deepCopy copies a value through JSON, the form these Docker types have on
// the wire, so nested maps and slices are not shared with the original.
func deepCopy[T any](v *T) (*T, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("copy container settings: %w", err)
	}
	out := new(T)
	if err := json.Unmarshal(b, out); err != nil {
		return nil, fmt.Errorf("copy container settings: %w", err)
	}
	return out, nil
}
