package updates

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/image"

	"dc0s/internal/dockerapi/dockertest"
	"dc0s/internal/registryauth"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hex64   = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

var ctx = context.Background()

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)} }

// localImage puts an image on the fake host as if it had been pulled from repo
// with the given digest.
func localImage(f *dockertest.Fake, ref, id, repo, dgst string) {
	img := image.InspectResponse{ID: id}
	if dgst != "" {
		img.RepoDigests = []string{repo + "@" + dgst}
	}
	f.AddImage(ref, img)
}

func check(c *Checker, f *dockertest.Fake, ref string) Result {
	return c.Check(ctx, f, "aws", []string{ref}, false)[ref]
}

func TestCheckStatuses(t *testing.T) {
	const ref = "ghcr.io/acme/api:develop"
	const repo = "ghcr.io/acme/api"

	t.Run("same digest", func(t *testing.T) {
		f := dockertest.New()
		localImage(f, ref, "sha256:img1", repo, digestA)
		f.SetRemoteDigest(ref, digestA)
		r := check(NewChecker(nil, newClock().now), f, ref)
		if r.Status != UpToDate || r.LocalImageID != "sha256:img1" || r.Reason != "" {
			t.Errorf("result = %+v", r)
		}
	})

	t.Run("different digest", func(t *testing.T) {
		f := dockertest.New()
		localImage(f, ref, "sha256:img1", repo, digestA)
		f.SetRemoteDigest(ref, digestB)
		if r := check(NewChecker(nil, newClock().now), f, ref); r.Status != Available {
			t.Errorf("result = %+v, want update_available", r)
		}
	})

	t.Run("several local digests, one matches", func(t *testing.T) {
		f := dockertest.New()
		f.AddImage(ref, image.InspectResponse{ID: "sha256:img1", RepoDigests: []string{
			"ghcr.io/other/thing@" + digestB, // another repository: must be ignored
			repo + "@" + digestA,
		}})
		f.SetRemoteDigest(ref, digestA)
		if r := check(NewChecker(nil, newClock().now), f, ref); r.Status != UpToDate {
			t.Errorf("result = %+v, want up_to_date", r)
		}
	})

	t.Run("docker hub short names", func(t *testing.T) {
		f := dockertest.New()
		// Docker reports Hub digests under the short name.
		localImage(f, "nginx:1.27", "sha256:img1", "nginx", digestA)
		f.SetRemoteDigest("nginx:1.27", digestA)
		if r := check(NewChecker(nil, newClock().now), f, "nginx:1.27"); r.Status != UpToDate {
			t.Errorf("result = %+v, want up_to_date", r)
		}
	})

	t.Run("built locally", func(t *testing.T) {
		f := dockertest.New()
		localImage(f, "myapp:dev", "sha256:img1", "", "")
		r := check(NewChecker(nil, newClock().now), f, "myapp:dev")
		if r.Status != Unknown || r.Reason != ReasonBuiltLocally || !strings.Contains(r.Reason, "locally") {
			t.Errorf("result = %+v, want unknown / %q", r, ReasonBuiltLocally)
		}
		if f.Count("DistributionInspect") != 0 {
			t.Error("asked the registry about an image that was never pulled")
		}
	})

	t.Run("registry wants a login", func(t *testing.T) {
		for _, err := range []error{
			fmt.Errorf("unauthorized: %w", cerrdefs.ErrUnauthenticated),
			fmt.Errorf("denied: %w", cerrdefs.ErrPermissionDenied),
			errors.New("errors:\ndenied: requested access to the resource is denied\nunauthorized: authentication required"),
		} {
			f := dockertest.New()
			localImage(f, ref, "sha256:img1", repo, digestA)
			f.SetRemoteError(ref, err)
			r := check(NewChecker(nil, newClock().now), f, ref)
			if r.Status != Unknown || r.Reason != ReasonNeedsLogin {
				t.Errorf("%v: result = %+v, want unknown / %q", err, r, ReasonNeedsLogin)
			}
		}
	})

	t.Run("registry error", func(t *testing.T) {
		f := dockertest.New()
		localImage(f, ref, "sha256:img1", repo, digestA)
		f.SetRemoteError(ref, errors.New("dial tcp: i/o timeout"))
		r := check(NewChecker(nil, newClock().now), f, ref)
		if r.Status != Unknown || !strings.HasPrefix(r.Reason, "registry error") || !strings.Contains(r.Reason, "i/o timeout") {
			t.Errorf("result = %+v, want unknown / registry error: …", r)
		}
	})

	t.Run("tag no longer on the host", func(t *testing.T) {
		f := dockertest.New()
		r := check(NewChecker(nil, newClock().now), f, ref)
		if r.Status != Unknown || r.Reason == "" {
			t.Errorf("result = %+v, want unknown with a reason", r)
		}
	})

	t.Run("pinned", func(t *testing.T) {
		f := dockertest.New()
		c := NewChecker(nil, newClock().now)
		for _, pinned := range []string{repo + "@sha256:" + hex64, "sha256:" + hex64} {
			if r := check(c, f, pinned); r.Status != Pinned {
				t.Errorf("%s: result = %+v, want pinned", pinned, r)
			}
		}
		if n := f.Count("ImageInspect") + f.Count("DistributionInspect"); n != 0 {
			t.Errorf("made %d Docker calls for pinned references, want none", n)
		}
	})
}

