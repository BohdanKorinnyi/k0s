package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"

	"dockpanel/internal/dockerapi"
	"dockpanel/internal/services"
	"dockpanel/internal/store"
	"dockpanel/internal/updates"
)

// Time limits for work done on behalf of one request.
const (
	hostCallTimeout    = 15 * time.Second
	pingTimeout        = 5 * time.Second
	actionTimeout      = 15 * time.Minute
	updateCheckTimeout = 2 * time.Minute
)

// The shapes below are what the frontend receives. Secrets have no field here
// at all, so they cannot be sent by accident: a host reports only whether it
// has TLS material, a registry login only whether it has a password.

type hostView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Protected bool   `json:"protected"`
	HasTLS    bool   `json:"hasTLS"`
}

func toHostView(h store.Host) hostView {
	return hostView{ID: h.ID, Name: h.Name, URL: h.URL, Protected: h.Protected, HasTLS: h.TLS != nil}
}

type registryView struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
}

func toRegistryView(r store.Registry) registryView {
	return registryView{ID: r.ID, Address: r.Address, Username: r.Username, HasPassword: r.Password != ""}
}

type containerView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Image      string     `json:"image"`
	ImageID    string     `json:"imageId"`
	State      string     `json:"state"`
	Health     string     `json:"health"`
	ExitCode   int        `json:"exitCode"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Restarts   int        `json:"restarts"`
}

type updateView struct {
	// Status is one of the updates statuses, or "unchecked" before the first
	// check of this host has finished.
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type serviceView struct {
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Project    string          `json:"project"`
	Image      string          `json:"image"`
	Images     []string        `json:"images"`
	Running    int             `json:"running"`
	Total      int             `json:"total"`
	State      string          `json:"state"`
	ExitCode   int             `json:"exitCode"`
	StartedAt  *time.Time      `json:"startedAt"`
	Restarts   int             `json:"restarts"`
	Update     updateView      `json:"update"`
	Actions    []string        `json:"actions"`
	Containers []containerView `json:"containers"`
}

type groupView struct {
	Project  string        `json:"project"`
	Services []serviceView `json:"services"`
}

type servicesResponse struct {
	Groups           []groupView `json:"groups"`
	UpdatesCheckedAt *time.Time  `json:"updatesCheckedAt"`
}

// timePtr turns Docker's "never" (the zero time) into null.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *server) toServiceView(hostID string, svc services.Service) serviceView {
	v := serviceView{
		Key:        svc.Key,
		Name:       svc.Name,
		Project:    svc.Project,
		Image:      svc.Image,
		Images:     svc.Images,
		Running:    svc.Running,
		Total:      svc.Total,
		State:      svc.State,
		ExitCode:   svc.ExitCode,
		StartedAt:  timePtr(svc.StartedAt),
		Restarts:   svc.Restarts,
		Update:     s.updateFor(hostID, svc),
		Actions:    s.deps.Actions.Applicable(svc),
		Containers: make([]containerView, 0, len(svc.Containers)),
	}
	if v.Images == nil {
		v.Images = []string{}
	}
	for _, c := range svc.Containers {
		v.Containers = append(v.Containers, containerView{
			ID: c.ID, Name: c.Name, Image: c.Image, ImageID: c.ImageID,
			State: c.State, Health: c.Health, ExitCode: c.ExitCode,
			StartedAt: timePtr(c.StartedAt), FinishedAt: timePtr(c.FinishedAt),
			Restarts: c.Restarts,
		})
	}
	return v
}

// updateFor combines what is remembered about each of the service's image
// references. Nothing is asked of the registry here.
func (s *server) updateFor(hostID string, svc services.Service) updateView {
	if s.deps.Updates == nil {
		return updateView{Status: "unchecked"}
	}
	results := map[string]updates.Result{}
	for _, ref := range svc.Images {
		r, ok := s.deps.Updates.Cached(hostID, ref)
		if !ok {
			return updateView{Status: "unchecked"}
		}
		results[ref] = r
	}
	images := make([]updates.ContainerImage, 0, len(svc.Containers))
	for _, c := range svc.Containers {
		images = append(images, updates.ContainerImage{Ref: c.Image, ImageID: c.ImageID})
	}
	status, reason := updates.ForService(results, images)
	return updateView{Status: string(status), Reason: reason}
}

func (s *server) toServicesResponse(hostID string, groups []services.Group) servicesResponse {
	out := servicesResponse{Groups: make([]groupView, 0, len(groups))}
	for _, g := range groups {
		gv := groupView{Project: g.Project, Services: make([]serviceView, 0, len(g.Services))}
		for _, svc := range g.Services {
			gv.Services = append(gv.Services, s.toServiceView(hostID, svc))
		}
		out.Groups = append(out.Groups, gv)
	}
	if s.deps.Updates != nil {
		out.UpdatesCheckedAt = timePtr(s.deps.Updates.LastRun(hostID))
	}
	return out
}

// hostFor finds the saved host named in the path. On failure it has sent the
// response.
func (s *server) hostFor(w http.ResponseWriter, r *http.Request) (store.Host, bool) {
	id := r.PathValue("host")
	h, ok := s.deps.Store.Host(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no host with ID "+id)
		return store.Host{}, false
	}
	return h, true
}

// clientFor finds the host and a Docker client for it.
func (s *server) clientFor(w http.ResponseWriter, r *http.Request) (store.Host, dockerapi.Client, bool) {
	h, ok := s.hostFor(w, r)
	if !ok {
		return store.Host{}, nil, false
	}
	cli, err := s.deps.Hosts.Client(r.Context(), h.ID)
	if err != nil {
		unreachable(w, h, err)
		return store.Host{}, nil, false
	}
	return h, cli, true
}

// unreachable reports that a host could not be talked to, with the reason.
// The reason is usually exactly what the person needs: "Permission denied
// (publickey)", "connection refused", "no route to host".
func unreachable(w http.ResponseWriter, h store.Host, err error) {
	slog.Warn("host unreachable", "host", h.ID, "error", err)
	writeError(w, http.StatusBadGateway, "host_unreachable", "cannot reach "+h.Name+": "+brief(err))
}

// dockerFailure reports an error from a Docker call about one object.
func dockerFailure(w http.ResponseWriter, what string, err error) {
	switch {
	case cerrdefs.IsNotFound(err), errors.Is(err, services.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+" was not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusBadGateway, "host_unreachable", "the host did not answer in time")
	default:
		writeError(w, http.StatusBadGateway, "docker_error", brief(err))
	}
}

// brief shortens an error for display: its first line, capped.
func brief(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	const limit = 400
	if len(msg) > limit {
		msg = msg[:limit] + "…"
	}
	return msg
}

// storeFailure maps the store's errors to responses.
func storeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "duplicate", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		slog.Error("save failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not save: "+brief(err))
	}
}
