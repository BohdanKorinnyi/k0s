package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "dc0s.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, path
}

func mustSaveHost(t *testing.T, s *Store, h Host) Host {
	t.Helper()
	saved, err := s.SaveHost(h)
	if err != nil {
		t.Fatalf("SaveHost(%+v): %v", h, err)
	}
	return saved
}

func TestHostIDs(t *testing.T) {
	s, _ := open(t)
	cases := []struct{ name, wantID string }{
		{"AWS Prod", "aws-prod"},
		{"!!!", "host"},
		{"a b", "a-b"},
		{"a-b", "a-b-2"},
		{"  gcp_staging.eu  ", "gcp-staging-eu"},
	}
	for _, c := range cases {
		h := mustSaveHost(t, s, Host{Name: c.name, URL: "tcp://10.0.0.1:2376"})
		if h.ID != c.wantID {
			t.Errorf("name %q: ID = %q, want %q", c.name, h.ID, c.wantID)
		}
	}
}

func TestSaveHostTrimsNameAndURL(t *testing.T) {
	s, _ := open(t)
	h := mustSaveHost(t, s, Host{Name: "  aws  ", URL: " ssh://deploy@aws.example.com "})
	if h.Name != "aws" || h.URL != "ssh://deploy@aws.example.com" {
		t.Errorf("got %+v, want trimmed name and URL", h)
	}
}

