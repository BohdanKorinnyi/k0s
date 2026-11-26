package api

import (
	"context"
	"fmt"
	"sync"

	"dockpanel/internal/dockerapi"
	"dockpanel/internal/dockerapi/dockertest"
	"dockpanel/internal/store"
)

// fakeHosts stands in for hosts.Manager: each host ID maps to a fake daemon.
type fakeHosts struct {
	mu          sync.Mutex
	daemons     map[string]*dockertest.Fake
	invalidated []string
	// testResult is what Test returns; testSeen records the host it was given.
	testVersion string
	testErr     error
	testSeen    store.Host
}

func newFakeHosts() *fakeHosts {
	return &fakeHosts{daemons: map[string]*dockertest.Fake{}, testVersion: "27.0"}
}

// daemon returns the fake daemon behind a host ID, creating it on first use.
func (f *fakeHosts) daemon(hostID string) *dockertest.Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.daemons[hostID]
	if !ok {
		d = dockertest.New()
		f.daemons[hostID] = d
	}
	return d
}

func (f *fakeHosts) Client(_ context.Context, hostID string) (dockerapi.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.daemons[hostID]
	if !ok {
		return nil, fmt.Errorf("host %q: %w", hostID, store.ErrNotFound)
	}
	return d, nil
}

func (f *fakeHosts) Test(_ context.Context, h store.Host) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testSeen = h
	return f.testVersion, f.testErr
}

func (f *fakeHosts) Invalidate(hostID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, hostID)
}

func (f *fakeHosts) invalidatedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.invalidated...)
}
