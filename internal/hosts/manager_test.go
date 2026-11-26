package hosts

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dc0s/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// selfSigned returns a certificate and its key as PEM text.
func selfSigned(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dc0s test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestDialEveryKindWithoutConnecting(t *testing.T) {
	cert, key := selfSigned(t)
	hosts := []store.Host{
		{Name: "sock", URL: "unix:///var/run/docker.sock"},
		{Name: "plain", URL: "tcp://10.0.0.5:2375"},
		{Name: "tls", URL: "tcp://10.0.0.5:2376", TLS: &store.TLS{CA: cert, Cert: cert, Key: key}},
		{Name: "tls-ca-only", URL: "tcp://10.0.0.5:2376", TLS: &store.TLS{CA: cert}},
		{Name: "ssh", URL: "ssh://deploy@aws.example.com"},
	}
	for _, h := range hosts {
		cli, err := Dial(h, Options{})
		if err != nil {
			t.Errorf("Dial(%s): %v", h.Name, err)
			continue
		}
		cli.Close()
	}
}

func TestDialBadTLS(t *testing.T) {
	cert, key := selfSigned(t)
	cases := []struct {
		name string
		tls  store.TLS
	}{
		{"cert is not PEM", store.TLS{Cert: "not pem", Key: key}},
		{"CA is not PEM", store.TLS{CA: "not pem"}},
		{"cert without key", store.TLS{Cert: cert}},
	}
	for _, c := range cases {
		_, err := Dial(store.Host{Name: "tls", URL: "tcp://10.0.0.5:2376", TLS: &c.tls}, Options{})
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("%s: err = %v, want one mentioning the certificate", c.name, err)
		}
	}
}

func TestDialRejectsInvalidURL(t *testing.T) {
	if _, err := Dial(store.Host{Name: "x", URL: "http://x"}, Options{}); err == nil {
		t.Error("Dial accepted an unsupported URL")
	}
}

func TestManagerCaches(t *testing.T) {
	st := newStore(t)
	h, err := st.SaveHost(store.Host{Name: "aws", URL: "tcp://10.0.0.5:2375"})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, Options{})
	defer m.Close()

	a, err := m.Client(context.Background(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Client(context.Background(), h.ID)
	if a != b {
		t.Error("two Client calls returned different clients; want the cached one")
	}
	m.Invalidate(h.ID)
	c, _ := m.Client(context.Background(), h.ID)
	if c == a {
		t.Error("Client returned the old client after Invalidate")
	}
	if _, err := m.Client(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown host: err = %v, want store.ErrNotFound", err)
	}
}

func TestTestUnreachable(t *testing.T) {
	m := NewManager(newStore(t), Options{})
	defer m.Close()
	start := time.Now()
	_, err := m.Test(context.Background(), store.Host{Name: "x", URL: "tcp://127.0.0.1:1"})
	if err == nil {
		t.Fatal("Test succeeded against a port nothing listens on")
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("Test took %v, want under 6s", d)
	}
}

// A host that accepts the connection and then says nothing must not hang the
// caller: the limit applies to the whole exchange, not only to connecting.
func TestTestSilentHostTimesOut(t *testing.T) {
	m := NewManager(newStore(t), Options{SSHCommand: writeScript(t, "sleep 30")})
	defer m.Close()
	old := testTimeout
	testTimeout = 300 * time.Millisecond
	defer func() { testTimeout = old }()

	start := time.Now()
	_, err := m.Test(context.Background(), store.Host{Name: "x", URL: "ssh://deploy@host"})
	if err == nil {
		t.Fatal("Test succeeded against a host that never answers")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Test took %v, want it cut off near the limit", d)
	}
}

// What ssh prints when it fails is what the person needs to read.
func TestSSHFailureIsReported(t *testing.T) {
	m := NewManager(newStore(t), Options{
		SSHCommand: writeScript(t, `echo "deploy@host: Permission denied (publickey)." >&2; exit 255`),
	})
	defer m.Close()
	_, err := m.Test(context.Background(), store.Host{Name: "x", URL: "ssh://deploy@host"})
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Errorf("err = %v, want ssh's own message", err)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
