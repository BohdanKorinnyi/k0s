package hosts

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"dockpanel/internal/dockerapi"
	"dockpanel/internal/store"
)

// testTimeout bounds a connection test. A variable so tests can shorten it.
var testTimeout = 5 * time.Second

// Manager hands out one Docker client per saved host and reuses it.
type Manager struct {
	store *store.Store
	opts  Options

	mu      sync.Mutex
	clients map[string]*client.Client
}

// NewManager returns a manager for the hosts in st.
func NewManager(st *store.Store, o Options) *Manager {
	return &Manager{store: st, opts: o, clients: map[string]*client.Client{}}
}

// Client returns the client for a saved host, creating it on first use. It
// returns store.ErrNotFound for an unknown host.
func (m *Manager) Client(_ context.Context, hostID string) (dockerapi.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[hostID]; ok {
		return c, nil
	}
	h, ok := m.store.Host(hostID)
	if !ok {
		return nil, fmt.Errorf("host %q: %w", hostID, store.ErrNotFound)
	}
	c, err := Dial(h, m.opts)
	if err != nil {
		return nil, err
	}
	m.clients[hostID] = c
	return c, nil
}

// Invalidate drops the cached client for a host, after the host was edited or
// removed. The next Client call builds a fresh one.
func (m *Manager) Invalidate(hostID string) {
	m.mu.Lock()
	c, ok := m.clients[hostID]
	delete(m.clients, hostID)
	m.mu.Unlock()
	if ok {
		_ = c.Close()
	}
}

// Test connects to a host that need not be saved yet and returns the Docker
// version it reports. It gives up after a few seconds.
func (m *Manager) Test(ctx context.Context, h store.Host) (string, error) {
	c, err := Dial(h, m.opts)
	if err != nil {
		return "", err
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	v, err := c.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return "", err
	}
	return v.Version, nil
}

// Close closes every cached client.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.clients {
		_ = c.Close()
		delete(m.clients, id)
	}
	return nil
}