// Docker's newer image store gives every image a digest, including ones that
// were only ever built on the host, so "has a digest" no longer means "came
// from a registry". Where Docker says how an image came to be, that decides.
func TestCheckUsesWhereTheImageCameFrom(t *testing.T) {
	const ref = "ghcr.io/acme/api:develop"
	withDigest := func(identity *image.Identity) image.InspectResponse {
		return image.InspectResponse{ID: "sha256:img1", RepoDigests: []string{"ghcr.io/acme/api@" + digestA}, Identity: identity}
	}

	t.Run("built on the host", func(t *testing.T) {
		f := dockertest.New()
		f.AddImage(ref, withDigest(&image.Identity{Build: []image.BuildIdentity{{Ref: "abc"}}}))
		r := check(NewChecker(nil, newClock().now), f, ref)
		if r.Status != Unknown || r.Reason != ReasonBuiltLocally {
			t.Errorf("result = %+v, want unknown / %q", r, ReasonBuiltLocally)
		}
		if f.Count("DistributionInspect") != 0 {
			t.Error("asked the registry about an image that was built on the host")
		}
	})

	t.Run("pulled from this repository", func(t *testing.T) {
		f := dockertest.New()
		f.AddImage(ref, withDigest(&image.Identity{Pull: []image.PullIdentity{{Repository: "ghcr.io/acme/api"}}}))
		f.SetRemoteDigest(ref, digestB)
		if r := check(NewChecker(nil, newClock().now), f, ref); r.Status != Available {
			t.Errorf("result = %+v, want update_available", r)
		}
	})

	t.Run("docker hub's long and short names are the same repository", func(t *testing.T) {
		f := dockertest.New()
		f.AddImage("nginx:1.27", image.InspectResponse{ID: "sha256:img1", RepoDigests: []string{"nginx@" + digestA},
			Identity: &image.Identity{Pull: []image.PullIdentity{{Repository: "docker.io/library/nginx"}}}})
		f.SetRemoteDigest("nginx:1.27", digestA)
		if r := check(NewChecker(nil, newClock().now), f, "nginx:1.27"); r.Status != UpToDate {
			t.Errorf("result = %+v, want up_to_date", r)
		}
	})

	t.Run("pulled under another name and retagged", func(t *testing.T) {
		f := dockertest.New()
		f.AddImage(ref, withDigest(&image.Identity{Pull: []image.PullIdentity{{Repository: "docker.io/library/alpine"}}}))
		r := check(NewChecker(nil, newClock().now), f, ref)
		if r.Status != Unknown || r.Reason != ReasonBuiltLocally || f.Count("DistributionInspect") != 0 {
			t.Errorf("result = %+v (registry calls %d), want unknown / tagged locally, no registry call", r, f.Count("DistributionInspect"))
		}
	})

	// No word on the origin (the classic store, or an image committed from a
	// container): ask the registry, and if it refuses, say both things that
	// can mean.
	t.Run("origin unknown and the registry refuses", func(t *testing.T) {
		f := dockertest.New()
		f.AddImage(ref, withDigest(nil))
		f.SetRemoteError(ref, errors.New("pull access denied for acme/api, repository does not exist or may require 'docker login': denied: requested access to the resource is denied"))
		r := check(NewChecker(nil, newClock().now), f, ref)
		if r.Status != Unknown || r.Reason != ReasonNeedsLogin {
			t.Errorf("result = %+v, want unknown / %q", r, ReasonNeedsLogin)
		}
		if !strings.Contains(r.Reason, "login") || !strings.Contains(r.Reason, "only on this host") {
			t.Errorf("reason %q should name both possibilities: a login is needed, or the image exists only on this host", r.Reason)
		}
	})
}

