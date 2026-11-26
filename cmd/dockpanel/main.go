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
	"regexp"
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

	// Keys and config mounted from the host are copied to where ssh reads
	// them, with the ownership and modes ssh insists on.
	if home, err := os.UserHomeDir(); err == nil {
		n, err := hosts.PrepareSSHDir(cfg.SSHDir, filepath.Join(home, ".ssh"))
		if err != nil {
			slog.Warn("could not prepare the SSH keys", "from", cfg.SSHDir, "error", err)
		} else if n > 0 {
			slog.Info("SSH keys and configuration are in place", "from", cfg.SSHDir, "files", n)
		}
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

	mountinfo, _ := os.ReadFile("/proc/self/mountinfo")
	hostname, _ := os.Hostname()
	self := selfContainerID(string(mountinfo), hostname)

	panel := api.NewServer(api.Deps{
		Config:          cfg,
		Store:           st,
		Hosts:           manager,
		Actions:         actions.Default(),
		Updates:         updates.NewChecker(auth, time.Now),
		Auth:            auth,
		UI:              webui.FS(),
		SelfContainerID: func() string { return self },
	})
	server := &http.Server{
		Addr:    cfg.Addr,
		Handler: panel.Handler(),
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
	// An action cut off between stopping a container and starting its
	// replacement would leave a service down, so running actions are given
	// time to finish. Whoever stops the panel must allow that time too: see
	// stop_grace_period in compose.yaml.
	slog.Info("shutting down; waiting for running actions to finish")
	drain, stopDrain := context.WithTimeout(context.Background(), drainTimeout)
	if err := panel.Drain(drain); err != nil {
		slog.Error("shut down while an action was still running; a container may be left stopped under a name ending in -old-…", "error", err)
	}
	stopDrain()

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// drainTimeout is how long shutdown waits for running actions.
const drainTimeout = 3 * time.Minute

var (
	containerInMountinfo = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)
	shortContainerID     = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// selfContainerID works out which container the panel runs in, so that it
// can refuse to replace itself. Docker mounts a few files from the
// container's own folder, which names its ID; failing that, a container's
// hostname is its short ID unless someone chose another. Outside a container
// both come up empty.
func selfContainerID(mountinfo, hostname string) string {
	if m := containerInMountinfo.FindStringSubmatch(mountinfo); m != nil {
		return m[1]
	}
	if shortContainerID.MatchString(hostname) {
		return hostname
	}
	return ""
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