func TestDuplicateHostName(t *testing.T) {
	s, _ := open(t)
	mustSaveHost(t, s, Host{Name: "aws", URL: "tcp://a:1"})
	if _, err := s.SaveHost(Host{Name: "AWS", URL: "tcp://b:1"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
	if n := len(s.Hosts()); n != 1 {
		t.Errorf("hosts = %d, want 1", n)
	}
}

func TestHostRequiresNameAndURL(t *testing.T) {
	s, _ := open(t)
	for _, h := range []Host{{Name: "", URL: "tcp://a:1"}, {Name: "   ", URL: "tcp://a:1"}, {Name: "aws", URL: ""}} {
		if _, err := s.SaveHost(h); !errors.Is(err, ErrInvalid) {
			t.Errorf("SaveHost(%+v): err = %v, want ErrInvalid", h, err)
		}
	}
}

func TestUpdateKeepsID(t *testing.T) {
	s, _ := open(t)
	h := mustSaveHost(t, s, Host{Name: "AWS Prod", URL: "tcp://a:1"})
	h.Name = "Production"
	h.Protected = true
	updated := mustSaveHost(t, s, h)
	if updated.ID != "aws-prod" || updated.Name != "Production" || !updated.Protected {
		t.Errorf("updated = %+v", updated)
	}
	if got, ok := s.Host("aws-prod"); !ok || got.Name != "Production" {
		t.Errorf("Host(aws-prod) = %+v, %v", got, ok)
	}
	// Renaming a host to its own name in another case is not a duplicate.
	updated.Name = "PRODUCTION"
	if _, err := s.SaveHost(updated); err != nil {
		t.Errorf("renaming to own name in another case: %v", err)
	}
}

func TestWriteOnlySecrets(t *testing.T) {
	s, _ := open(t)
	h := mustSaveHost(t, s, Host{Name: "tls", URL: "tcp://a:2376", TLS: &TLS{CA: "ca1", Cert: "cert1", Key: "key1"}})

	// A blank key on update keeps the stored key.
	h.TLS = &TLS{CA: "ca2", Cert: "cert2", Key: ""}
	got := mustSaveHost(t, s, h)
	if got.TLS == nil || got.TLS.Key != "key1" || got.TLS.CA != "ca2" || got.TLS.Cert != "cert2" {
		t.Errorf("after blank-key update TLS = %+v, want key1 kept with ca2/cert2", got.TLS)
	}

	// A nil TLS on update removes it.
	h.TLS = nil
	got = mustSaveHost(t, s, h)
	if got.TLS != nil {
		t.Errorf("after nil update TLS = %+v, want nil", got.TLS)
	}

	r, err := s.SaveRegistry(Registry{Address: "ghcr.io", Username: "bot", Password: "pw1"})
	if err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	r.Username, r.Password = "bot2", ""
	got2, err := s.SaveRegistry(r)
	if err != nil {
		t.Fatalf("SaveRegistry update: %v", err)
	}
	if got2.Password != "pw1" || got2.Username != "bot2" {
		t.Errorf("after blank-password update = %+v, want pw1 kept", got2)
	}
}

func TestReturnedValuesAreCopies(t *testing.T) {
	s, _ := open(t)
	h := mustSaveHost(t, s, Host{Name: "tls", URL: "tcp://a:2376", TLS: &TLS{Key: "key1"}})
	h.TLS.Key = "tampered"
	s.Hosts()[0].Name = "tampered"
	got, _ := s.Host(h.ID)
	if got.TLS.Key != "key1" || got.Name != "tls" {
		t.Errorf("stored host changed through a returned value: %+v / %+v", got, got.TLS)
	}
}

func TestRegistries(t *testing.T) {
	s, _ := open(t)
	r, err := s.SaveRegistry(Registry{Address: " ghcr.io ", Username: "bot", Password: "pw"})
	if err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	if len(r.ID) != 8 || strings.Trim(r.ID, "0123456789abcdef") != "" {
		t.Errorf("ID = %q, want 8 hex characters", r.ID)
	}
	if r.Address != "ghcr.io" {
		t.Errorf("Address = %q, want trimmed", r.Address)
	}
	if _, err := s.SaveRegistry(Registry{Address: "GHCR.io", Username: "x", Password: "y"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate address: err = %v, want ErrDuplicate", err)
	}
	if _, err := s.SaveRegistry(Registry{Address: "", Username: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty address: err = %v, want ErrInvalid", err)
	}
	if err := s.DeleteRegistry(r.ID); err != nil {
		t.Errorf("DeleteRegistry: %v", err)
	}
	if n := len(s.Registries()); n != 0 {
		t.Errorf("registries = %d, want 0", n)
	}
}

func TestPermissionsAndReload(t *testing.T) {
	s, path := open(t)
	if !s.IsNew() {
		t.Error("IsNew = false on a path that did not exist")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file exists before the first save (stat err = %v)", err)
	}
	mustSaveHost(t, s, Host{Name: "aws", URL: "ssh://deploy@aws.example.com", Protected: true,
		TLS: &TLS{CA: "ca", Cert: "cert", Key: "key"}})
	if _, err := s.SaveRegistry(Registry{Address: "ghcr.io", Username: "bot", Password: "pw"}); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", di.Mode().Perm())
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if again.IsNew() {
		t.Error("IsNew = true after reopening an existing file")
	}
	hosts, regs := again.Hosts(), again.Registries()
	if len(hosts) != 1 || hosts[0].ID != "aws" || !hosts[0].Protected || hosts[0].TLS == nil || hosts[0].TLS.Key != "key" {
		t.Errorf("reloaded hosts = %+v", hosts)
	}
	if len(regs) != 1 || regs[0].Address != "ghcr.io" || regs[0].Password != "pw" {
		t.Errorf("reloaded registries = %+v", regs)
	}

	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dc0s.json")
	const garbage = "{not json"
	if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if err == nil {
		t.Fatal("Open succeeded on a corrupt file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != garbage {
		t.Errorf("corrupt file was modified: %q", b)
	}
}

func TestEmptyFileIsANewStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dc0s.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on an empty file: %v", err)
	}
	if !s.IsNew() || len(s.Hosts()) != 0 {
		t.Errorf("IsNew = %v, hosts = %v; want a new, empty store", s.IsNew(), s.Hosts())
	}
}

func TestNotFound(t *testing.T) {
	s, _ := open(t)
	if err := s.DeleteHost("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteHost: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteRegistry("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteRegistry: err = %v, want ErrNotFound", err)
	}
	if _, err := s.SaveHost(Host{ID: "nope", Name: "x", URL: "tcp://a:1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SaveHost unknown ID: err = %v, want ErrNotFound", err)
	}
	if _, err := s.SaveRegistry(Registry{ID: "nope", Address: "ghcr.io"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SaveRegistry unknown ID: err = %v, want ErrNotFound", err)
	}
	if _, ok := s.Host("nope"); ok {
		t.Error("Host(nope) reported found")
	}
}

func TestDeleteHost(t *testing.T) {
	s, path := open(t)
	a := mustSaveHost(t, s, Host{Name: "a", URL: "tcp://a:1"})
	mustSaveHost(t, s, Host{Name: "b", URL: "tcp://b:1"})
	if err := s.DeleteHost(a.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if hosts := again.Hosts(); len(hosts) != 1 || hosts[0].ID != "b" {
		t.Errorf("hosts after delete and reload = %+v, want only b", hosts)
	}
}

func TestConcurrentSaves(t *testing.T) {
	s, path := open(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SaveHost(Host{Name: fmt.Sprintf("host %d", i), URL: "tcp://a:1"}); err != nil {
				t.Errorf("SaveHost %d: %v", i, err)
			}
			_ = s.Hosts()
		}()
	}
	wg.Wait()
	if n := len(s.Hosts()); n != 20 {
		t.Errorf("hosts = %d, want 20", n)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Hosts()); n != 20 {
		t.Errorf("hosts after reload = %d, want 20", n)
	}
}
