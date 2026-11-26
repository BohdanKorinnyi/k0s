// Package hosts turns a saved host into a live connection to its Docker
// daemon: a local socket, TCP with optional TLS, or SSH.
package hosts

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Options configure how connections are made.
type Options struct {
	// KnownHostsFile is where host keys seen for the first time are remembered.
	// Empty means ssh's own default files.
	KnownHostsFile string
	// SSHCommand is the ssh client to run. Empty means "ssh" from PATH.
	SSHCommand string
}

// ValidateURL checks a connection string of the kind DOCKER_HOST takes:
//
//	unix:///var/run/docker.sock
//	tcp://host:2376
//	ssh://user@host[:port][/path/to/docker.sock]
func ValidateURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("the connection URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %v", err)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the connection URL must not have a query or fragment")
	}

	switch u.Scheme {
	case "unix":
		if u.Host != "" {
			return errors.New("a unix:// URL needs an absolute socket path, for example unix:///var/run/docker.sock")
		}
		if u.Path == "" {
			return errors.New("a unix:// URL needs a socket path, for example unix:///var/run/docker.sock")
		}
	case "tcp":
		if u.Hostname() == "" {
			return errors.New("a tcp:// URL needs a host, for example tcp://10.0.0.5:2376")
		}
		if u.User != nil {
			return errors.New("a tcp:// URL cannot carry a user name; Docker over TCP is secured with TLS certificates")
		}
		if u.Path != "" && u.Path != "/" {
			return errors.New("a tcp:// URL must not have a path")
		}
	case "ssh":
		if u.Hostname() == "" {
			return errors.New("an ssh:// URL needs a host, for example ssh://deploy@server.example.com")
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return errors.New("a password in the URL is not supported; use an SSH key or agent")
		}
		// These reach the ssh command line. A leading dash would be read as an
		// option, which can run arbitrary commands (-oProxyCommand=...).
		if strings.HasPrefix(u.Hostname(), "-") || strings.HasPrefix(u.User.Username(), "-") {
			return errors.New("the SSH user and host must not start with a dash")
		}
		// The path is passed to a shell on the far side.
		if !safeSocketPath(u.Path) {
			return errors.New("the socket path may contain only letters, digits and . _ - / ~")
		}
	default:
		return fmt.Errorf("unsupported scheme %q: use unix://, tcp:// or ssh://", u.Scheme)
	}
	return nil
}

func safeSocketPath(p string) bool {
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._-/~", r):
		default:
			return false
		}
	}
	return true
}

// sshArgs builds the ssh command line that connects to the host and runs
// `docker system dial-stdio` there, which bridges stdin/stdout to the daemon.
func sshArgs(u *url.URL, o Options) []string {
	args := []string{
		"-T",
		// Never prompt: there is no terminal to answer on.
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		// Notice a dead connection instead of hanging on it.
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		// Trust a host key the first time it is seen, refuse if it later changes.
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if o.KnownHostsFile != "" {
		// New keys are written to the first file; both are consulted.
		args = append(args, "-o", "UserKnownHostsFile="+o.KnownHostsFile+" ~/.ssh/known_hosts")
	}
	// A config file brought from macOS may use options only Apple's ssh knows.
	args = append(args, "-o", "IgnoreUnknown=UseKeychain")

	if user := u.User.Username(); user != "" {
		args = append(args, "-l", user)
	}
	if port := u.Port(); port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", u.Hostname(), "docker")
	if u.Path != "" && u.Path != "/" {
		args = append(args, "-H", "unix://"+u.Path)
	}
	return append(args, "system", "dial-stdio")
}

// tcpAddr returns host:port, adding Docker's conventional port when the URL
// has none: 2376 with TLS, 2375 without.
func tcpAddr(u *url.URL, withTLS bool) string {
	port := u.Port()
	if port == "" {
		port = "2375"
		if withTLS {
			port = "2376"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}
