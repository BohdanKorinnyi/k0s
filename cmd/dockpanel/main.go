// Command dockpanel serves the web control panel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dockpanel/internal/actions"
	"dockpanel/internal/api"
	"dockpanel/internal/appinfo"
	"dockpanel/internal/config"
	"dockpanel/internal/hosts"
	"dockpanel/internal/registryauth"
	"dockpanel/internal/store"
	"dockpanel/internal/updates"
	"dockpanel/internal/webui"
)

func main() {
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println(appinfo.Name, appinfo.Version)
		return
	}

	cfg, err := config.Load(os.Getenv, os.ReadFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, appinfo.Name+":", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, appinfo.Name+":", err)
		os.Exit(1)
	}
}

// run serves until ctx ends.
func run(ctx context.Context, cfg config.Config) error {
	st, err := store.Open(filepath.Join(cfg.DataDir, appinfo.Name+".json"))
	if err != nil {
		return err
	}
	if added, err := addLocalHost(st, cfg.LocalSocket); err != nil {
		slog.Warn("could not add the local Docker socket as a host", "error", err)
	} else if added {
		slog.Info("first run: added the local Docker socket as host \"local\"", "socket", cfg.LocalSocket)
	}

	manager := hosts.NewManager(st, hosts.Options{
		// Host keys seen for the first time are remembered next to the data.
		KnownHostsFile: filepath.Join(cfg.DataDir, "known_hosts"),
	})
	defer manager.Close()

	// Registry logins are read from the store on every use, so a login saved
	// in the UI works at once.
	auth := registryauth.Static{List: func() []registryauth.Credential {
		saved := st.Registries()
		out := make([]registryauth.Credential, 0, len(saved))
		for _, r := range saved {
			out = append(out, registryauth.Credential{Address: r.Address, Username: r.Username, Password: r.Password})
		}
		return out
	}}

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: api.New(api.Deps{
			Config:  cfg,
			Store:   st,
			Hosts:   manager,
			Actions: actions.Default(),
			Updates: updates.NewChecker(auth, time.Now),
			Auth:    auth,
			UI:      webui.FS(),
		}),
		// No overall read or write limits: log streams, shells and action
		// progress are long-lived by design.
		ReadHeaderTimeout: 10 * time.Second,
	}

	if w := exposureWarning(cfg); w != "" {
		slog.Warn(w)
	}
	slog.Info(appinfo.Name+" is listening", "addr", cfg.Addr, "version", appinfo.Version,
		"login", map[bool]string{true: "password", false: "none"}[cfg.Password != ""])

	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// addLocalHost offers the mounted Docker socket as a host named "local", so
// the first screen of a fresh install shows something. It acts only when
// there is no saved data yet: once the person has their own list of hosts —
// even an empty one — it is theirs.
func addLocalHost(st *store.Store, socket string) (bool, error) {
	if !st.IsNew() || socket == "" {
		return false, nil
	}
	if _, err := os.Stat(socket); err != nil {
		return false, nil
	}
	if _, err := st.SaveHost(store.Host{Name: "local", URL: "unix://" + socket}); err != nil {
		return false, err
	}
	return true, nil
}

// exposureWarning returns a warning when the panel was told to answer under
// names beyond the machine itself and has no password: anyone who can reach
// one of those names can then open shells and read secrets.
func exposureWarning(cfg config.Config) string {
	if cfg.Password != "" || len(cfg.AllowedHosts) <= len(config.DefaultAllowedHosts) {
		return ""
	}
	extra := cfg.AllowedHosts[len(config.DefaultAllowedHosts):]
	return fmt.Sprintf("the panel answers to %s without a password: anyone who can reach it there can open shells and read secrets. Set %sPASSWORD.",
		strings.Join(extra, ", "), appinfo.EnvPrefix)
}