func TestCheckCache(t *testing.T) {
	const ref = "ghcr.io/acme/api:develop"
	f := dockertest.New()
	localImage(f, ref, "sha256:img1", "ghcr.io/acme/api", digestA)
	f.SetRemoteDigest(ref, digestA)
	clk := newClock()
	c := NewChecker(nil, clk.now)

	if !c.LastRun("aws").IsZero() {
		t.Error("LastRun is set before any check")
	}
	if _, ok := c.Cached("aws", ref); ok {
		t.Error("Cached reports a result before any check")
	}

	first := check(c, f, ref)
	if first.CheckedAt != clk.t || c.LastRun("aws") != clk.t {
		t.Errorf("CheckedAt = %v, LastRun = %v, want %v", first.CheckedAt, c.LastRun("aws"), clk.t)
	}
	if got, ok := c.Cached("aws", ref); !ok || got != first {
		t.Errorf("Cached = %+v, %v", got, ok)
	}

	clk.advance(14 * time.Minute)
	check(c, f, ref)
	if n := f.Count("DistributionInspect"); n != 1 {
		t.Errorf("registry calls after a second check inside 15m = %d, want 1", n)
	}

	c.Check(ctx, f, "aws", []string{ref}, true)
	if n := f.Count("DistributionInspect"); n != 2 {
		t.Errorf("registry calls after a forced check = %d, want 2", n)
	}

	clk.advance(MaxAge)
	check(c, f, ref)
	if n := f.Count("DistributionInspect"); n != 3 {
		t.Errorf("registry calls after 15m passed = %d, want 3", n)
	}

	// Results are per host.
	if _, ok := c.Cached("gcp", ref); ok {
		t.Error("a result for aws is reported for gcp")
	}
	c.Forget("aws")
	if _, ok := c.Cached("aws", ref); ok || !c.LastRun("aws").IsZero() {
		t.Error("Forget left results behind")
	}
}

func TestInvalidate(t *testing.T) {
	const ref, other = "ghcr.io/acme/api:develop", "ghcr.io/acme/web:develop"
	f := dockertest.New()
	for _, r := range []string{ref, other} {
		localImage(f, r, "sha256:img1", strings.TrimSuffix(r, ":develop"), digestA)
		f.SetRemoteDigest(r, digestA)
	}
	c := NewChecker(nil, newClock().now)
	c.Check(ctx, f, "aws", []string{ref, other}, false)

	c.Invalidate("aws", []string{ref})
	if _, ok := c.Cached("aws", ref); ok {
		t.Error("the invalidated reference is still remembered")
	}
	if _, ok := c.Cached("aws", other); !ok {
		t.Error("another reference was forgotten too")
	}
	// Nothing to do for a host or reference that was never checked.
	c.Invalidate("gcp", []string{ref})
	c.Invalidate("aws", []string{"never:seen"})
}

func TestCheckDeduplicatesReferences(t *testing.T) {
	const ref = "ghcr.io/acme/api:develop"
	f := dockertest.New()
	localImage(f, ref, "sha256:img1", "ghcr.io/acme/api", digestA)
	f.SetRemoteDigest(ref, digestA)
	got := NewChecker(nil, newClock().now).Check(ctx, f, "aws", []string{ref, ref, ref}, false)
	if len(got) != 1 || f.Count("DistributionInspect") != 1 {
		t.Errorf("results = %d, registry calls = %d; want 1 and 1", len(got), f.Count("DistributionInspect"))
	}
}

