// Package store keeps the panel's saved hosts and registry logins in one JSON
// file. There is no database: the file is small, readable, and easy to back up.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// fileData is the layout of the data file.
type fileData struct {
	Version    int        `json:"version"`
	Hosts      []Host     `json:"hosts"`
	Registries []Registry `json:"registries"`
}

const fileVersion = 1

// Store is safe for use from several goroutines.
type Store struct {
	path  string
	isNew bool

	mu   sync.RWMutex
	data fileData
}

// Open reads the data file at path. A missing or empty file is a new store;
// nothing is written until the first save. A file that cannot be understood is
// an error and is left exactly as it was, so a mistake while editing by hand
// never costs the saved hosts.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	s := &Store{path: path, data: fileData{Version: fileVersion}}

	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.isNew = true
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		s.isNew = true
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("%s is not a valid data file (%v); fix or remove it and start again", path, err)
	}
	if s.data.Version > fileVersion {
		return nil, fmt.Errorf("%s was written by a newer version (file version %d, this program understands %d)",
			path, s.data.Version, fileVersion)
	}
	s.data.Version = fileVersion
	return s, nil
}

// IsNew reports whether there was no saved data when the store was opened.
func (s *Store) IsNew() bool { return s.isNew }

// Hosts returns the saved hosts in the order they were added.
func (s *Store) Hosts() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, len(s.data.Hosts))
	for i, h := range s.data.Hosts {
		out[i] = h.clone()
	}
	return out
}

// Host returns the host with the given ID.
func (s *Store) Host(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.data.Hosts {
		if h.ID == id {
			return h.clone(), true
		}
	}
	return Host{}, false
}

// SaveHost adds a host when h.ID is empty and updates it otherwise.
//
// On update, a TLS section with an empty Key keeps the stored key, so the key
// never has to be sent back to the browser; a nil TLS removes the section.
func (s *Store) SaveHost(h Host) (Host, error) {
	h = h.clone()
	h.Name = strings.TrimSpace(h.Name)
	h.URL = strings.TrimSpace(h.URL)
	if h.Name == "" {
		return Host{}, fmt.Errorf("%w: a host needs a name", ErrInvalid)
	}
	if h.URL == "" {
		return Host{}, fmt.Errorf("%w: a host needs a connection URL", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	hosts := append([]Host(nil), s.data.Hosts...)
	for _, other := range hosts {
		if other.ID != h.ID && strings.EqualFold(other.Name, h.Name) {
			return Host{}, fmt.Errorf("%w: a host named %q", ErrDuplicate, other.Name)
		}
	}

	if h.ID == "" {
		h.ID = uniqueSlug(h.Name, hosts)
		hosts = append(hosts, h)
	} else {
		i := indexOfHost(hosts, h.ID)
		if i < 0 {
			return Host{}, fmt.Errorf("host %q: %w", h.ID, ErrNotFound)
		}
		if h.TLS != nil && h.TLS.Key == "" && hosts[i].TLS != nil {
			h.TLS.Key = hosts[i].TLS.Key
		}
		hosts[i] = h
	}

	next := s.data
	next.Hosts = hosts
	if err := s.commit(next); err != nil {
		return Host{}, err
	}
	return h.clone(), nil
}

// DeleteHost removes a host.
func (s *Store) DeleteHost(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := indexOfHost(s.data.Hosts, id)
	if i < 0 {
		return fmt.Errorf("host %q: %w", id, ErrNotFound)
	}
	next := s.data
	next.Hosts = append(append([]Host(nil), s.data.Hosts[:i]...), s.data.Hosts[i+1:]...)
	return s.commit(next)
}

// Registries returns the saved registry logins in the order they were added.
func (s *Store) Registries() []Registry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Registry(nil), s.data.Registries...)
}

// SaveRegistry adds a login when r.ID is empty and updates it otherwise. On
// update an empty Password keeps the stored one.
func (s *Store) SaveRegistry(r Registry) (Registry, error) {
	r.Address = strings.TrimSpace(r.Address)
	r.Username = strings.TrimSpace(r.Username)
	if r.Address == "" {
		return Registry{}, fmt.Errorf("%w: a registry login needs an address", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	regs := append([]Registry(nil), s.data.Registries...)
	for _, other := range regs {
		if other.ID != r.ID && strings.EqualFold(other.Address, r.Address) {
			return Registry{}, fmt.Errorf("%w: a login for %q", ErrDuplicate, other.Address)
		}
	}

	if r.ID == "" {
		id, err := randomID()
		if err != nil {
			return Registry{}, err
		}
		r.ID = id
		regs = append(regs, r)
	} else {
		i := indexOfRegistry(regs, r.ID)
		if i < 0 {
			return Registry{}, fmt.Errorf("registry login %q: %w", r.ID, ErrNotFound)
		}
		if r.Password == "" {
			r.Password = regs[i].Password
		}
		regs[i] = r
	}

	next := s.data
	next.Registries = regs
	if err := s.commit(next); err != nil {
		return Registry{}, err
	}
	return r, nil
}

// DeleteRegistry removes a registry login.
func (s *Store) DeleteRegistry(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := indexOfRegistry(s.data.Registries, id)
	if i < 0 {
		return fmt.Errorf("registry login %q: %w", id, ErrNotFound)
	}
	next := s.data
	next.Registries = append(append([]Registry(nil), s.data.Registries[:i]...), s.data.Registries[i+1:]...)
	return s.commit(next)
}

// commit writes next to disk and, only if that worked, makes it current. The
// caller holds the write lock.
func (s *Store) commit(next fileData) error {
	if next.Hosts == nil {
		next.Hosts = []Host{}
	}
	if next.Registries == nil {
		next.Registries = []Registry{}
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encode data file: %w", err)
	}
	if err := writeFileAtomic(s.path, append(b, '\n')); err != nil {
		return fmt.Errorf("save %s: %w", s.path, err)
	}
	s.data = next
	return nil
}

// writeFileAtomic replaces path with data in one step, so a crash mid-write
// leaves either the old file or the new one, never half of each. The file is
// readable only by its owner: it holds registry passwords and TLS keys.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dc0s-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func indexOfHost(hosts []Host, id string) int {
	for i, h := range hosts {
		if h.ID == id {
			return i
		}
	}
	return -1
}

func indexOfRegistry(regs []Registry, id string) int {
	for i, r := range regs {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// slug turns a name into something safe in a URL: lower-case letters and
// digits, with every run of anything else collapsed to one dash.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "host"
	}
	return b.String()
}

func uniqueSlug(name string, hosts []Host) string {
	base := slug(name)
	candidate := base
	for n := 2; indexOfHost(hosts, candidate) >= 0; n++ {
		candidate = base + "-" + strconv.Itoa(n)
	}
	return candidate
}

func randomID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
