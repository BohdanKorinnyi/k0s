package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"dc0s/internal/registryauth"
	"dc0s/internal/services"
)

// pullRestart pulls the newest image for the tag each container was created
// with, then replaces every container that is behind it, one at a time.
type pullRestart struct{}

func pullRestartAction() Action { return pullRestart{} }

func (pullRestart) Info() Info {
	return Info{ID: "pull-restart", Label: "Pull & restart", Destructive: true}
}

// ChangesImages marks the action as one after which update checks are stale.
func (pullRestart) ChangesImages() bool { return true }

// Applies when at least one container was created from a tag. A reference
// pinned to a digest always means the same image, so there is nothing to pull.
func (pullRestart) Applies(svc services.Service) bool {
	for _, c := range svc.Containers {
		if registryauth.Pullable(c.Image) {
			return true
		}
	}
	return false
}

func (pullRestart) Run(ctx context.Context, env Env, t Target, report Progress) error {
	if t.ContainerID != "" {
		return errors.New("pull & restart applies to the whole service: a new image is for all of its containers")
	}

	// Checked before anything is pulled or touched: a service that includes
	// the panel's own container cannot be replaced from inside the panel.
	for _, c := range t.Service.Containers {
		if err := refuseSelf(env, c.ID, c.Name); err != nil {
			return err
		}
	}

	var refs []string
	seen := map[string]bool{}
	for _, c := range t.Service.Containers {
		if registryauth.Pullable(c.Image) && !seen[c.Image] {
			seen[c.Image] = true
			refs = append(refs, c.Image)
		}
	}
	for _, ref := range refs {
		if err := pull(ctx, env, ref, platformInUse(ctx, env, t.Service, ref), report); err != nil {
			return err
		}
	}

	var todo []services.Container
	for _, c := range t.Service.Containers {
		if !registryauth.Pullable(c.Image) {
			report(Event{Step: c.Name + " is pinned to an exact image; left as it is", Level: LevelInfo})
			continue
		}
		todo = append(todo, c)
	}

	replacedAny := false
	for i, c := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		replaced, err := Replace(ctx, env, c.ID, report)
		if errors.Is(err, ErrStillStarting) {
			// The new container is running but nothing has vouched for it.
			// Do not replace the rest on trust: if the image is bad, the
			// remaining containers are what keeps the service up.
			if rest := todo[i+1:]; len(rest) > 0 {
				return fmt.Errorf("%s is on the new image but its health check has not passed yet, so %s left on the previous image. Run the action again once %s is healthy",
					c.Name, leftAlone(rest), c.Name)
			}
			replacedAny = true
			continue
		}
		if err != nil {
			// Stop here: the remaining containers keep running the old image,
			// so a bad image never takes the whole service down.
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		replacedAny = replacedAny || replaced
	}
	if !replacedAny {
		report(Event{Step: "Already up to date", Level: LevelInfo})
	}
	return nil
}

func leftAlone(rest []services.Container) string {
	names := make([]string, len(rest))
	for i, c := range rest {
		names[i] = c.Name
	}
	if len(names) == 1 {
		return names[0] + " was"
	}
	return strings.Join(names, ", ") + " were"
}

// platformInUse returns the platform of the image that containers using ref
// run now, so the pull asks for the same one. Without it Docker pulls the
// host's native variant, and a container deliberately run for another
// platform would be switched.
func platformInUse(ctx context.Context, env Env, svc services.Service, ref string) *ocispec.Platform {
	for _, c := range svc.Containers {
		if c.Image != ref || c.ImageID == "" {
			continue
		}
		if img, err := env.Docker.ImageInspect(ctx, c.ImageID); err == nil {
			return platformOf(img.InspectResponse)
		}
	}
	return nil
}

// pull fetches one image reference on the host, relaying Docker's summary
// lines. Per-layer progress is left out: it is noise in a step list.
func pull(ctx context.Context, env Env, ref string, platform *ocispec.Platform, report Progress) error {
	report(Event{Step: "Pulling " + ref, Level: LevelInfo})
	auth, err := registryauth.Encoded(ctx, env.Auth, ref)
	if err != nil {
		return fmt.Errorf("registry login for %s: %w", ref, err)
	}
	options := client.ImagePullOptions{RegistryAuth: auth}
	if platform != nil {
		options.Platforms = []ocispec.Platform{*platform}
	}
	resp, err := env.Docker.ImagePull(ctx, ref, options)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer resp.Close()

	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if strings.HasPrefix(msg.Status, "Status: ") || strings.HasPrefix(msg.Status, "Digest: ") {
			report(Event{Step: msg.Status, Level: LevelInfo})
		}
	}
	return nil
}
