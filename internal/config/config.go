// Package config reads the program's settings from environment variables.
package config

import (
	"fmt"
	"strings"
	"time"

	"dockpanel/internal/appinfo"
)

// Config is everything the program can be told at start-up.
type Config struct {
	// Addr is the address the web server listens on.
	Addr string
	// DataDir holds the data file and SSH bookkeeping.
	DataDir string
	// Password, when not empty, must be entered before the panel can be used.
	Password string
	// AllowedHosts are the hostnames requests may be addressed to.
	AllowedHosts []string
	// HealthTimeout is how long a replaced container may take to become healthy.
	HealthTimeout time.Duration
	// LocalSocket is the Docker socket offered as host "local" on first run.
	LocalSocket string
	// SSHDir is a folder of SSH keys and configuration, usually the user's
	// ~/.ssh mounted read-only. Its files are copied to where ssh reads them.
	SSHDir string
}

// DefaultAllowedHosts are always accepted: the machine talking to itself.
var DefaultAllowedHosts = []string{"localhost", "127.0.0.1", "::1"}

// Load builds a Config from the environment. getenv and readFile are passed in
// so tests do not depend on the real environment or file system.
func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	key := func(name string) string { return appinfo.EnvPrefix + name }
	get := func(name, fallback string) string {
		if v := strings.TrimSpace(getenv(key(name))); v != "" {
			return v
		}
		return fallback
	}

	c := Config{
		// Loopback by default: run on its own, the panel is not reachable
		// from the network. The Docker image sets ADDR to all interfaces,
		// which a container needs for its port to be published at all.
		Addr:          get("ADDR", "127.0.0.1:8080"),
		DataDir:       get("DATA_DIR", "/data"),
		AllowedHosts:  append([]string(nil), DefaultAllowedHosts...),
		HealthTimeout: 2 * time.Minute,
		LocalSocket:   get("LOCAL_SOCKET", "/var/run/docker.sock"),
		SSHDir:        get("SSH_DIR", "/ssh"),
	}

	for _, h := range strings.Split(getenv(key("ALLOWED_HOSTS")), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			c.AllowedHosts = append(c.AllowedHosts, h)
		}
	}

	// A password given directly is not trimmed: spaces may be intended.
	password := getenv(key("PASSWORD"))
	passwordFile := strings.TrimSpace(getenv(key("PASSWORD_FILE")))
	switch {
	case password != "" && passwordFile != "":
		return Config{}, fmt.Errorf("set %s or %s, not both", key("PASSWORD"), key("PASSWORD_FILE"))
	case passwordFile != "":
		b, err := readFile(passwordFile)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", key("PASSWORD_FILE"), err)
		}
		c.Password = strings.TrimSpace(string(b))
	default:
		c.Password = password
	}

	if raw := strings.TrimSpace(getenv(key("HEALTH_TIMEOUT"))); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %q is not a duration such as 90s or 2m", key("HEALTH_TIMEOUT"), raw)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("%s: must be greater than zero", key("HEALTH_TIMEOUT"))
		}
		c.HealthTimeout = d
	}

	return c, nil
}
