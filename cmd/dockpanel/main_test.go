package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dockpanel/internal/config"
	"dockpanel/internal/store"
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
	dataFile := filepath.Join(dir, "data", "dockpanel.json")

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
	st := openStore(t, filepath.Join(dir, "dockpanel.json"))
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
	if !strings.Contains(w, "panel.lan") || !strings.Contains(w, "DOCKPANEL_PASSWORD") {
		t.Errorf("warning = %q, want it to name the hostname and the password setting", w)
	}
}
