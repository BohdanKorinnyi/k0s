package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dc0s/internal/config"
	"dc0s/internal/store"
)

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLocalHostAdded(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "docker.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(dir, "data", "dc0s.json")

	// First run with the socket mounted: the first screen is not empty.
	st := openStore(t, dataFile)
	added, err := addLocalHost(st, socket)
	if err != nil || !added {
		t.Fatalf("addLocalHost = %v, %v; want true", added, err)
	}
	hosts := st.Hosts()
	if len(hosts) != 1 || hosts[0].ID != "local" || hosts[0].Name != "local" || hosts[0].URL != "unix://"+socket || hosts[0].Protected {
		t.Errorf("hosts = %+v", hosts)
	}

	// A later run leaves the saved hosts alone, even if "local" was removed.
	if err := st.DeleteHost("local"); err != nil {
		t.Fatal(err)
	}
	again := openStore(t, dataFile)
	if added, err := addLocalHost(again, socket); err != nil || added {
		t.Errorf("second run: addLocalHost = %v, %v; want false", added, err)
	}
	if n := len(again.Hosts()); n != 0 {
		t.Errorf("hosts after the second run = %d, want the deletion respected", n)
	}
}

func TestLocalHostNotAddedWithoutSocket(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, filepath.Join(dir, "dc0s.json"))
	added, err := addLocalHost(st, filepath.Join(dir, "no-such.sock"))
	if err != nil || added {
		t.Errorf("addLocalHost = %v, %v; want false when the socket is not mounted", added, err)
	}
	if n := len(st.Hosts()); n != 0 {
		t.Errorf("hosts = %d, want none", n)
	}
}

func TestExposureWarning(t *testing.T) {
	defaults := append([]string(nil), config.DefaultAllowedHosts...)

	if w := exposureWarning(config.Config{AllowedHosts: defaults}); w != "" {
		t.Errorf("default setup warned: %q", w)
	}
	if w := exposureWarning(config.Config{AllowedHosts: append(defaults, "panel.lan"), Password: "x"}); w != "" {
		t.Errorf("extra hostname with a password warned: %q", w)
	}
	w := exposureWarning(config.Config{AllowedHosts: append(defaults, "panel.lan")})
	if !strings.Contains(w, "panel.lan") || !strings.Contains(w, "DC0S_PASSWORD") {
		t.Errorf("warning = %q, want it to name the hostname and the password setting", w)
	}
}

// The panel finds the ID of the container it runs in, so that it can refuse to
// replace itself.
func TestSelfContainerID(t *testing.T) {
	const id = "0f7d86e0c5493b2c8a1e4d5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7"
	// What /proc/self/mountinfo holds inside a container: Docker mounts a few
	// files from the container's own folder.
	mountinfo := "1234 1200 254:1 /docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/vda1 rw\n" +
		"1235 1200 254:1 /docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw\n"

	if got := selfContainerID(mountinfo, "whatever"); got != id {
		t.Errorf("from mountinfo: %q, want the full ID", got)
	}
	// Failing that, a container's hostname is its short ID unless someone set one.
	if got := selfContainerID("", id[:12]); got != id[:12] {
		t.Errorf("from the hostname: %q, want %q", got, id[:12])
	}
	// Not in a container, or a hostname someone chose: unknown.
	for _, hostname := range []string{"my-laptop", "panel", "", "ABCDEF123456"} {
		if got := selfContainerID("42 1 0:1 / / rw - ext4 /dev/sda1 rw\n", hostname); got != "" {
			t.Errorf("hostname %q: got %q, want unknown", hostname, got)
		}
	}
}
