package api

import (
	"net/http"

	"dc0s/internal/store"
)

type registryInput struct {
	Address  string `json:"address"`
	Username string `json:"username"`
	// Password is write-only. Blank on an edit keeps the stored one.
	Password string `json:"password"`
}

func (s *server) handleRegistriesList(w http.ResponseWriter, _ *http.Request) {
	saved := s.deps.Store.Registries()
	out := make([]registryView, 0, len(saved))
	for _, reg := range saved {
		out = append(out, toRegistryView(reg))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleRegistryCreate(w http.ResponseWriter, r *http.Request) {
	var in registryInput
	if !readJSON(w, r, &in) {
		return
	}
	saved, err := s.deps.Store.SaveRegistry(store.Registry{Address: in.Address, Username: in.Username, Password: in.Password})
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRegistryView(saved))
}

func (s *server) handleRegistryUpdate(w http.ResponseWriter, r *http.Request) {
	var in registryInput
	if !readJSON(w, r, &in) {
		return
	}
	saved, err := s.deps.Store.SaveRegistry(store.Registry{
		ID: r.PathValue("id"), Address: in.Address, Username: in.Username, Password: in.Password,
	})
	if err != nil {
		storeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRegistryView(saved))
}

func (s *server) handleRegistryDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteRegistry(r.PathValue("id")); err != nil {
		storeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
