// Package services turns a host's flat list of containers into the services a
// person thinks in: the containers of one Compose service together, and every
// other container as a service of its own.
package services

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"dockpanel/internal/dockerapi"
)

// Labels Docker Compose puts on the containers it creates.
const (
	LabelProject    = "com.docker.compose.project"
	LabelService    = "com.docker.compose.service"
	LabelNumber     = "com.docker.compose.container-number"
	LabelOneOff     = "com.docker.compose.oneoff"
	LabelWorkingDir = "com.docker.compose.project.working_dir"
)

// Service states, from most to least urgent.
const (
	StateRestarting = "restarting"
	StateUnhealthy  = "unhealthy"
	StateRunning    = "running"
	StatePartial    = "partial" // some containers running, some not
	StateStopped    = "stopped"
)

// ErrNotFound means the host has no service with the given key.
var ErrNotFound = errors.New("service not found")

// maxInspects bounds how many containers are inspected at once.
const maxInspects = 8

// Container is one container of a service.
type Container struct {
	ID      string
	Name    string
	Image   string // the reference it was created with, e.g. "shop/api:develop"
	ImageID string
	// State is Docker's own: created, running, paused, restarting, removing,
	// exited or dead.
	State string
	// Health is "", "healthy", "unhealthy" or "starting".
	Health     string
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Restarts   int
}

// Service is a group of containers shown as one row.
type Service struct {
	// Key identifies the service on its host and is safe in a URL:
	// "c~<project>~<service>" for Compose, "s~<container name>" otherwise.
	Key     string
	Name    string
	Project string // "" when the container was not created by Compose
	// Image is the reference all containers share, or "" when they differ.
	Image string
	// Images lists the distinct references, sorted.
	Images  []string
	Running int
	Total   int
	State   string
	// ExitCode is the exit code of the container that stopped last. It is
	// meaningful when State is "stopped".
	ExitCode int
	// StartedAt is the most recent start among running containers.
	StartedAt  time.Time
	Restarts   int
	Containers []Container
}

// Group is the services of one Compose project. Project is "" for containers
// that do not belong to one.
type Group struct {
	Project  string
	Services []Service
}

// List returns everything on the host, running or not, grouped by project.
// Projects are in name order with the no-project group last.
func List(ctx context.Context, cli dockerapi.Client) ([]Group, error) {
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	inspected, err := inspectAll(ctx, cli, listed.Items)
	if err != nil {
		return nil, err
	}

	type entry struct {
		c      Container
		number int
	}
	type bucket struct {
		svc     Service
		entries []entry
	}
	buckets := map[string]*bucket{}
	var order []string

	for _, in := range inspected {
		if in == nil {
			continue // removed between listing and inspecting
		}
		c := toContainer(*in)
		labels := map[string]string{}
		if in.Config != nil {
			labels = in.Config.Labels
		}
		project, service := labels[LabelProject], labels[LabelService]
		oneOff := strings.EqualFold(labels[LabelOneOff], "true")

		svc := Service{Key: "s~" + c.Name, Name: c.Name, Project: project}
		if project != "" && service != "" && !oneOff {
			svc = Service{Key: "c~" + project + "~" + service, Name: service, Project: project}
		}
		b, ok := buckets[svc.Key]
		if !ok {
			b = &bucket{svc: svc}
			buckets[svc.Key] = b
			order = append(order, svc.Key)
		}
		number, _ := strconv.Atoi(labels[LabelNumber])
		b.entries = append(b.entries, entry{c: c, number: number})
	}

	byProject := map[string][]Service{}
	for _, key := range order {
		b := buckets[key]
		slices.SortFunc(b.entries, func(x, y entry) int {
			return cmp.Or(cmp.Compare(x.number, y.number), cmp.Compare(x.c.Name, y.c.Name))
		})
		for _, e := range b.entries {
			b.svc.Containers = append(b.svc.Containers, e.c)
		}
		summarise(&b.svc)
		byProject[b.svc.Project] = append(byProject[b.svc.Project], b.svc)
	}

	groups := make([]Group, 0, len(byProject))
	for project, svcs := range byProject {
		slices.SortFunc(svcs, func(x, y Service) int { return cmp.Compare(x.Name, y.Name) })
		groups = append(groups, Group{Project: project, Services: svcs})
	}
	slices.SortFunc(groups, func(x, y Group) int {
		if (x.Project == "") != (y.Project == "") {
			if x.Project == "" {
				return 1
			}
			return -1
		}
		return cmp.Compare(x.Project, y.Project)
	})
	return groups, nil
}

