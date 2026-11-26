package hosts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// manifestName lists the files a previous PrepareSSHDir copied, so that a
// later run can remove the ones that are gone from the source — and nothing
// else.
const manifestName = ".copied-by-dockpanel"

// maxSSHFile is the largest file worth copying. Keys and config are small.
const maxSSHFile = 1 << 20

// PrepareSSHDir copies SSH keys and configuration from a mounted folder into
// the folder ssh reads, owned by the current user and readable by nobody
// else. It returns how many files it copied.
//
// A folder mounted from the host keeps the host's ownership, and from Windows
// every file is world-readable; ssh refuses to use a config owned by someone
// else or a key others can read. Copying sidesteps both without asking anyone
// to change their own files.
//
// A missing source is not an error: nothing was mounted. Files in dst that
// this function did not put there are never removed.
func PrepareSSHDir(src, dst string) (int, error) {
	if src == "" {
		return 0, nil
	}
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return 0, nil
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	if err := os.Chmod(dst, 0o700); err != nil {
		return 0, err
	}

	previous := readManifest(dst)
	copied := map[string]bool{}

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == src {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, 0o700)
		}
		// Follow links, and take only ordinary files: an agent socket or a
		// link pointing outside the mount is of no use here.
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSSHFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if err := writePrivate(target, data); err != nil {
			return err
		}
		copied[rel] = true
		return nil
	})
	if err != nil {
		return len(copied), err
	}

	// What an earlier run copied and the mount no longer has: a key removed
	// on the host must not live on here.
	for _, rel := range previous {
		if !copied[rel] {
			_ = os.Remove(filepath.Join(dst, rel))
		}
	}
	names := make([]string, 0, len(copied))
	for rel := range copied {
		names = append(names, rel)
	}
	if err := writePrivate(filepath.Join(dst, manifestName), []byte(strings.Join(names, "\n"))); err != nil {
		return len(copied), err
	}
	return len(copied), nil
}

// writePrivate writes a file only its owner can read, replacing any existing
// one in a single step.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

func readManifest(dst string) []string {
	data, err := os.ReadFile(filepath.Join(dst, manifestName))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		// Only plain relative paths: the manifest must never be able to name
		// a file outside dst.
		if line == "" || filepath.IsAbs(line) || strings.Contains(line, "..") {
			continue
		}
		out = append(out, line)
	}
	return out
}
