// Package api is the panel's HTTP server: a JSON API under /api/, live
// connections for logs and shells, and the built frontend for everything else.
package api

import (
	"context"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"dockpanel/internal/actions"
	"dockpanel/internal/config"
	"dockpanel/internal/dockerapi"
	"dockpanel/internal/registryauth"
	"dockpanel/internal/store"
	"dockpanel/internal/updates"
)

// HostClients gives the server a Docker client for a saved host. It is the
// part of hosts.Manager the server uses, as an interface so tests can supply
// fake daemons.
type HostClients interface {
	Client(ctx context.Context, hostID string) (dockerapi.Client, error)
	Test(ctx context.Context, h store.Host) (version string, err error)
	Invalidate(hostID string)
}

// Deps is everything the server needs from the rest of the program.
type Deps struct {
	Config  config.Config
	Store   *store.Store
	Hosts   HostClients
	Actions *actions.Registry
	Updates *updates.Checker
	// Auth supplies registry logins to actions. May be nil.
	Auth registryauth.Provider
	// UI holds the built frontend, with index.html at its root.
	UI fs.FS
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type server struct {
	deps     Deps
	mux      *http.ServeMux
	sessions *sessionStore
	logins   *failureLimiter

	// busy holds the services an action is currently running on, so a second
	// action on the same service is refused instead of interleaving.
	busyMu sync.Mutex
	busy   map[string]bool
}

// New returns the server's handler.
func New(d Deps) http.Handler { return newServer(d).handler() }

func newServer(d Deps) *server {
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &server{
		deps:     d,
		mux:      http.NewServeMux(),
		sessions: newSessionStore(d.Now),
		logins:   newFailureLimiter(d.Now),
		busy:     map[string]bool{},
	}
	s.routes()
	return s
}

func (s *server) routes() {
	s.mux.HandleFunc("GET /api/session", s.handleSession)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Anything else under /api/ is unknown; everything outside it is the UI.
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such API route: "+r.Method+" "+r.URL.Path)
	})
	s.mux.Handle("/", s.uiHandler())
}

// handler wraps the routes in the checks every request passes, outermost
// first. The order matters: a request addressed to a hostname we do not answer
// to, or sent by another website, is turned away before anything looks at
// cookies or routes.
func (s *server) handler() http.Handler {
	var h http.Handler = s.mux
	h = s.requireSession(h)
	h = refuseCrossSite(h)
	h = contentSecurityPolicy(h)
	h = s.requireAllowedHost(h)
	h = baseHeaders(h)
	h = recoverPanics(h)
	return h
}
