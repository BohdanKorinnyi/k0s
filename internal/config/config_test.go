package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func noFiles(string) ([]byte, error) { return nil, errors.New("no such file") }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil), noFiles)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", c.Addr)
	}
	if c.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", c.DataDir)
	}
	if want := []string{"localhost", "127.0.0.1", "::1"}; !reflect.DeepEqual(c.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %v, want %v", c.AllowedHosts, want)
	}
	if c.HealthTimeout != 2*time.Minute {
		t.Errorf("HealthTimeout = %v, want 2m", c.HealthTimeout)
	}
	if c.LocalSocket != "/var/run/docker.sock" {
		t.Errorf("LocalSocket = %q", c.LocalSocket)
	}
	if c.Password != "" {
		t.Errorf("Password = %q, want empty", c.Password)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"DOCKPANEL_ADDR":           "127.0.0.1:9000",
		"DOCKPANEL_DATA_DIR":       "/tmp/dp",
		"DOCKPANEL_PASSWORD":       "hunter2",
		"DOCKPANEL_HEALTH_TIMEOUT": "45s",
		"DOCKPANEL_LOCAL_SOCKET":   "/run/user/1000/docker.sock",
	}), noFiles)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "127.0.0.1:9000" || c.DataDir != "/tmp/dp" || c.Password != "hunter2" ||
		c.HealthTimeout != 45*time.Second || c.LocalSocket != "/run/user/1000/docker.sock" {
		t.Errorf("overrides not applied: %+v", c)
	}
}

func TestLoadAllowedHosts(t *testing.T) {
	c, err := Load(env(map[string]string{"DOCKPANEL_ALLOWED_HOSTS": " panel.lan, Box.Local ,,"}), noFiles)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"localhost", "127.0.0.1", "::1", "panel.lan", "box.local"}
	if !reflect.DeepEqual(c.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %v, want %v", c.AllowedHosts, want)
	}
}

func TestLoadPasswordFile(t *testing.T) {
	read := func(p string) ([]byte, error) {
		if p != "/run/secrets/pw" {
			t.Errorf("read %q, want /run/secrets/pw", p)
		}
		return []byte("s3cret\n"), nil
	}
	c, err := Load(env(map[string]string{"DOCKPANEL_PASSWORD_FILE": "/run/secrets/pw"}), read)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Password != "s3cret" {
		t.Errorf("Password = %q, want s3cret", c.Password)
	}
}

func TestLoadPasswordFileUnreadable(t *testing.T) {
	_, err := Load(env(map[string]string{"DOCKPANEL_PASSWORD_FILE": "/nope"}), noFiles)
	if err == nil || !strings.Contains(err.Error(), "DOCKPANEL_PASSWORD_FILE") {
		t.Errorf("err = %v, want one naming DOCKPANEL_PASSWORD_FILE", err)
	}
}

func TestLoadPasswordAndFileConflict(t *testing.T) {
	_, err := Load(env(map[string]string{
		"DOCKPANEL_PASSWORD":      "a",
		"DOCKPANEL_PASSWORD_FILE": "/run/secrets/pw",
	}), noFiles)
	if err == nil {
		t.Fatal("want an error when both are set")
	}
	for _, name := range []string{"DOCKPANEL_PASSWORD", "DOCKPANEL_PASSWORD_FILE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestLoadBadDuration(t *testing.T) {
	for _, v := range []string{"soon", "-5s", "0s"} {
		_, err := Load(env(map[string]string{"DOCKPANEL_HEALTH_TIMEOUT": v}), noFiles)
		if err == nil || !strings.Contains(err.Error(), "DOCKPANEL_HEALTH_TIMEOUT") {
			t.Errorf("HEALTH_TIMEOUT=%q: err = %v, want one naming DOCKPANEL_HEALTH_TIMEOUT", v, err)
		}
	}
}
