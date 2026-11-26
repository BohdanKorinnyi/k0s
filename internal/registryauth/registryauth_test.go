package registryauth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/pkg/authconfig"
)

const hex64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"https://index.docker.io/v1/": "docker.io",
		"index.docker.io":             "docker.io",
		"registry-1.docker.io":        "docker.io",
		"docker.io":                   "docker.io",
		"Docker.IO":                   "docker.io",
		"GHCR.io/":                    "ghcr.io",
		"https://ghcr.io/v2/":         "ghcr.io",
		"localhost:5000":              "localhost:5000",
		"  quay.io  ":                 "quay.io",
		"123456789.dkr.ecr.eu-west-1.amazonaws.com": "123456789.dkr.ecr.eu-west-1.amazonaws.com",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"nginx":                    "docker.io",
		"nginx:1.27":               "docker.io",
		"grafana/grafana:11.2.0":   "docker.io",
		"ghcr.io/acme/api:develop": "ghcr.io",
		"localhost:5000/app":       "localhost:5000",
		"123456789.dkr.ecr.eu-west-1.amazonaws.com/app:1":           "123456789.dkr.ecr.eu-west-1.amazonaws.com",
		"europe-west1-docker.pkg.dev/proj/repo/app@sha256:" + hex64: "europe-west1-docker.pkg.dev",
	}
	for ref, want := range cases {
		got, err := RegistryHost(ref)
		if err != nil || got != want {
			t.Errorf("RegistryHost(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	// "Nginx" is invalid: repository names are lower-case. (An upper-case first
	// component followed by a slash is read as a registry host, so it is valid.)
	for _, bad := range []string{"", "Nginx:1", "sha256:" + hex64, hex64} {
		if _, err := RegistryHost(bad); err == nil {
			t.Errorf("RegistryHost(%q) succeeded, want an error", bad)
		}
	}
}

func TestPullable(t *testing.T) {
	yes := []string{"app", "app:develop", "localhost:5000/app", "ghcr.io/acme/api:1.2.3", "deadbeefcafe"}
	for _, ref := range yes {
		if !Pullable(ref) {
			t.Errorf("Pullable(%q) = false, want true", ref)
		}
	}
	no := []string{
		"app@sha256:" + hex64,
		"app:1@sha256:" + hex64,
		"sha256:" + hex64,
		hex64,
		"",
		"not a reference",
	}
	for _, ref := range no {
		if Pullable(ref) {
			t.Errorf("Pullable(%q) = true, want false", ref)
		}
	}
}

func static(creds ...Credential) Static {
	return Static{List: func() []Credential { return creds }}
}

func TestStaticLogin(t *testing.T) {
	p := static(
		Credential{Address: "https://index.docker.io/v1/", Username: "hub", Password: "hubpw"},
		Credential{Address: "GHCR.io", Username: "bot", Password: "ghpw"},
	)
	c, ok, err := p.Login(context.Background(), "ghcr.io")
	if err != nil || !ok || c.Username != "bot" {
		t.Errorf("ghcr.io: %+v, %v, %v", c, ok, err)
	}
	c, ok, _ = p.Login(context.Background(), "docker.io")
	if !ok || c.Username != "hub" {
		t.Errorf("docker.io: %+v, %v", c, ok)
	}
	if _, ok, _ := p.Login(context.Background(), "quay.io"); ok {
		t.Error("quay.io: found a login that was never saved")
	}
}

func TestEncoded(t *testing.T) {
	p := static(Credential{Address: "ghcr.io", Username: "bot", Password: "s3cr3t"})

	enc, err := Encoded(context.Background(), p, "ghcr.io/acme/api:develop")
	if err != nil || enc == "" {
		t.Fatalf("Encoded = %q, %v", enc, err)
	}
	got, err := authconfig.Decode(enc)
	if err != nil {
		t.Fatalf("the result is not what Docker decodes: %v", err)
	}
	if got.Username != "bot" || got.Password != "s3cr3t" || got.ServerAddress != "ghcr.io" {
		t.Errorf("decoded = %+v", got)
	}

	// No saved login for the image's registry: nothing is sent.
	if enc, err := Encoded(context.Background(), p, "nginx:1.27"); err != nil || enc != "" {
		t.Errorf("no match: Encoded = %q, %v; want empty", enc, err)
	}
	// No provider at all.
	if enc, err := Encoded(context.Background(), nil, "ghcr.io/acme/api:develop"); err != nil || enc != "" {
		t.Errorf("nil provider: Encoded = %q, %v; want empty", enc, err)
	}
	// An image ID has no registry: nothing to send, and not an error.
	if enc, err := Encoded(context.Background(), p, "sha256:"+hex64); err != nil || enc != "" {
		t.Errorf("image ID: Encoded = %q, %v; want empty", enc, err)
	}
}

type failing struct{}

func (failing) Login(context.Context, string) (Credential, bool, error) {
	return Credential{}, false, errors.New("token service down")
}

func TestEncodedProviderError(t *testing.T) {
	_, err := Encoded(context.Background(), failing{}, "ghcr.io/acme/api:develop")
	if err == nil || !strings.Contains(err.Error(), "token service down") {
		t.Errorf("err = %v, want the provider's error", err)
	}
}
