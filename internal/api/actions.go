package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"dockpanel/internal/actions"
)

func (s *server) handleActionsList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Actions.List())
}

type actionInput struct {
	// Container limits the action to one container, by ID or name.
	Container string `json:"container"`
	// Confirm must equal the service name on a protected host.
	Confirm string `json:"confirm"`
}

// handleAction runs an action on a service and streams its progress, one JSON
// event per line, ending with an event whose "done" is true.
//
// Once started, an action runs to completion even if the browser goes away:
// stopping halfway through replacing a container is the one outcome worse than
// either finishing or never starting.
func (s *server) handleAction(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFor(w, r)
	if !ok {
		return
	}
	action, ok := s.deps.Actions.Get(r.PathValue("action"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such action: "+r.PathValue("action"))
		return
	}
	info := action.Info()

	var in actionInput
	if !readOptionalJSON(w, r, &in) {
		return
	}

	// One action per service at a time. Checked before talking to Docker, so
	// a second click is refused at once rather than queued behind the first.
	lock := h.ID + "\x00" + r.PathValue("key")
	if !s.acquire(lock) {
		writeError(w, http.StatusConflict, "busy", "another action is already running on this service")
		return
	}
	started := false
	defer func() {
		if !started {
			s.release(lock)
		}
	}()

	cli, err := s.deps.Hosts.Client(r.Context(), h.ID)
	if err != nil {
		unreachable(w, h, err)
		return
	}
	groups, ok := s.listServices(w, r, h, cli)
	if !ok {
		return
	}
	svc, found := findService(groups, r.PathValue("key"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such service on "+h.Name)
		return
	}
	if !action.Applies(svc) {
		writeError(w, http.StatusConflict, "not_applicable", fmt.Sprintf("%s does not apply to %s as it is now", info.Label, svc.Name))
		return
	}
	if h.Protected && in.Confirm != svc.Name {
		writeError(w, http.StatusForbidden, "confirmation_required",
			fmt.Sprintf("%s is a protected host: type the service name (%s) to confirm", h.Name, svc.Name))
		return
	}

	target := actions.Target{Service: svc}
	if in.Container != "" {
		if !info.ContainerLevel {
			writeError(w, http.StatusBadRequest, "invalid", info.Label+" applies to the whole service, not to one container")
			return
		}
		for _, c := range svc.Containers {
			if c.ID == in.Container || c.Name == in.Container {
				target.ContainerID = c.ID
			}
		}
		if target.ContainerID == "" {
			writeError(w, http.StatusBadRequest, "invalid", "that container is not part of "+svc.Name)
			return
		}
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // ask proxies not to hold the stream back
	w.WriteHeader(http.StatusOK)
	flush := http.NewResponseController(w).Flush
	_ = flush()

	events := make(chan actions.Event, 16)
	gone := make(chan struct{}) // closed when this handler stops listening
	defer close(gone)
	send := func(e actions.Event) {
		select {
		case events <- e:
		case <-gone:
		}
	}

	env := actions.Env{Docker: cli, Auth: s.deps.Auth, HealthTimeout: s.deps.Config.HealthTimeout}
	// Detached from the request so that a closed tab does not cancel it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
	started = true
	go func() {
		defer s.release(lock)
		defer cancel()
		defer close(events)

		err := runAction(ctx, action, env, target, send)
		final := actions.Event{Step: info.Label + " finished", Level: actions.LevelSuccess, Done: true}
		if err != nil {
			final = actions.Event{Step: err.Error(), Level: actions.LevelError, Done: true}
		}
		slog.Info("action", "host", h.ID, "service", svc.Key, "action", info.ID, "container", target.ContainerID, "error", err)
		send(final)
	}()

	enc := json.NewEncoder(w)
	for {
		select {
		case e, open := <-events:
			if !open {
				return
			}
			if err := enc.Encode(e); err != nil {
				return
			}
			_ = flush()
		case <-r.Context().Done():
			return
		}
	}
}

// runAction runs the action, turning a panic inside it into an error so the
// stream still ends with a closing event and the service is released.
func runAction(ctx context.Context, a actions.Action, env actions.Env, t actions.Target, report actions.Progress) (err error) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("action panicked", "action", a.Info().ID, "panic", v)
			err = fmt.Errorf("the action failed unexpectedly: %v", v)
		}
	}()
	return a.Run(ctx, env, t, report)
}

func (s *server) acquire(key string) bool {
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	if s.busy[key] {
		return false
	}
	s.busy[key] = true
	return true
}

func (s *server) release(key string) {
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	delete(s.busy, key)
}

// readOptionalJSON is readJSON for routes whose body may be absent.
func readOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the request is too large")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "the request body is not valid JSON")
		return false
	}
	return true
}