// Find returns the service with the given key.
func Find(groups []Group, key string) (Service, bool) {
	for _, g := range groups {
		for _, s := range g.Services {
			if s.Key == key {
				return s, true
			}
		}
	}
	return Service{}, false
}

// Get lists the host and returns one service.
func Get(ctx context.Context, cli dockerapi.Client, key string) (Service, error) {
	groups, err := List(ctx, cli)
	if err != nil {
		return Service{}, err
	}
	s, ok := Find(groups, key)
	if !ok {
		return Service{}, ErrNotFound
	}
	return s, nil
}

// inspectAll inspects every listed container, a few at a time, keeping the
// listed order. A container that no longer exists yields nil: containers come
// and go, and one being replaced mid-refresh must not fail the whole list.
func inspectAll(ctx context.Context, cli dockerapi.Client, items []container.Summary) ([]*container.InspectResponse, error) {
	out := make([]*container.InspectResponse, len(items))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	slots := make(chan struct{}, maxInspects)
	for i, item := range items {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			res, err := cli.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
			switch {
			case err == nil:
				out[i] = &res.Container
			case cerrdefs.IsNotFound(err):
			default:
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out, firstErr
}

func toContainer(in container.InspectResponse) Container {
	c := Container{
		ID:       in.ID,
		Name:     strings.TrimPrefix(in.Name, "/"),
		ImageID:  in.Image,
		Restarts: in.RestartCount,
	}
	if in.Config != nil {
		c.Image = in.Config.Image
	}
	if st := in.State; st != nil {
		c.State = string(st.Status)
		c.ExitCode = st.ExitCode
		c.StartedAt = parseTime(st.StartedAt)
		c.FinishedAt = parseTime(st.FinishedAt)
		if st.Health != nil && st.Health.Status != container.NoHealthcheck {
			c.Health = string(st.Health.Status)
		}
	}
	return c
}

// summarise fills the fields of a service that are derived from its containers.
func summarise(s *Service) {
	s.Total = len(s.Containers)
	seen := map[string]bool{}
	var restarting, unhealthy bool
	var lastFinished time.Time

	for i, c := range s.Containers {
		if !seen[c.Image] {
			seen[c.Image] = true
			s.Images = append(s.Images, c.Image)
		}
		s.Restarts += c.Restarts
		switch c.State {
		case string(container.StateRunning):
			s.Running++
			if c.StartedAt.After(s.StartedAt) {
				s.StartedAt = c.StartedAt
			}
		case string(container.StateRestarting):
			restarting = true
		}
		if c.Health == string(container.Unhealthy) {
			unhealthy = true
		}
		if c.State != string(container.StateRunning) && (i == 0 || !c.FinishedAt.Before(lastFinished)) {
			lastFinished = c.FinishedAt
			s.ExitCode = c.ExitCode
		}
	}
	slices.Sort(s.Images)
	if len(s.Images) == 1 {
		s.Image = s.Images[0]
	}

	switch {
	case restarting:
		s.State = StateRestarting
	case unhealthy:
		s.State = StateUnhealthy
	case s.Running == s.Total && s.Total > 0:
		s.State = StateRunning
	case s.Running > 0:
		s.State = StatePartial
	default:
		s.State = StateStopped
	}
	if s.State != StateStopped {
		s.ExitCode = 0
	}
}

// parseTime reads Docker's timestamps. Docker reports "never" as year 1, which
// parses to the zero time.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
