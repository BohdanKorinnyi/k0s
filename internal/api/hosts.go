package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/moby/moby/client"

	"dc0s/internal/hosts"
	"dc0s/internal/store"
)

type tlsInput struct {
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type hostInput struct {
	// ID is used only by the connection test, to find stored secrets for a
	// host that is being edited.
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Protected bool      `json:"protected"`
	TLS       *tlsInput `json:"tls"`
}

// hostFromInput turns what the form sent into a host. The browser is never
// given the saved certificates or key, so it cannot send them back: each TLS
// field left blank means "unchanged" and is filled in from what is stored.
// Sending no TLS section at all removes TLS. The result is checked by building
// a client from it, which catches a bad URL or certificate before anything is
// saved.
func (s *server) hostFromInput(in hostInput, existingID string) (store.Host, error) {
	h := store.Host{
		ID:        existingID,
		Name:      in.Name,
		URL:       strings.TrimSpace(in.URL),
		Protected: in.Protected,
	}
	if err := hosts.ValidateURL(h.URL); err != nil {
		return store.Host{}, err
	}

	// TLS belongs to tcp:// only; switching a host to another scheme drops it.
	if u, err := url.Parse(h.URL); err == nil && u.Scheme == "tcp" && in.TLS != nil {
		t := store.TLS{CA: strings.TrimSpace(in.TLS.CA), Cert: strings.TrimSpace(in.TLS.Cert), Key: strings.TrimSpace(in.TLS.Key)}
		if existingID != "" {
			if existing, ok := s.deps.Store.Host(existingID); ok && existing.TLS != nil {
				if t.CA == "" {
					t.CA = existing.TLS.CA
				}
				if t.Cert == "" {
					t.Cert = existing.TLS.Cert
				}
				if t.Key == "" {
					t.Key = existing.TLS.Key
				}
			}
		}
		if t.CA != "" || t.Cert != "" || t.Key != "" {
			h.TLS = &t
		}
	}

	cli, err := hosts.Dial(h, hosts.Options{})
	if err != nil {
		return store.Host{}, err
	}
	_ = cli.Close()
	return h, nil
}

func (s *server) handleHostsList(w http.ResponseWriter, _ *http.Request) {
	saved := s.deps.Store.Hosts()
	out := make([]hostView, 0, len(saved))
	for _, h := range saved {
		out = append(out, toHostView(h))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleHostCreate(w http.ResponseWriter, r *http.Request) {
	var in hostInput
	if !readJSON(w, r, &in) {
		return
	}
	h, err := s.hostFromInput(in, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	saved, err := s.deps.Store.SaveHost(h)
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toHostView(saved))
}

func (s *server) handleHostUpdate(w http.ResponseWriter, r *http.Request) {
	existing, ok := s.hostFor(w, r)
	if !ok {
		return
	}
	var in hostInput
	if !readJSON(w, r, &in) {
		return
	}
	h, err := s.hostFromInput(in, existing.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	saved, err := s.deps.Store.SaveHost(h)
	if err != nil {
		storeFailure(w, err)
		return
	}
	s.forgetHost(saved.ID)
	writeJSON(w, http.StatusOK, toHostView(saved))
}

func (s *server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFor(w, r)
	if !ok {
		return
	}
	if err := s.deps.Store.DeleteHost(h.ID); err != nil {
		storeFailure(w, err)
		return
	}
	s.forgetHost(h.ID)
	w.WriteHeader(http.StatusNoContent)
}

// forgetHost drops the live connection and remembered update results of a
// host whose settings changed.
func (s *server) forgetHost(id string) {
	s.deps.Hosts.Invalidate(id)
	if s.deps.Updates != nil {
		s.deps.Updates.Forget(id)
	}
}

type connectionResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// handleHostTest tries a connection described by the form, saved or not. A
// failed connection is a normal answer, not an HTTP error: the form shows it.
func (s *server) handleHostTest(w http.ResponseWriter, r *http.Request) {
	var in hostInput
	if !readJSON(w, r, &in) {
		return
	}
	h, err := s.hostFromInput(in, in.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	version, err := s.deps.Hosts.Test(r.Context(), h)
	if err != nil {
		writeJSON(w, http.StatusOK, connectionResult{Error: brief(err)})
		return
	}
	writeJSON(w, http.StatusOK, connectionResult{OK: true, Version: version})
}

// handleHostStatus reports whether a saved host answers, for the dot on its tab.
func (s *server) handleHostStatus(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFor(w, r)
	if !ok {
		return
	}
	cli, err := s.deps.Hosts.Client(r.Context(), h.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, connectionResult{Error: brief(err)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()
	v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		writeJSON(w, http.StatusOK, connectionResult{Error: brief(err)})
		return
	}
	writeJSON(w, http.StatusOK, connectionResult{OK: true, Version: v.Version})
}
