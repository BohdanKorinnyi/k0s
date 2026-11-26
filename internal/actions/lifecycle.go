package actions

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"

	"dc0s/internal/dockerapi"
	"dc0s/internal/services"
)

// lifecycle is the shape restart, start and stop share: the plain Docker
// operation applied to each container in turn. Same container, same image.
type lifecycle struct {
	info    Info
	applies func(services.Service) bool
	// skip returns a note when the container is already in the wanted state.
	skip func(services.Container) (note string, skip bool)
	verb string
	do   func(ctx context.Context, cli dockerapi.Client, id string) error
}

func (l lifecycle) Info() Info                        { return l.info }
func (l lifecycle) Applies(svc services.Service) bool { return l.applies(svc) }

func (l lifecycle) Run(ctx context.Context, env Env, t Target, report Progress) error {
	containers, err := t.containers()
	if err != nil {
		return err
	}
	for _, c := range containers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.skip != nil {
			if note, skip := l.skip(c); skip {
				report(Event{Step: c.Name + " " + note, Level: LevelInfo})
				continue
			}
		}
		report(Event{Step: l.verb + " " + c.Name, Level: LevelInfo})
		if err := l.do(ctx, env.Docker, c.ID); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

func anyUp(svc services.Service) bool {
	for _, c := range svc.Containers {
		if isUp(c) {
			return true
		}
	}
	return false
}

func anyDown(svc services.Service) bool {
	for _, c := range svc.Containers {
		if !isUp(c) {
			return true
		}
	}
	return false
}

func restartAction() Action {
	return lifecycle{
		info:    Info{ID: "restart", Label: "Restart", ContainerLevel: true},
		applies: func(svc services.Service) bool { return svc.Total > 0 },
		verb:    "Restarting",
		do: func(ctx context.Context, cli dockerapi.Client, id string) error {
			_, err := cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
			return err
		},
	}
}

func startAction() Action {
	return lifecycle{
		info:    Info{ID: "start", Label: "Start", ContainerLevel: true},
		applies: anyDown,
		skip: func(c services.Container) (string, bool) {
			return "is already running", isUp(c)
		},
		verb: "Starting",
		do: func(ctx context.Context, cli dockerapi.Client, id string) error {
			_, err := cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
			return err
		},
	}
}

func stopAction() Action {
	return lifecycle{
		info:    Info{ID: "stop", Label: "Stop", Destructive: true, ContainerLevel: true},
		applies: anyUp,
		skip: func(c services.Container) (string, bool) {
			return "is already stopped", !isUp(c)
		},
		verb: "Stopping",
		do: func(ctx context.Context, cli dockerapi.Client, id string) error {
			_, err := cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
			return err
		},
	}
}
