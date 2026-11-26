package hosts

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to the umask; set the mode outright.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// Keys mounted from the host arrive with the host's owner and, from Windows,
// readable by everyone. ssh refuses both. A private copy with the right owner
// and modes is what ssh is pointed at.
func TestPrepareSSHDir(t *testing.T) {
	src, dst := filepath.Join(t.TempDir(), "mounted"), filepath.Join(t.TempDir(), "home", ".ssh")
	write(t, filepath.Join(src, "id_ed25519"), "PRIVATE KEY", 0o644) // too open, as from a Windows mount
	write(t, filepath.Join(src, "id_ed25519.pub"), "ssh-ed25519 AAAA", 0o644)
	write(t, filepath.Join(src, "config"), "Host aws\n  User deploy\n", 0o664)
	write(t, filepath.Join(src, "known_hosts"), "aws ssh-ed25519 AAAA", 0o644)
	write(t, filepath.Join(src, "config.d", "work"), "Host gcp\n", 0o644)

	n, err := PrepareSSHDir(src, dst)
	if err != nil {
		t.Fatalf("PrepareSSHDir: %v", err)
	}
	if n != 5 {
		t.Errorf("copied %d files, want 5", n)
	}
	for _, name := range []string{"id_ed25519", "id_ed25519.pub", "config", "known_hosts", filepath.Join("config.d", "work")} {
		want, _ := os.ReadFile(filepath.Join(src, name))
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || string(got) != string(want) {
			t.Errorf("%s: content %q, %v; want %q", name, got, err, want)
		}
		if m := mode(t, filepath.Join(dst, name)); m != 0o600 {
			t.Errorf("%s: mode %o, want 600", name, m)
		}
	}
	for _, dir := range []string{dst, filepath.Join(dst, "config.d")} {
		if m := mode(t, dir); m != 0o700 {
			t.Errorf("%s: mode %o, want 700", dir, m)
		}
	}
	// The mounted folder itself is left exactly as it was.
	if m := mode(t, filepath.Join(src, "id_ed25519")); m != 0o644 {
		t.Errorf("the source key's mode changed to %o", m)
	}
}

func TestPrepareSSHDirWithNothingMounted(t *testing.T) {
	dst := filepath.Join(t.TempDir(), ".ssh")
	n, err := PrepareSSHDir(filepath.Join(t.TempDir(), "absent"), dst)
	if n != 0 || err != nil {
		t.Errorf("PrepareSSHDir = %d, %v; want nothing done and no error", n, err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a folder was created although nothing was mounted (stat err = %v)", err)
	}
	if n, err := PrepareSSHDir("", dst); n != 0 || err != nil {
		t.Errorf("empty source: %d, %v", n, err)
	}
}

func TestPrepareSSHDirSkipsWhatIsNotAFile(t *testing.T) {
	src, dst := filepath.Join(t.TempDir(), "mounted"), filepath.Join(t.TempDir(), ".ssh")
	write(t, filepath.Join(src, "id_rsa"), "KEY", 0o600)
	// An agent socket's leftover link, or a link to something outside the mount.
	if err := os.Symlink(filepath.Join(src, "gone"), filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	n, err := PrepareSSHDir(src, dst)
	if err != nil || n != 1 {
		t.Errorf("PrepareSSHDir = %d, %v; want the one real file copied and the broken link skipped", n, err)
	}
}

// A second start replaces the copy, so a key removed on the host is gone here too.
func TestPrepareSSHDirRefreshesTheCopy(t *testing.T) {
	src, dst := filepath.Join(t.TempDir(), "mounted"), filepath.Join(t.TempDir(), ".ssh")
	write(t, filepath.Join(src, "old_key"), "OLD", 0o600)
	if _, err := PrepareSSHDir(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "old_key")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "new_key"), "NEW", 0o600)
	if _, err := PrepareSSHDir(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "old_key")); !os.IsNotExist(err) {
		t.Error("a key that was removed from the mount is still in the copy")
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "new_key")); string(got) != "NEW" {
		t.Errorf("new_key = %q", got)
	}
}
