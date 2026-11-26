package store

import "errors"

// Host is one Docker daemon the panel can talk to.
type Host struct {
	// ID is a slug made from the first name the host was given. It appears in
	// URLs and never changes, even when the host is renamed.
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the connection string, as in DOCKER_HOST: unix://, tcp:// or ssh://.
	URL string `json:"url"`
	// TLS is set for tcp:// hosts that require client certificates.
	TLS *TLS `json:"tls,omitempty"`
	// Protected hosts ask for the service name to be typed before any action.
	Protected bool `json:"protected"`
}

// TLS holds certificates as PEM text.
type TLS struct {
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

// Registry is a saved login for an image registry.
type Registry struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"`
}

var (
	// ErrNotFound means no host or registry has the given ID.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate means the name or address is already taken.
	ErrDuplicate = errors.New("already exists")
	// ErrInvalid means a required field is missing.
	ErrInvalid = errors.New("invalid")
)

func (h Host) clone() Host {
	if h.TLS != nil {
		t := *h.TLS
		h.TLS = &t
	}
	return h
}
