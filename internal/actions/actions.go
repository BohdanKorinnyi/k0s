// Package actions holds the things a person can do to a service: restart it,
// stop it, pull a newer image and replace its containers.
//
// # Adding an action
//
// Write a type that implements Action in a file of its own, and register it in
// Default. The API exposes every registered action, and the frontend draws its
// buttons from that list, so nothing else needs to change unless the action
// needs a screen of its own.
package actions

import (
	"context"
	"fmt"
	"time"

	"dockpanel/internal/dockerapi"
	"dockpanel/internal/registryauth"
	"dockpanel/internal/services"
)

// Levels of an Event.
const (
	LevelInfo    = "info"
	LevelSuccess = "success"
	LevelWarning = "warning"
	LevelError   = "error"
)

// Event is one line of progress shown to the person while an action runs.
type Event struct {
	Step  string `json:"step"`
	Level string `json:"level"`
	// Done marks the last event of an action. Actions do not set it: whoever
	// runs the action sends the closing event from the error Run returned.
	Done bool `json:"done"`
}

// Progress receives events as an action proceeds.
type Progress func(Event)

// Target is what an action is applied to.
type Target struct {
	Service services.Service
	// ContainerID limits the action to one container of the service. Empty
	// means every container, one at a time.
	ContainerID string
}

// Env is what an action may use.
type Env struct {
	Docker dockerapi.Client
	// Auth supplies registry logins for pulls. May be nil.
	Auth registryauth.Provider
	// HealthTimeout is how long a replaced container may take to turn healthy.
	HealthTimeout time.Duration
	// StartGrace is how long a replaced container without a health check must
	// stay up to count as started.
	StartGrace time.Duration
	// Poll is the interval between checks while waiting.
	Poll time.Duration
	// SelfContainerID is the ID (or its 12-character short form) of the
	// container the panel itself runs in, when it runs in one. Actions that
	// would stop that container halfway through their own work refuse.
	SelfContainerID string
}

// Info describes an action to the user interface.
type Info struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Destructive actions ask for confirmation even on hosts that are not
	// protected.
	Destructive bool `json:"destructive"`
	// ContainerLevel actions can also be applied to a single container.
	ContainerLevel bool `json:"containerLevel"`
}

// Action is one operation on a service.
type Action interface {
	Info() Info
	// Applies reports whether the action makes sense for the service as it is
	// now: there is no point offering Start for a service that is fully up.
	Applies(svc services.Service) bool
	// Run performs the action, reporting each step. It returns the first error
	// and leaves the remaining containers untouched.
	Run(ctx context.Context, env Env, t Target, report Progress) error
}

// Registry is an ordered set of actions.
type Registry struct {
	actions []Action
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds an action. Registering two actions with one ID is a
// programming error and panics.
func (r *Registry) Register(a Action) {
	id := a.Info().ID
	if _, exists := r.Get(id); exists {
		panic(fmt.Sprintf("actions: %q registered twice", id))
	}
	r.actions = append(r.actions, a)
}

// Get returns the action with the given ID.
func (r *Registry) Get(id string) (Action, bool) {
	for _, a := range r.actions {
		if a.Info().ID == id {
			return a, true
		}
	}
	return nil, false
}

// List describes every action, in the order they were registered.
func (r *Registry) List() []Info {
	out := make([]Info, 0, len(r.actions))
	for _, a := range r.actions {
		out = append(out, a.Info())
	}
	return out
}

// Applicable returns the IDs of the actions that apply to the service.
func (r *Registry) Applicable(svc services.Service) []string {
	out := []string{}
	for _, a := range r.actions {
		if a.Applies(svc) {
			out = append(out, a.Info().ID)
		}
	}
	return out
}

// Default returns the actions the panel ships with.
func Default() *Registry {
	r := NewRegistry()
	r.Register(restartAction())
	r.Register(startAction())
	r.Register(stopAction())
	r.Register(pullRestartAction())
	return r
}

// containers returns the containers the target covers.
func (t Target) containers() ([]services.Container, error) {
	if t.ContainerID == "" {
		return t.Service.Containers, nil
	}
	for _, c := range t.Service.Containers {
		if c.ID == t.ContainerID {
			return []services.Container{c}, nil
		}
	}
	return nil, fmt.Errorf("container %q is not part of service %q", t.ContainerID, t.Service.Name)
}

// isUp reports whether Docker has the container going: running, paused, or in
// a restart loop. Such a container can be stopped and need not be started.
func isUp(c services.Container) bool {
	switch c.State {
	case "running", "restarting", "paused":
		return true
	}
	return false
}
