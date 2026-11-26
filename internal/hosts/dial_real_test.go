package hosts

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"dockpanel/internal/store"
)

// These tests talk to a tiny stand-in for the Docker API over real sockets,
// so they exercise what Dial builds: scheme, dialer and TLS, end to end.

const bridgeEnv = "DOCKPANEL_TEST_SSH_BRIDGE"

// TestMain lets the test binary double as a fake `ssh`: when the variable is
// set it joins its stdin and stdout to a unix socket, which is what
// `ssh host docker system dial-stdio` does against a real host.
func TestMain(m *testing.M) {
	if sock := os.Getenv(bridgeEnv); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(255)
		}
		go func() { _, _ = io.Copy(conn, os.Stdin) }()
		_, _ = io.Copy(os.Stdout, conn)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeDocker answers the two calls a connection test makes.
func fakeDocker() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.47")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/version"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "27.0", "ApiVersion": "1.47", "MinAPIVersion": "1.24"})
		default:
			http.NotFound(w, r)
		}
	})
}

// unixDocker serves the fake API on a unix socket and returns its path.
func unixDocker(t *testing.T) string {
	t.Helper()
	// A short path: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "dp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: fakeDocker()}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return path
}

func testHost(t *testing.T, o Options, h store.Host) (string, error) {
	t.Helper()
	m := NewManager(newStore(t), o)
	defer m.Close()
	return m.Test(context.Background(), h)
}

func TestDialUnixSocket(t *testing.T) {
	path := unixDocker(t)
	version, err := testHost(t, Options{}, store.Host{Name: "sock", URL: "unix://" + path})
	if err != nil || version != "27.0" {
		t.Errorf("Test = %q, %v; want 27.0", version, err)
	}
}

func TestDialPlainTCP(t *testing.T) {
	srv := httptest.NewServer(fakeDocker())
	defer srv.Close()
	version, err := testHost(t, Options{}, store.Host{Name: "tcp", URL: "tcp://" + strings.TrimPrefix(srv.URL, "http://")})
	if err != nil || version != "27.0" {
		t.Errorf("Test = %q, %v; want 27.0", version, err)
	}
}

func TestDialTLS(t *testing.T) {
	srv := httptest.NewTLSServer(fakeDocker())
	defer srv.Close()
	addr := "tcp://" + strings.TrimPrefix(srv.URL, "https://")
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	version, err := testHost(t, Options{}, store.Host{Name: "tls", URL: addr, TLS: &store.TLS{CA: ca}})
	if err != nil || version != "27.0" {
		t.Errorf("with the server's CA: Test = %q, %v; want 27.0", version, err)
	}

	// A certificate we were not told to trust must be refused.
	other, _ := selfSigned(t)
	if _, err := testHost(t, Options{}, store.Host{Name: "tls", URL: addr, TLS: &store.TLS{CA: other}}); err == nil {
		t.Error("connected to a server whose certificate is not signed by the given CA")
	}
	// Speaking plain HTTP to a TLS port fails rather than silently working.
	if _, err := testHost(t, Options{}, store.Host{Name: "plain", URL: addr}); err == nil {
		t.Error("a plain connection to a TLS port succeeded")
	}
}

func TestDialSSH(t *testing.T) {
	path := unixDocker(t)
	t.Setenv(bridgeEnv, path)
	o := Options{SSHCommand: os.Args[0]}
	h := store.Host{Name: "remote", URL: "ssh://deploy@remote.example.com"}

	version, err := testHost(t, o, h)
	if err != nil || version != "27.0" {
		t.Fatalf("Test = %q, %v; want 27.0 through the ssh bridge", version, err)
	}

	// Shells and other raw streams use a second kind of connection. It must go
	// through ssh as well — not to a Docker socket on this machine.
	cli, err := Dial(h, o)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cli.Dialer()(ctx)
	if err != nil {
		t.Fatalf("raw stream dial: %v", err)
	}
	defer conn.Close()
	if network := conn.RemoteAddr().Network(); network != "command" {
		t.Errorf("raw streams for an ssh host use a %q connection, want the ssh command", network)
	}
}

// Several requests in a row reuse the same ssh process instead of starting
// one each.
func TestSSHConnectionIsReused(t *testing.T) {
	path := unixDocker(t)
	t.Setenv(bridgeEnv, path)
	counter := filepath.Join(t.TempDir(), "starts")
	wrapper := writeScript(t, `echo x >> "`+counter+`"; exec "`+os.Args[0]+`"`)

	st := newStore(t)
	h, err := st.SaveHost(store.Host{Name: "remote", URL: "ssh://deploy@remote.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, Options{SSHCommand: wrapper})
	defer m.Close()
	cli, err := m.Client(context.Background(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := cli.ServerVersion(context.Background(), client.ServerVersionOptions{}); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	starts, _ := os.ReadFile(counter)
	if n := strings.Count(string(starts), "x"); n != 1 {
		t.Errorf("ssh was started %d times for 5 requests in a row, want 1", n)
	}
}
