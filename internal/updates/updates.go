// Package updates works out whether a newer image exists for a reference a
// host is running: it compares the digest the host has with the digest the
// registry serves now.
//
// The registry is asked through the host's own Docker daemon, so a registry
// reachable only from the host still works.
package updates

import (
	"context"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"dockpanel/internal/dockerapi"
	"dockpanel/internal/registryauth"
)

// Status is the outcome of a check for one image reference.
type Status string

const (
	UpToDate  Status = "up_to_date"
	Available Status = "update_available"
	// Unknown means the check could not decide; Result.Reason says why.
	Unknown Status = "unknown"
	// Pinned means the reference names one exact image, so there is nothing
	// to check.
	Pinned Status = "pinned"
)

// Reasons for an Unknown result.
const (
	// The host has no registry digest for the image: it was built here, or
	// loaded from a file, so there is nothing to compare with.
	ReasonBuiltLocally = "built or loaded locally"
	// The registry refused. It answers the same way for an image it does not
	// have as for one it will not show without a login, so both are named.
	ReasonNeedsLogin = "needs a registry login, or the image exists only on this host"
	ReasonNotOnHost  = "the image tag is no longer on this host"
)

// MaxAge is how long a result is trusted before the registry is asked again.
const MaxAge = 15 * time.Minute

// maxChecks bounds how many references are checked at once.
const maxChecks = 4

// Result is what is known about one image reference on one host.
type Result struct {
	Status Status
	Reason string
	// LocalImageID is the image the reference points to on the host.
	LocalImageID string
	CheckedAt    time.Time
}

// ContainerImage pairs the reference a container was created with and the
// image it actually runs.
type ContainerImage struct {
	Ref     string
	ImageID string
}

// Checker runs update checks and remembers the results per host.
type Checker struct {
	auth registryauth.Provider
	now  func() time.Time

	mu      sync.Mutex
	results map[string]map[string]Result // host ID → reference → result
	lastRun map[string]time.Time
	running map[string]bool
}

// NewChecker returns a Checker. auth may be nil.
func NewChecker(auth registryauth.Provider, now func() time.Time) *Checker {
	return &Checker{
		auth:    auth,
		now:     now,
		results: map[string]map[string]Result{},
		lastRun: map[string]time.Time{},
		running: map[string]bool{},
	}
}

// Check returns a result for each reference. A result younger than MaxAge is
// reused unless force is set.
func (c *Checker) Check(ctx context.Context, cli dockerapi.Client, hostID string, refs []string, force bool) map[string]Result {
	out := map[string]Result{}
	var todo []string
	c.mu.Lock()
	for _, ref := range refs {
		if _, seen := out[ref]; seen {
			continue
		}
		if r, ok := c.results[hostID][ref]; ok && !force && c.now().Sub(r.CheckedAt) < MaxAge {
			out[ref] = r
			continue
		}
		out[ref] = Result{}
		todo = append(todo, ref)
	}
	c.mu.Unlock()

	var (
		wg    sync.WaitGroup
		outMu sync.Mutex
	)
	slots := make(chan struct{}, maxChecks)
	for _, ref := range todo {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			r := c.checkOne(ctx, cli, ref)
			outMu.Lock()
			out[ref] = r
			outMu.Unlock()
		}()
	}
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.results[hostID] == nil {
		c.results[hostID] = map[string]Result{}
	}
	for _, ref := range todo {
		c.results[hostID][ref] = out[ref]
	}
	c.lastRun[hostID] = c.now()
	c.running[hostID] = false
	return out
}

func (c *Checker) checkOne(ctx context.Context, cli dockerapi.Client, ref string) Result {
	r := Result{CheckedAt: c.now()}
	if !registryauth.Pullable(ref) {
		r.Status = Pinned
		return r
	}
	unknown := func(reason string) Result {
		r.Status, r.Reason = Unknown, reason
		return r
	}

	img, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return unknown(ReasonNotOnHost)
		}
		return unknown("cannot read the image: " + firstLine(err.Error()))
	}
	r.LocalImageID = img.ID

	local := localDigests(ref, img.RepoDigests)
	if len(local) == 0 || !cameFromRegistry(ref, img.Identity) {
		return unknown(ReasonBuiltLocally)
	}

	auth, err := registryauth.Encoded(ctx, c.auth, ref)
	if err != nil {
		return unknown("registry login failed: " + firstLine(err.Error()))
	}
	remote, err := cli.DistributionInspect(ctx, ref, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
	if err != nil {
		if needsLogin(err) {
			return unknown(ReasonNeedsLogin)
		}
		return unknown("registry error: " + firstLine(err.Error()))
	}

	r.Status = Available
	if local[remote.Descriptor.Digest.String()] {
		r.Status = UpToDate
	}
	return r
}

