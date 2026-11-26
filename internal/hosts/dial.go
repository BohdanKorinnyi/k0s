package hosts

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"dockpanel/internal/appinfo"
	"dockpanel/internal/store"
)

// Dial builds a Docker client for the host. It does not connect: the first
// request does, so a client for an unreachable host is still returned.
func Dial(h store.Host, o Options) (*client.Client, error) {
	if err := ValidateURL(h.URL); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(h.URL))
	if err != nil {
		return nil, err
	}

	// Keep a few connections open between refreshes. Over SSH each new one is
	// a full handshake, so reuse matters.
	transport := &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	opts := []client.Opt{
		client.WithHTTPClient(&http.Client{Transport: transport}),
		client.WithUserAgent(appinfo.Name + "/" + appinfo.Version),
	}

	switch u.Scheme {
	case "unix":
		opts = append(opts, client.WithHost("unix://"+u.Path))

	case "tcp":
		if h.TLS != nil {
			cfg, err := tlsConfig(*h.TLS)
			if err != nil {
				return nil, err
			}
			// The client switches to https when the transport has a TLS config.
			transport.TLSClientConfig = cfg
		}
		opts = append(opts, client.WithHost("tcp://"+tcpAddr(u, h.TLS != nil)))

	case "ssh":
		command := o.SSHCommand
		if command == "" {
			command = "ssh"
		}
		args := sshArgs(u, o)
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := newCommandConn(ctx, command, args...)
			if errors.Is(err, exec.ErrNotFound) {
				return nil, errNoSSH
			}
			return conn, err
		}
		// The address is never dialled: every connection goes through ssh. A
		// unix:// placeholder keeps HTTP proxy settings out of the picture.
		opts = append(opts, client.WithHost("unix:///var/run/docker.sock"), client.WithDialContext(dial))
	}

	return client.New(opts...)
}

// tlsConfig builds the TLS settings for a tcp:// host from PEM text.
func tlsConfig(t store.TLS) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if ca := strings.TrimSpace(t.CA); ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("the CA certificate is not valid PEM")
		}
		cfg.RootCAs = pool
	}

	cert, key := strings.TrimSpace(t.Cert), strings.TrimSpace(t.Key)
	if cert != "" || key != "" {
		if cert == "" || key == "" {
			return nil, errors.New("the client certificate and its key must be given together")
		}
		pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return nil, fmt.Errorf("the client certificate or key is not valid: %v", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}
