// Package registryauth supplies registry logins for pulls and update checks.
//
// Provider is the extension point: Static serves the fixed logins saved in the
// panel, and other kinds (short-lived cloud tokens, for instance) can be added
// as further implementations without touching the callers.
package registryauth

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
)

// Credential is a login for one registry.
type Credential struct {
	Address  string
	Username string
	Password string
}

// Provider produces the login for a registry host, such as "ghcr.io". It
// reports false when it has none; that is not an error, since public images
// need no login.
type Provider interface {
	Login(ctx context.Context, registryHost string) (Credential, bool, error)
}

// Static is a Provider backed by a list of fixed logins. List is called on
// every lookup, so changes to the saved logins take effect at once.
type Static struct {
	List func() []Credential
}

// Login returns the saved login whose address names the same registry.
func (s Static) Login(_ context.Context, registryHost string) (Credential, bool, error) {
	if s.List == nil {
		return Credential{}, false, nil
	}
	want := Normalize(registryHost)
	for _, c := range s.List() {
		if Normalize(c.Address) == want {
			return c, true, nil
		}
	}
	return Credential{}, false, nil
}

// dockerHubAliases are the names Docker Hub goes by.
var dockerHubAliases = map[string]bool{
	"docker.io":               true,
	"index.docker.io":         true,
	"registry-1.docker.io":    true,
	"registry.hub.docker.com": true,
}

// Normalize reduces a registry address as a person might type it
// ("https://index.docker.io/v1/", "GHCR.io/") to a bare lower-case host, with
// every Docker Hub alias mapped to "docker.io".
func Normalize(address string) string {
	a := strings.ToLower(strings.TrimSpace(address))
	if i := strings.Index(a, "://"); i >= 0 {
		a = a[i+3:]
	}
	if i := strings.IndexByte(a, '/'); i >= 0 {
		a = a[:i]
	}
	if dockerHubAliases[a] {
		return "docker.io"
	}
	return a
}

var imageIDPattern = regexp.MustCompile(`^(sha256:)?[0-9a-f]{64}$`)

// RegistryHost returns the registry an image reference points at: "docker.io"
// for "nginx", "ghcr.io" for "ghcr.io/acme/api:develop".
func RegistryHost(imageRef string) (string, error) {
	if imageIDPattern.MatchString(imageRef) {
		return "", errors.New("an image ID has no registry")
	}
	named, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return "", err
	}
	return reference.Domain(named), nil
}

// Pullable reports whether pulling the reference again could bring a newer
// image. A reference pinned to a digest, or a bare image ID, always means the
// same image.
func Pullable(imageRef string) bool {
	if imageIDPattern.MatchString(imageRef) {
		return false
	}
	named, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return false
	}
	_, pinned := named.(reference.Digested)
	return !pinned
}

// Encoded returns the login for the image's registry in the form the Docker
// API expects in its X-Registry-Auth header, or "" when there is none.
func Encoded(ctx context.Context, p Provider, imageRef string) (string, error) {
	if p == nil {
		return "", nil
	}
	host, err := RegistryHost(imageRef)
	if err != nil {
		return "", nil // nothing to log in to
	}
	c, ok, err := p.Login(ctx, host)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return authconfig.Encode(registry.AuthConfig{
		Username:      c.Username,
		Password:      c.Password,
		ServerAddress: c.Address,
	})
}