func TestCheckUsesLogin(t *testing.T) {
	const ref = "ghcr.io/acme/api:develop"
	f := dockertest.New()
	localImage(f, ref, "sha256:img1", "ghcr.io/acme/api", digestA)
	f.SetRemoteDigest(ref, digestA)
	auth := registryauth.Static{List: func() []registryauth.Credential {
		return []registryauth.Credential{{Address: "ghcr.io", Username: "bot", Password: "pw"}}
	}}
	check(NewChecker(auth, newClock().now), f, ref)
	if f.LastAuth(ref) == "" {
		t.Error("the registry lookup carried no login although one is saved for ghcr.io")
	}

	f2 := dockertest.New()
	localImage(f2, "nginx:1.27", "sha256:img1", "nginx", digestA)
	f2.SetRemoteDigest("nginx:1.27", digestA)
	check(NewChecker(auth, newClock().now), f2, "nginx:1.27")
	if f2.LastAuth("nginx:1.27") != "" {
		t.Error("a login for ghcr.io was sent to Docker Hub")
	}
}

func TestStartIfDue(t *testing.T) {
	const ref = "ghcr.io/acme/api:develop"
	f := dockertest.New()
	localImage(f, ref, "sha256:img1", "ghcr.io/acme/api", digestA)
	f.SetRemoteDigest(ref, digestA)
	clk := newClock()
	c := NewChecker(nil, clk.now)

	if !c.StartIfDue("aws") {
		t.Fatal("StartIfDue = false for a host never checked")
	}
	if c.StartIfDue("aws") {
		t.Error("StartIfDue = true while a check is already running")
	}
	if !c.StartIfDue("gcp") {
		t.Error("a running check on aws blocked gcp")
	}
	check(c, f, ref) // completes the run started above
	if c.StartIfDue("aws") {
		t.Error("StartIfDue = true right after a check finished")
	}
	clk.advance(MaxAge + time.Second)
	if !c.StartIfDue("aws") {
		t.Error("StartIfDue = false after the results aged out")
	}
}

func TestForService(t *testing.T) {
	up := Result{Status: UpToDate, LocalImageID: "sha256:new"}
	avail := Result{Status: Available, LocalImageID: "sha256:new"}
	unknown := Result{Status: Unknown, Reason: "needs a registry login"}
	pinned := Result{Status: Pinned}

	cases := []struct {
		name       string
		results    map[string]Result
		containers []ContainerImage
		want       Status
		wantReason string
	}{
		{"all current", map[string]Result{"app:1": up},
			[]ContainerImage{{"app:1", "sha256:new"}, {"app:1", "sha256:new"}}, UpToDate, ""},
		{"registry has a newer image", map[string]Result{"app:1": avail},
			[]ContainerImage{{"app:1", "sha256:new"}}, Available, ""},
		{"tag is current but a container still runs the old image", map[string]Result{"app:1": up},
			[]ContainerImage{{"app:1", "sha256:new"}, {"app:1", "sha256:old"}}, Available, ""},
		{"unknown carries its reason", map[string]Result{"app:1": unknown},
			[]ContainerImage{{"app:1", "sha256:x"}}, Unknown, "needs a registry login"},
		{"available wins over unknown", map[string]Result{"a:1": unknown, "b:1": avail},
			[]ContainerImage{{"a:1", "sha256:x"}, {"b:1", "sha256:new"}}, Available, ""},
		{"all pinned", map[string]Result{"app@sha256:" + hex64: pinned},
			[]ContainerImage{{"app@sha256:" + hex64, "sha256:x"}}, Pinned, ""},
		{"pinned beside current is current", map[string]Result{"app@sha256:" + hex64: pinned, "b:1": up},
			[]ContainerImage{{"app@sha256:" + hex64, "sha256:x"}, {"b:1", "sha256:new"}}, UpToDate, ""},
	}
	for _, c := range cases {
		got, reason := ForService(c.results, c.containers)
		if got != c.want || reason != c.wantReason {
			t.Errorf("%s: ForService = %q, %q; want %q, %q", c.name, got, reason, c.want, c.wantReason)
		}
	}
}