// Cached returns the remembered result for a reference, however old.
func (c *Checker) Cached(hostID, ref string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.results[hostID][ref]
	return r, ok
}

// LastRun returns when a check last finished for the host, or the zero time.
func (c *Checker) LastRun(hostID string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRun[hostID]
}

// StartIfDue reports whether a background check should start now: the host's
// results are older than MaxAge and no check is already running. When it
// returns true the caller must run Check, or call Done if it cannot.
func (c *Checker) StartIfDue(hostID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[hostID] {
		return false
	}
	if last := c.lastRun[hostID]; !last.IsZero() && c.now().Sub(last) < MaxAge {
		return false
	}
	c.running[hostID] = true
	return true
}

// Done releases a start granted by StartIfDue that did not lead to a Check.
func (c *Checker) Done(hostID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[hostID] = false
}

// Invalidate drops what is remembered about some image references on a host.
// Call it when the images behind them may have changed, as after a pull: an
// answer about the old image must not be shown for the new one.
func (c *Checker) Invalidate(hostID string, refs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ref := range refs {
		delete(c.results[hostID], ref)
	}
}

// Forget drops everything remembered about a host, for when it is edited or
// removed.
func (c *Checker) Forget(hostID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.results, hostID)
	delete(c.lastRun, hostID)
	delete(c.running, hostID)
}

// ForService combines per-reference results into one status for a service.
// results must hold an entry for every reference its containers use.
//
// A service also counts as having an update when the tag on the host is
// current but one of its containers still runs an older image: the image was
// pulled, the container was not replaced.
func ForService(results map[string]Result, containers []ContainerImage) (Status, string) {
	for _, r := range results {
		if r.Status == Available {
			return Available, ""
		}
	}
	for _, c := range containers {
		r := results[c.Ref]
		if r.Status == UpToDate && r.LocalImageID != "" && c.ImageID != r.LocalImageID {
			return Available, ""
		}
	}
	current := false
	for _, r := range results {
		switch r.Status {
		case Unknown:
			return Unknown, r.Reason
		case UpToDate:
			current = true
		}
	}
	if current || len(results) == 0 {
		return UpToDate, ""
	}
	return Pinned, ""
}

// cameFromRegistry reports whether the image may have been pulled from the
// repository its reference names.
//
// Docker's newer image store gives every image a digest, including ones built
// or committed on the host, so a digest alone proves nothing. Where the daemon
// records how an image came to be, that settles it: pulled from this
// repository, or not. Where it records nothing (the classic store; an image
// committed from a container), the registry is asked.
func cameFromRegistry(ref string, origin *image.Identity) bool {
	if origin == nil {
		return true
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return false
	}
	for _, pull := range origin.Pull {
		if from, err := reference.ParseNormalizedNamed(pull.Repository); err == nil && from.Name() == named.Name() {
			return true
		}
	}
	return false
}

// localDigests returns the digests the host recorded for ref's repository.
// Docker lists them as "repository@digest"; an image tagged into several
// repositories has an entry for each, and only the matching one counts.
func localDigests(ref string, repoDigests []string) map[string]bool {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, rd := range repoDigests {
		parsed, err := reference.ParseNormalizedNamed(rd)
		if err != nil || parsed.Name() != named.Name() {
			continue
		}
		if d, ok := parsed.(reference.Digested); ok {
			out[d.Digest().String()] = true
		}
	}
	return out
}

// needsLogin recognises the ways a registry says "who are you?". Daemons relay
// registry errors as text as often as with a proper status, hence the wording
// checks.
func needsLogin(err error) bool {
	if cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"unauthorized", "authentication required", "denied", "no basic auth credentials"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const limit = 200
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}
