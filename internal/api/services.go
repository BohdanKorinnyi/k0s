package api

import (
	"context"
	"net/http"

	"dockpanel/internal/dockerapi"
	"dockpanel/internal/services"
	"dockpanel/internal/store"
)

// listServices lists a host within the time limit. On failure it has sent the
// response: a host that cannot be listed is unreachable, whatever the detail.
func (s *server) listServices(w http.ResponseWriter, r *http.Request, h store.Host, cli dockerapi.Client) ([]services.Group, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), hostCallTimeout)
	defer cancel()
	groups, err := services.List(ctx, cli)
	if err != nil {
		unreachable(w, h, err)
		return nil, false
	}
	return groups, true
}

func (s *server) handleServices(w http.ResponseWriter, r *http.Request) {
	h, cli, ok := s.clientFor(w, r)
	if !ok {
		return
	}
	groups, ok := s.listServices(w, r, h, cli)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toServicesResponse(h.ID, groups))

	// Asking registries can be slow, so it never holds up the list. When the
	// host's results have aged out, a check starts now and a later refresh of
	// the list shows what it found.
	if s.deps.Updates != nil && s.deps.Updates.StartIfDue(h.ID) {
		refs := imageRefs(groups)
		s.spawn(func() { s.checkUpdates(h.ID, cli, refs, false) })
	}
}

func (s *server) handleService(w http.ResponseWriter, r *http.Request) {
	h, cli, ok := s.clientFor(w, r)
	if !ok {
		return
	}
	groups, ok := s.listServices(w, r, h, cli)
	if !ok {
		return
	}
	svc, found := services.Find(groups, r.PathValue("key"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such service on "+h.Name)
		return
	}
	writeJSON(w, http.StatusOK, s.toServiceView(h.ID, svc))
}

// handleCheckUpdates is the "Check now" button: ask the registries again,
// wait, and answer with the refreshed list.
func (s *server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	h, cli, ok := s.clientFor(w, r)
	if !ok {
		return
	}
	groups, ok := s.listServices(w, r, h, cli)
	if !ok {
		return
	}
	if s.deps.Updates != nil {
		s.checkUpdates(h.ID, cli, imageRefs(groups), true)
	}
	writeJSON(w, http.StatusOK, s.toServicesResponse(h.ID, groups))
}

func (s *server) checkUpdates(hostID string, cli dockerapi.Client, refs []string, force bool) {
	// Not tied to the request: a background check outlives it.
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	s.deps.Updates.Check(ctx, cli, hostID, refs, force)
}

// imageRefs returns every distinct image reference in use on the host.
func imageRefs(groups []services.Group) []string {
	seen := map[string]bool{}
	var refs []string
	for _, g := range groups {
		for _, svc := range g.Services {
			for _, ref := range svc.Images {
				if !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
		}
	}
	return refs
}
