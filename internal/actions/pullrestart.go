package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moby/moby/client"

	"dockpanel/internal/registryauth"
	"dockpanel/internal/services"
)

// pullRestart pulls the newest image for the tag each container was created
// with, then replaces every container that is behind it, one at a time.
type pullRestart struct{}

func pullRestartAction() Action { return pullRestart{} }

func (pullRestart) Info() Info {
	return Info{ID: "pull-restart", Label: "Pull & restart", Destructive: true}
}

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

	var refs []string
	seen := map[string]bool{}
	for _, c := range t.Service.Containers {
		if registryauth.Pullable(c.Image) && !seen[c.Image] {
			seen[c.Image] = true
			refs = append(refs, c.Image)
		}
	}
	for _, ref := range refs {
		if err := pull(ctx, env, ref, report); err != nil {
			return err
		}
	}

	replacedAny := false
	for _, c := range t.Service.Containers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !registryauth.Pullable(c.Image) {
			report(Event{Step: c.Name + " is pinned to an exact image; left as it is", Level: LevelInfo})
			continue
		}
		replaced, err := Replace(ctx, env, c.ID, report)
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

// pull fetches one image reference on the host, relaying Docker's summary
// lines. Per-layer progress is left out: it is noise in a step list.
func pull(ctx context.Context, env Env, ref string, report Progress) error {
	report(Event{Step: "Pulling " + ref, Level: LevelInfo})
	auth, err := registryauth.Encoded(ctx, env.Auth, ref)
	if err != nil {
		return fmt.Errorf("registry login for %s: %w", ref, err)
	}
	resp, err := env.Docker.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: auth})
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
