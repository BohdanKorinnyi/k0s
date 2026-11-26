package api

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"dockpanel/internal/actions"
	"dockpanel/internal/dockerapi/dockertest"
	"dockpanel/internal/services"
	"dockpanel/internal/store"
)

const (
	pemKey  = "-----BEGIN EC PRIVATE KEY-----\nTOPSECRETKEYMATERIAL\n-----END EC PRIVATE KEY-----"
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// addHost saves a host and gives it a fake daemon.
func (r *rig) addHost(name string, protected bool) (store.Host, *dockertest.Fake) {
	r.t.Helper()
	h, err := r.store.SaveHost(store.Host{Name: name, URL: "ssh://deploy@" + name + ".example.com", Protected: protected})
	if err != nil {
		r.t.Fatal(err)
	}
	return h, r.hosts.daemon(h.ID)
}

// shop puts a two-container Compose service "api" and a standalone redis on
// the daemon.
func shop(f *dockertest.Fake) {
	for i := 1; i <= 2; i++ {
		c := dockertest.Running(fmt.Sprintf("shop-api-%d", i), "ghcr.io/acme/api:develop")
		c.Image = "sha256:img1"
		c.Config.Labels = dockertest.ComposeLabels("shop", "api", i)
		c.Config.Env = []string{"A=1", "URL=postgres://u:p@h/db?x=1", "EMPTY", "SECRET_TOKEN=hunter2"}
		f.AddContainer(c)
	}
	f.AddContainer(dockertest.Stopped("redis-cache", "redis:7.4", 137))
	f.AddImage("ghcr.io/acme/api:develop", image.InspectResponse{ID: "sha256:img1", RepoDigests: []string{"ghcr.io/acme/api@" + digestA}})
	f.SetRemoteDigest("ghcr.io/acme/api:develop", digestB)
}

type hostBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Protected bool   `json:"protected"`
	HasTLS    bool   `json:"hasTLS"`
}

func TestHostsCRUD(t *testing.T) {
	r := newRig(t, nil)

	if rec := r.do("GET", "/api/hosts", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty list: %d %s; want []", rec.Code, rec.Body.String())
	}

	rec := r.do("POST", "/api/hosts", map[string]any{
		"name": "AWS Prod", "url": "tcp://10.0.0.5:2376", "protected": true,
		"tls": map[string]string{"ca": "", "cert": "", "key": pemKey},
	})
	// A key without a certificate is not a usable TLS setup.
	wantError(t, rec, http.StatusBadRequest, "invalid")

	rec = r.do("POST", "/api/hosts", map[string]any{"name": "AWS Prod", "url": "ssh://deploy@aws.example.com", "protected": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	created := decode[hostBody](t, rec)
	if created.ID != "aws-prod" || created.Name != "AWS Prod" || !created.Protected || created.HasTLS {
		t.Errorf("created = %+v", created)
	}

	wantError(t, r.do("POST", "/api/hosts", map[string]any{"name": "x", "url": "http://nope"}), http.StatusBadRequest, "invalid")
	wantError(t, r.do("POST", "/api/hosts", map[string]any{"name": "", "url": "tcp://a:1"}), http.StatusBadRequest, "invalid")
	wantError(t, r.do("POST", "/api/hosts", map[string]any{"name": "aws prod", "url": "tcp://a:1"}), http.StatusConflict, "duplicate")

	rec = r.do("PUT", "/api/hosts/aws-prod", map[string]any{"name": "Production", "url": "ssh://deploy@aws.example.com", "protected": false})
	if rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if u := decode[hostBody](t, rec); u.ID != "aws-prod" || u.Name != "Production" || u.Protected {
		t.Errorf("updated = %+v, want the ID kept and the fields changed", u)
	}
	if got := r.hosts.invalidatedIDs(); !slices.Contains(got, "aws-prod") {
		t.Errorf("invalidated = %v, want the edited host's connection dropped", got)
	}

	wantError(t, r.do("PUT", "/api/hosts/nope", map[string]any{"name": "x", "url": "tcp://a:1"}), http.StatusNotFound, "not_found")

	list := decode[[]hostBody](t, r.do("GET", "/api/hosts", nil))
	if len(list) != 1 || list[0].Name != "Production" {
		t.Errorf("list = %+v", list)
	}

	if rec := r.do("DELETE", "/api/hosts/aws-prod", nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if n := len(r.hosts.invalidatedIDs()); n != 2 {
		t.Errorf("invalidations = %d, want one for the edit and one for the delete", n)
	}
	wantError(t, r.do("DELETE", "/api/hosts/aws-prod", nil), http.StatusNotFound, "not_found")
}

func TestHostSecretsNeverLeave(t *testing.T) {
	r := newRig(t, nil)
	h, err := r.store.SaveHost(store.Host{Name: "tls", URL: "tcp://10.0.0.5:2376", TLS: &store.TLS{CA: "CACERT", Cert: "CLIENTCERT", Key: pemKey}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []*httptest.ResponseRecorder{
		r.do("GET", "/api/hosts", nil),
		// An edit that leaves the key blank keeps it.
		r.do("PUT", "/api/hosts/"+h.ID, map[string]any{"name": "tls", "url": "ssh://x@tls.example.com"}),
	} {
		if strings.Contains(rec.Body.String(), "TOPSECRETKEYMATERIAL") || strings.Contains(rec.Body.String(), "CLIENTCERT") {
			t.Errorf("a response contains TLS material: %s", rec.Body.String())
		}
	}
	list := decode[[]hostBody](t, r.do("GET", "/api/hosts", nil))
	if len(list) != 1 || list[0].HasTLS {
		t.Errorf("list = %+v, want TLS dropped once the host is no longer tcp://", list)
	}
}

// The edit form never receives the saved certificates, so it cannot send them
// back. Each TLS field left blank keeps what is stored; only an explicit
// "no TLS" removes them.
func TestEditingATLSHostKeepsBlankFields(t *testing.T) {
	r := newRig(t, nil)
	cert, key := selfSigned(t)
	h, err := r.store.SaveHost(store.Host{Name: "tls", URL: "tcp://10.0.0.5:2376", TLS: &store.TLS{CA: cert, Cert: cert, Key: key}})
	if err != nil {
		t.Fatal(err)
	}

	rec := r.do("PUT", "/api/hosts/"+h.ID, map[string]any{
		"name": "tls renamed", "url": "tcp://10.0.0.5:2376",
		"tls": map[string]string{"ca": "", "cert": "", "key": ""},
	})
	if rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	stored, _ := r.store.Host(h.ID)
	if stored.Name != "tls renamed" || stored.TLS == nil || stored.TLS.CA != cert || stored.TLS.Cert != cert || stored.TLS.Key != key {
		t.Errorf("stored TLS after an edit with blank fields = %+v, want all three kept", stored.TLS)
	}

	// Replacing only the CA keeps the client certificate and key.
	other, _ := selfSigned(t)
	r.do("PUT", "/api/hosts/"+h.ID, map[string]any{
		"name": "tls renamed", "url": "tcp://10.0.0.5:2376",
		"tls": map[string]string{"ca": other, "cert": "", "key": ""},
	})
	stored, _ = r.store.Host(h.ID)
	// (What the form sends is stored without its surrounding whitespace.)
	if stored.TLS == nil || stored.TLS.CA != strings.TrimSpace(other) || stored.TLS.Cert != cert || stored.TLS.Key != key {
		t.Errorf("after replacing the CA: the CA was not replaced, or the certificate and key were not kept")
	}

	// Turning TLS off removes it.
	rec = r.do("PUT", "/api/hosts/"+h.ID, map[string]any{"name": "tls renamed", "url": "tcp://10.0.0.5:2375", "tls": nil})
	if got := decode[hostBody](t, rec); got.HasTLS {
		t.Errorf("after tls: null the host still has TLS: %+v", got)
	}
}

type testBody struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

func TestHostTest(t *testing.T) {
	r := newRig(t, nil)

	rec := r.do("POST", "/api/hosts/test", map[string]any{"name": "new", "url": "ssh://deploy@new.example.com"})
	if got := decode[testBody](t, rec); rec.Code != 200 || !got.OK || got.Version != "27.0" {
		t.Errorf("reachable: %d %+v", rec.Code, got)
	}

	r.hosts.testErr = errors.New("ssh: deploy@new: Permission denied (publickey).")
	rec = r.do("POST", "/api/hosts/test", map[string]any{"name": "new", "url": "ssh://deploy@new.example.com"})
	if got := decode[testBody](t, rec); rec.Code != 200 || got.OK || !strings.Contains(got.Error, "Permission denied") {
		t.Errorf("failing: %d %+v; want 200 with ok=false and the reason", rec.Code, got)
	}

	wantError(t, r.do("POST", "/api/hosts/test", map[string]any{"url": "ftp://x"}), http.StatusBadRequest, "invalid")
}

func TestHostTestUsesStoredSecrets(t *testing.T) {
	r := newRig(t, nil)
	cert, key := selfSigned(t)
	h, err := r.store.SaveHost(store.Host{Name: "tls", URL: "tcp://10.0.0.5:2376", TLS: &store.TLS{CA: cert, Cert: cert, Key: key}})
	if err != nil {
		t.Fatal(err)
	}

	// The edit form cannot send the key back: it never had it.
	rec := r.do("POST", "/api/hosts/test", map[string]any{
		"id": h.ID, "name": "tls", "url": "tcp://10.0.0.6:2376",
		"tls": map[string]string{"ca": cert, "cert": cert, "key": ""},
	})
	if got := decode[testBody](t, rec); !got.OK {
		t.Fatalf("test result = %+v (status %d)", got, rec.Code)
	}
	seen := r.hosts.testSeen
	if seen.URL != "tcp://10.0.0.6:2376" || seen.TLS == nil || seen.TLS.Key != key {
		t.Errorf("host tested = %+v, want the new URL with the stored key", seen)
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Error("the response contains the key")
	}

	// Without the ID there is no stored key to fall back on, and a
	// certificate without its key is refused.
	wantError(t, r.do("POST", "/api/hosts/test", map[string]any{
		"name": "tls", "url": "tcp://10.0.0.6:2376",
		"tls": map[string]string{"ca": cert, "cert": cert, "key": ""},
	}), http.StatusBadRequest, "invalid")
}

// selfSigned returns a certificate and its key as PEM text.
func selfSigned(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dockpanel test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

type updateBody struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type containerBody struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Image     string  `json:"image"`
	ImageID   string  `json:"imageId"`
	State     string  `json:"state"`
	Health    string  `json:"health"`
	ExitCode  int     `json:"exitCode"`
	StartedAt *string `json:"startedAt"`
	Restarts  int     `json:"restarts"`
}

type serviceBody struct {
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Project    string          `json:"project"`
	Image      string          `json:"image"`
	Images     []string        `json:"images"`
	Running    int             `json:"running"`
	Total      int             `json:"total"`
	State      string          `json:"state"`
	ExitCode   int             `json:"exitCode"`
	StartedAt  *string         `json:"startedAt"`
	Restarts   int             `json:"restarts"`
	Update     updateBody      `json:"update"`
	Actions    []string        `json:"actions"`
	Containers []containerBody `json:"containers"`
}

type servicesBody struct {
	Groups []struct {
		Project  string        `json:"project"`
		Services []serviceBody `json:"services"`
	} `json:"groups"`
	UpdatesCheckedAt *string `json:"updatesCheckedAt"`
}

func (b servicesBody) service(key string) (serviceBody, bool) {
	for _, g := range b.Groups {
		for _, s := range g.Services {
			if s.Key == key {
				return s, true
			}
		}
	}
	return serviceBody{}, false
}

func TestServices(t *testing.T) {
	r := newRig(t, nil)
	r.srv.spawn = func(func()) {} // no background check in this test
	h, f := r.addHost("aws", false)
	shop(f)

	rec := r.do("GET", "/api/hosts/"+h.ID+"/services", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode[servicesBody](t, rec)
	if len(body.Groups) != 2 || body.Groups[0].Project != "shop" || body.Groups[1].Project != "" {
		t.Fatalf("groups = %+v", body.Groups)
	}
	api, _ := body.service("c~shop~api")
	if api.Name != "api" || api.Running != 2 || api.Total != 2 || api.State != "running" || api.Image != "ghcr.io/acme/api:develop" {
		t.Errorf("api = %+v", api)
	}
	if want := []string{"restart", "stop", "pull-restart"}; !reflect.DeepEqual(api.Actions, want) {
		t.Errorf("api actions = %v, want %v", api.Actions, want)
	}
	if api.Update.Status != "unchecked" {
		t.Errorf("update before any check = %+v, want unchecked", api.Update)
	}
	if len(api.Containers) != 2 || api.Containers[0].Name != "shop-api-1" || api.Containers[0].StartedAt == nil {
		t.Errorf("containers = %+v", api.Containers)
	}
	if body.UpdatesCheckedAt != nil {
		t.Errorf("updatesCheckedAt = %v before any check, want null", *body.UpdatesCheckedAt)
	}

	redis, _ := body.service("s~redis-cache")
	if redis.State != "stopped" || redis.ExitCode != 137 || redis.StartedAt != nil {
		t.Errorf("redis = %+v, want stopped with exit 137 and no start time", redis)
	}
	if want := []string{"restart", "start", "pull-restart"}; !reflect.DeepEqual(redis.Actions, want) {
		t.Errorf("redis actions = %v, want %v", redis.Actions, want)
	}

	one := decode[serviceBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services/c~shop~api", nil))
	if one.Key != "c~shop~api" || len(one.Containers) != 2 {
		t.Errorf("single service = %+v", one)
	}
	wantError(t, r.do("GET", "/api/hosts/"+h.ID+"/services/c~shop~nope", nil), http.StatusNotFound, "not_found")
	wantError(t, r.do("GET", "/api/hosts/nope/services", nil), http.StatusNotFound, "not_found")
}

func TestServicesHostUnreachable(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	f.Fail("ContainerList", errors.New("ssh: connect to host aws.example.com port 22: Connection timed out"))

	e := wantError(t, r.do("GET", "/api/hosts/"+h.ID+"/services", nil), http.StatusBadGateway, "host_unreachable")
	if !strings.Contains(e.Error, "Connection timed out") {
		t.Errorf("error = %q, want the reason passed on", e.Error)
	}
}

func TestBackgroundUpdateCheck(t *testing.T) {
	r := newRig(t, nil)
	r.srv.spawn = func(f func()) { f() } // run the check inline, after the response is built
	h, f := r.addHost("aws", false)
	shop(f)

	first := decode[servicesBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services?check=1", nil))
	if api, _ := first.service("c~shop~api"); api.Update.Status != "unchecked" {
		t.Errorf("first response update = %+v, want unchecked (the check runs after it)", api.Update)
	}

	second := decode[servicesBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services", nil))
	api, _ := second.service("c~shop~api")
	if api.Update.Status != "update_available" {
		t.Errorf("second response update = %+v, want update_available", api.Update)
	}
	if second.UpdatesCheckedAt == nil {
		t.Error("updatesCheckedAt is null after a check")
	}
	redis, _ := second.service("s~redis-cache")
	if redis.Update.Status != "unknown" || redis.Update.Reason == "" {
		t.Errorf("redis update = %+v, want unknown with a reason (its image is not on the fake host)", redis.Update)
	}

	// Within 15 minutes the registry is not asked again.
	calls := f.Count("DistributionInspect")
	r.do("GET", "/api/hosts/"+h.ID+"/services?check=1", nil)
	if got := f.Count("DistributionInspect"); got != calls {
		t.Errorf("registry calls went from %d to %d on a refresh inside 15 minutes", calls, got)
	}
}

func TestCheckNow(t *testing.T) {
	r := newRig(t, nil)
	r.srv.spawn = func(f func()) { f() }
	h, f := r.addHost("aws", false)
	shop(f)
	r.do("GET", "/api/hosts/"+h.ID+"/services?check=1", nil)
	before := f.Count("DistributionInspect")

	rec := r.do("POST", "/api/hosts/"+h.ID+"/updates/check", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.Count("DistributionInspect"); got != before+1 {
		t.Errorf("registry calls = %d, want %d: check-now must ask again", got, before+1)
	}
	if api, _ := decode[servicesBody](t, rec).service("c~shop~api"); api.Update.Status != "update_available" {
		t.Errorf("response update = %+v", api.Update)
	}
}

// events parses a stream of newline-delimited JSON events.
func events(t *testing.T, rec *httptest.ResponseRecorder) []actions.Event {
	t.Helper()
	var out []actions.Event
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		var e actions.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %q is not an event: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func actionPath(hostID, key, action string) string {
	return "/api/hosts/" + hostID + "/services/" + key + "/actions/" + action
}

func TestActionStream(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)

	rec := r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("status %d, content type %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	got := events(t, rec)
	if len(got) != 3 {
		t.Fatalf("events = %+v, want two steps and a closing event", got)
	}
	if got[0].Step != "Restarting shop-api-1" || got[0].Done {
		t.Errorf("first event = %+v", got[0])
	}
	if last := got[2]; !last.Done || last.Level != "success" {
		t.Errorf("last event = %+v, want done and success", last)
	}
	if want := []string{"ContainerRestart shop-api-1", "ContainerRestart shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q", f.CallLog())
	}
}

func TestActionOnOneContainer(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	second, _ := f.Container("shop-api-2")

	rec := r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), map[string]string{"container": second.ID})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if want := []string{"ContainerRestart shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want only the named container", f.CallLog())
	}

	// Pulling is for the whole service.
	wantError(t, r.do("POST", actionPath(h.ID, "c~shop~api", "pull-restart"), map[string]string{"container": second.ID}),
		http.StatusBadRequest, "invalid")
	wantError(t, r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), map[string]string{"container": "not-here"}),
		http.StatusBadRequest, "invalid")
}

func TestActionErrors(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)

	wantError(t, r.do("POST", actionPath(h.ID, "c~shop~api", "explode"), nil), http.StatusNotFound, "not_found")
	wantError(t, r.do("POST", actionPath(h.ID, "c~shop~nope", "restart"), nil), http.StatusNotFound, "not_found")
	wantError(t, r.do("POST", actionPath("nope", "c~shop~api", "restart"), nil), http.StatusNotFound, "not_found")
	// Every container is already up: there is nothing to start.
	wantError(t, r.do("POST", actionPath(h.ID, "c~shop~api", "start"), nil), http.StatusConflict, "not_applicable")
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}

	f.FailFor("ContainerRestart", "shop-api-1", errors.New("OCI runtime error: no space left on device"))
	rec := r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d; a failure after the stream began is reported in the stream", rec.Code)
	}
	got := events(t, rec)
	last := got[len(got)-1]
	if !last.Done || last.Level != "error" || !strings.Contains(last.Step, "no space left on device") || !strings.Contains(last.Step, "shop-api-1") {
		t.Errorf("last event = %+v, want the failure with its reason", last)
	}
}

func TestProtectedHost(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("prod", true)
	shop(f)
	path := actionPath(h.ID, "c~shop~api", "restart")

	wantError(t, r.do("POST", path, nil), http.StatusForbidden, "confirmation_required")
	wantError(t, r.do("POST", path, map[string]string{"confirm": "wrong"}), http.StatusForbidden, "confirmation_required")
	wantError(t, r.do("POST", path, map[string]string{"confirm": "API"}), http.StatusForbidden, "confirmation_required")
	if calls := f.CallLog(); len(calls) != 0 {
		t.Fatalf("calls = %q, want nothing done without confirmation", calls)
	}

	rec := r.do("POST", path, map[string]string{"confirm": "api"})
	if rec.Code != 200 || len(f.CallLog()) != 2 {
		t.Errorf("confirmed: status %d, calls %q", rec.Code, f.CallLog())
	}
}

// blockingAction holds an action open until released, so a test can act while
// one is in flight.
type blockingAction struct{ entered, release chan struct{} }

func (blockingAction) Info() actions.Info            { return actions.Info{ID: "block", Label: "Block"} }
func (blockingAction) Applies(services.Service) bool { return true }
func (b blockingAction) Run(context.Context, actions.Env, actions.Target, actions.Progress) error {
	close(b.entered)
	<-b.release
	return nil
}

func TestActionBusy(t *testing.T) {
	block := blockingAction{entered: make(chan struct{}), release: make(chan struct{})}
	r := newRig(t, func(d *Deps) {
		d.Actions = actions.Default()
		d.Actions.Register(block)
	})
	h, f := r.addHost("aws", false)
	shop(f)
	f.AddContainer(dockertest.Running("worker-1", "app:1"))

	firstDone := make(chan *httptest.ResponseRecorder)
	go func() { firstDone <- r.do("POST", actionPath(h.ID, "c~shop~api", "block"), nil) }()
	<-block.entered

	wantError(t, r.do("POST", actionPath(h.ID, "c~shop~api", "stop"), nil), http.StatusConflict, "busy")
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want the refused action to have done nothing", calls)
	}
	if rec := r.do("POST", actionPath(h.ID, "s~worker-1", "restart"), nil); rec.Code != 200 {
		t.Errorf("another service while one is busy: status %d, want it to proceed", rec.Code)
	}

	close(block.release)
	if rec := <-firstDone; rec.Code != 200 {
		t.Errorf("first action: status %d", rec.Code)
	}
	// Once it has finished the service is free again.
	if rec := r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), nil); rec.Code != 200 {
		t.Errorf("after the first finished: status %d, want 200", rec.Code)
	}
}

func TestActionSurvivesDisconnect(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)

	ctx, cancel := context.WithCancel(context.Background())
	f.After = func(method string, c *container.InspectResponse) {
		if method == "ContainerRestart" && c.Name == "/shop-api-1" {
			cancel() // the browser tab closes after the first container
		}
	}
	req := httptest.NewRequest("POST", actionPath(h.ID, "c~shop~api", "restart"), nil).WithContext(ctx)
	req.Host = testHost
	r.h.ServeHTTP(httptest.NewRecorder(), req)

	deadline := time.Now().Add(3 * time.Second)
	for len(f.CallLog()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if want := []string{"ContainerRestart shop-api-1", "ContainerRestart shop-api-2"}; !reflect.DeepEqual(f.CallLog(), want) {
		t.Errorf("calls = %q, want the action finished although the request was cancelled", f.CallLog())
	}
	// And the service is released afterwards.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rec := r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), nil); rec.Code == 200 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the service stayed busy after the detached action finished")
}

// After a pull-and-restart the containers run a new image. What was remembered
// about "is there an update" described the old one, and must not linger: the
// service would show an update badge the moment it had been updated.
func TestUpdateStatusIsRefreshedAfterAnAction(t *testing.T) {
	r := newRig(t, nil)
	r.srv.spawn = func(f func()) { f() }
	r.srv.tuneEnv = func(env *actions.Env) { env.StartGrace, env.Poll = 5*time.Millisecond, time.Millisecond }
	h, f := r.addHost("aws", false)

	const ref = "ghcr.io/acme/api:develop"
	oldImage := image.InspectResponse{ID: "sha256:old", RepoDigests: []string{"ghcr.io/acme/api@" + digestA}}
	newImage := image.InspectResponse{ID: "sha256:new", RepoDigests: []string{"ghcr.io/acme/api@" + digestB}}
	c := dockertest.Running("api-1", ref)
	c.Image = "sha256:old"
	f.AddContainer(c)
	f.AddImage(ref, oldImage)
	f.SetRemoteDigest(ref, digestB) // the registry already has the new build
	f.SetPullResult(ref, newImage)

	status := func() string {
		t.Helper()
		body := decode[servicesBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services", nil))
		svc, ok := body.service("s~api-1")
		if !ok {
			t.Fatalf("service missing from %+v", body)
		}
		return svc.Update.Status
	}

	r.do("GET", "/api/hosts/"+h.ID+"/services?check=1", nil) // opening the list starts the check
	if got := status(); got != "update_available" {
		t.Fatalf("before the action: update = %q, want update_available", got)
	}

	rec := r.do("POST", actionPath(h.ID, "s~api-1", "pull-restart"), nil)
	evs := events(t, rec)
	if last := evs[len(evs)-1]; !last.Done || last.Level != "success" {
		t.Fatalf("pull-restart did not succeed: %+v", evs)
	}

	// Straight away the stale answer must be gone; the fresh one follows as
	// soon as the registry has been asked again.
	if got := status(); got == "update_available" {
		t.Errorf("right after the action the service still shows the old update status")
	}
	deadline := time.Now().Add(3 * time.Second)
	for status() != "up_to_date" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := status(); got != "up_to_date" {
		t.Errorf("after the action: update = %q, want up_to_date", got)
	}
}

func TestActionsList(t *testing.T) {
	r := newRig(t, nil)
	got := decode[[]actions.Info](t, r.do("GET", "/api/actions", nil))
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if want := []string{"restart", "start", "stop", "pull-restart"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("actions = %v, want %v", ids, want)
	}
	if !got[2].Destructive || !got[3].Destructive || got[0].Destructive || got[3].ContainerLevel {
		t.Errorf("flags = %+v", got)
	}
}

func TestEnv(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	base := "/api/hosts/" + h.ID + "/containers/shop-api-1/env"

	rec := r.do("GET", base, nil)
	names := decode[struct {
		Names []string `json:"names"`
	}](t, rec)
	if want := []string{"A", "URL", "EMPTY", "SECRET_TOKEN"}; !reflect.DeepEqual(names.Names, want) {
		t.Errorf("names = %v, want %v", names.Names, want)
	}
	for _, secret := range []string{"hunter2", "postgres://"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the names response contains a value (%q): %s", secret, rec.Body.String())
		}
	}

	type values struct {
		Values map[string]string `json:"values"`
	}
	one := decode[values](t, r.do("POST", base+"/reveal", map[string]any{"names": []string{"A"}}))
	if !reflect.DeepEqual(one.Values, map[string]string{"A": "1"}) {
		t.Errorf("reveal A = %v", one.Values)
	}
	all := decode[values](t, r.do("POST", base+"/reveal", map[string]any{}))
	want := map[string]string{"A": "1", "URL": "postgres://u:p@h/db?x=1", "EMPTY": "", "SECRET_TOKEN": "hunter2"}
	if !reflect.DeepEqual(all.Values, want) {
		t.Errorf("reveal all = %v\nwant       %v", all.Values, want)
	}
	// An empty body means "all" too, and unknown names are simply absent.
	if got := decode[values](t, r.do("POST", base+"/reveal", nil)); len(got.Values) != 4 {
		t.Errorf("reveal with no body = %v, want all four", got.Values)
	}
	if got := decode[values](t, r.do("POST", base+"/reveal", map[string]any{"names": []string{"NOPE"}})); len(got.Values) != 0 {
		t.Errorf("reveal of an unknown name = %v, want nothing", got.Values)
	}

	// Values must not be readable with a GET, which other sites can trigger.
	if rec := r.do("GET", base+"/reveal", nil); rec.Code == 200 {
		t.Error("GET on the reveal route returned values")
	}
	wantError(t, r.do("GET", "/api/hosts/"+h.ID+"/containers/nope/env", nil), http.StatusNotFound, "not_found")
}

func TestInfo(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	c := dockertest.Running("shop-api-1", "ghcr.io/acme/api:develop")
	c.Image = "sha256:img1"
	c.Created = "2026-11-01T08:00:00Z"
	c.Path, c.Args = "/entry.sh", []string{"serve", "--port", "3000"}
	c.RestartCount = 4
	c.Config.Labels = dockertest.ComposeLabels("shop", "api", 1)
	c.Config.Labels["com.docker.compose.project.working_dir"] = "/srv/shop"
	c.HostConfig.RestartPolicy = container.RestartPolicy{Name: "unless-stopped"}
	c.HostConfig.PortBindings = network.PortMap{network.MustParsePort("3000/tcp"): {{HostPort: "8080"}}}
	c.NetworkSettings.Ports = network.PortMap{network.MustParsePort("3000/tcp"): {{HostPort: "8080"}}}
	c.Mounts = []container.MountPoint{{Type: mount.TypeBind, Source: "/srv/shop/uploads", Destination: "/app/uploads", RW: true}}
	c.NetworkSettings.Networks = map[string]*network.EndpointSettings{"shop_default": {Aliases: []string{"api"}}}
	f.AddContainer(c)
	f.AddImage("sha256:img1", image.InspectResponse{ID: "sha256:img1", RepoDigests: []string{"ghcr.io/acme/api@" + digestA}})

	rec := r.do("GET", "/api/hosts/"+h.ID+"/containers/shop-api-1/info", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var info struct {
		Name          string   `json:"name"`
		Image         string   `json:"image"`
		ImageID       string   `json:"imageId"`
		Digests       []string `json:"digests"`
		Created       string   `json:"created"`
		State         string   `json:"state"`
		RestartPolicy string   `json:"restartPolicy"`
		Restarts      int      `json:"restarts"`
		Command       []string `json:"command"`
		Ports         []struct {
			Container string `json:"container"`
			Host      string `json:"host"`
		} `json:"ports"`
		Mounts []struct {
			Type        string `json:"type"`
			Source      string `json:"source"`
			Destination string `json:"destination"`
			ReadOnly    bool   `json:"readOnly"`
		} `json:"mounts"`
		Networks []struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
		} `json:"networks"`
		Labels  map[string]string `json:"labels"`
		Compose struct {
			Project    string `json:"project"`
			Service    string `json:"service"`
			WorkingDir string `json:"workingDir"`
		} `json:"compose"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "shop-api-1" || info.Image != "ghcr.io/acme/api:develop" || info.ImageID != "sha256:img1" || info.State != "running" {
		t.Errorf("identity = %+v", info)
	}
	if !reflect.DeepEqual(info.Digests, []string{"ghcr.io/acme/api@" + digestA}) {
		t.Errorf("digests = %v", info.Digests)
	}
	if info.RestartPolicy != "unless-stopped" || info.Restarts != 4 || info.Created != "2026-11-01T08:00:00Z" {
		t.Errorf("policy/restarts/created = %q/%d/%q", info.RestartPolicy, info.Restarts, info.Created)
	}
	if !reflect.DeepEqual(info.Command, []string{"/entry.sh", "serve", "--port", "3000"}) {
		t.Errorf("command = %v", info.Command)
	}
	if len(info.Ports) != 1 || info.Ports[0].Container != "3000/tcp" || info.Ports[0].Host != "8080" {
		t.Errorf("ports = %+v", info.Ports)
	}
	if len(info.Mounts) != 1 || info.Mounts[0].Source != "/srv/shop/uploads" || info.Mounts[0].Destination != "/app/uploads" || info.Mounts[0].ReadOnly {
		t.Errorf("mounts = %+v", info.Mounts)
	}
	if len(info.Networks) != 1 || info.Networks[0].Name != "shop_default" {
		t.Errorf("networks = %+v", info.Networks)
	}
	if info.Compose.Project != "shop" || info.Compose.Service != "api" || info.Compose.WorkingDir != "/srv/shop" {
		t.Errorf("compose = %+v", info.Compose)
	}
	if info.Labels["com.docker.compose.project"] != "shop" {
		t.Errorf("labels = %v", info.Labels)
	}
	// The info view must not carry the environment: that is what Env and its
	// reveal step are for.
	if strings.Contains(rec.Body.String(), "\"env\"") || strings.Contains(rec.Body.String(), "\"Env\"") {
		t.Error("the info response contains the environment")
	}
}

type registryBody struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
}

func TestRegistriesCRUD(t *testing.T) {
	r := newRig(t, nil)

	rec := r.do("POST", "/api/registries", map[string]string{"address": "ghcr.io", "username": "bot", "password": "ghp_SUPERSECRET"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	created := decode[registryBody](t, rec)
	if created.ID == "" || created.Address != "ghcr.io" || created.Username != "bot" || !created.HasPassword {
		t.Errorf("created = %+v", created)
	}

	wantError(t, r.do("POST", "/api/registries", map[string]string{"address": "GHCR.io", "username": "x"}), http.StatusConflict, "duplicate")
	wantError(t, r.do("POST", "/api/registries", map[string]string{"address": "", "username": "x"}), http.StatusBadRequest, "invalid")

	// An edit that leaves the password blank keeps it.
	rec = r.do("PUT", "/api/registries/"+created.ID, map[string]string{"address": "ghcr.io", "username": "bot2", "password": ""})
	if u := decode[registryBody](t, rec); rec.Code != 200 || u.Username != "bot2" || !u.HasPassword {
		t.Errorf("update: %d %+v", rec.Code, u)
	}
	if stored := r.store.Registries(); len(stored) != 1 || stored[0].Password != "ghp_SUPERSECRET" {
		t.Errorf("stored = %+v, want the password kept", stored)
	}

	list := r.do("GET", "/api/registries", nil)
	if got := decode[[]registryBody](t, list); len(got) != 1 || got[0].Username != "bot2" {
		t.Errorf("list = %+v", got)
	}
	for _, body := range []string{rec.Body.String(), list.Body.String()} {
		if strings.Contains(body, "ghp_SUPERSECRET") {
			t.Errorf("a response contains the password: %s", body)
		}
	}

	wantError(t, r.do("PUT", "/api/registries/nope", map[string]string{"address": "quay.io"}), http.StatusNotFound, "not_found")
	if rec := r.do("DELETE", "/api/registries/"+created.ID, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	wantError(t, r.do("DELETE", "/api/registries/"+created.ID, nil), http.StatusNotFound, "not_found")
}

func TestEverythingNeedsLoginWhenAPasswordIsSet(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })
	h, f := r.addHost("aws", false)
	shop(f)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/hosts"},
		{"POST", "/api/hosts"},
		{"GET", "/api/hosts/" + h.ID + "/services"},
		{"POST", actionPath(h.ID, "c~shop~api", "restart")},
		{"GET", "/api/hosts/" + h.ID + "/containers/shop-api-1/env"},
		{"POST", "/api/hosts/" + h.ID + "/containers/shop-api-1/env/reveal"},
		{"GET", "/api/hosts/" + h.ID + "/containers/shop-api-1/info"},
		{"GET", "/api/registries"},
		{"GET", "/api/actions"},
	} {
		wantError(t, r.do(c.method, c.path, nil), http.StatusUnauthorized, "unauthenticated")
	}
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want nothing done without a login", calls)
	}
}

// Registries count lookups against a quota (Docker Hub allows 100 per six
// hours without a login). A list left open refreshes itself every few seconds;
// only opening it may start a check, or the quota drains and real pulls fail.
func TestOnlyOpeningTheListStartsAnUpdateCheck(t *testing.T) {
	r := newRig(t, nil)
	r.srv.spawn = func(f func()) { f() }
	h, f := r.addHost("aws", false)
	shop(f)

	for range 3 {
		r.do("GET", "/api/hosts/"+h.ID+"/services", nil) // the periodic refresh
	}
	if n := f.Count("DistributionInspect"); n != 0 {
		t.Fatalf("refreshes made %d registry lookups, want none", n)
	}
	r.do("GET", "/api/hosts/"+h.ID+"/services?check=1", nil) // the list being opened
	if n := f.Count("DistributionInspect"); n == 0 {
		t.Error("opening the list did not start a check")
	}
}

// Restart, stop and start cannot change which image a service runs: they must
// not cost a registry lookup, nor keep the service busy while one runs.
func TestPlainActionsDoNotAskTheRegistry(t *testing.T) {
	r := newRig(t, nil)
	r.srv.spawn = func(f func()) { f() }
	h, f := r.addHost("aws", false)
	shop(f)
	r.do("GET", "/api/hosts/"+h.ID+"/services?check=1", nil)
	before := f.Count("DistributionInspect")

	if rec := r.do("POST", actionPath(h.ID, "c~shop~api", "restart"), nil); rec.Code != 200 {
		t.Fatalf("restart: %d", rec.Code)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.Count("DistributionInspect"); got != before {
		t.Errorf("a restart caused %d registry lookups", got-before)
	}
	// And what was known about updates is still known.
	body := decode[servicesBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services", nil))
	if api, _ := body.service("c~shop~api"); api.Update.Status != "update_available" {
		t.Errorf("update status after a restart = %q, want it unchanged", api.Update.Status)
	}
}

// After a pull the registry is asked again, but as background work: the
// action is over, its progress stream closed and the service free, before the
// answer comes back.
func TestTheUpdateCheckAfterAPullDoesNotHoldTheAction(t *testing.T) {
	r := newRig(t, nil)
	var mu sync.Mutex
	var deferred []func()
	r.srv.spawn = func(f func()) {
		mu.Lock()
		defer mu.Unlock()
		deferred = append(deferred, f)
	}
	r.srv.tuneEnv = func(env *actions.Env) { env.StartGrace, env.Poll = 5*time.Millisecond, time.Millisecond }
	h, f := r.addHost("aws", false)

	const ref = "ghcr.io/acme/api:develop"
	c := dockertest.Running("api-1", ref)
	c.Image = "sha256:old"
	f.AddContainer(c)
	f.AddImage(ref, image.InspectResponse{ID: "sha256:old", RepoDigests: []string{"ghcr.io/acme/api@" + digestA}})
	f.SetRemoteDigest(ref, digestB)
	f.SetPullResult(ref, image.InspectResponse{ID: "sha256:new", RepoDigests: []string{"ghcr.io/acme/api@" + digestB}})

	rec := r.do("POST", actionPath(h.ID, "s~api-1", "pull-restart"), nil)
	if evs := events(t, rec); !evs[len(evs)-1].Done || evs[len(evs)-1].Level != "success" {
		t.Fatalf("pull-restart: %+v", evs)
	}
	// The stream has ended. The check has been handed off, not run…
	deadline := time.Now().Add(3 * time.Second)
	queued := func() int { mu.Lock(); defer mu.Unlock(); return len(deferred) }
	for queued() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if queued() != 1 {
		t.Fatalf("background jobs handed off = %d, want the one update check", queued())
	}
	if n := f.Count("DistributionInspect"); n != 0 {
		t.Errorf("the registry was asked %d times before the action ended", n)
	}
	// …and the service is already free for the next action.
	if rec := r.do("POST", actionPath(h.ID, "s~api-1", "restart"), nil); rec.Code != 200 {
		t.Errorf("an action right after the pull: status %d, want 200 (not busy)", rec.Code)
	}
	// Meanwhile the stale answer is gone rather than shown.
	body := decode[servicesBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services", nil))
	if svc, _ := body.service("s~api-1"); svc.Update.Status != "unchecked" {
		t.Errorf("update status before the re-check = %q, want unchecked", svc.Update.Status)
	}
	mu.Lock()
	job := deferred[0]
	mu.Unlock()
	job()
	body = decode[servicesBody](t, r.do("GET", "/api/hosts/"+h.ID+"/services", nil))
	if svc, _ := body.service("s~api-1"); svc.Update.Status != "up_to_date" {
		t.Errorf("update status after the re-check = %q, want up_to_date", svc.Update.Status)
	}
}

// Stopping the panel while it is halfway through replacing a container would
// leave that service down. Shutdown waits for running actions, and takes no
// new ones.
func TestDrainWaitsForRunningActions(t *testing.T) {
	block := blockingAction{entered: make(chan struct{}), release: make(chan struct{})}
	r := newRig(t, func(d *Deps) {
		d.Actions = actions.Default()
		d.Actions.Register(block)
	})
	h, f := r.addHost("aws", false)
	shop(f)

	running := make(chan *httptest.ResponseRecorder, 1)
	go func() { running <- r.do("POST", actionPath(h.ID, "c~shop~api", "block"), nil) }()
	<-block.entered

	drained := make(chan error, 1)
	go func() { drained <- r.srv.drain(context.Background()) }()
	select {
	case err := <-drained:
		t.Fatalf("drain returned (%v) while an action was still running", err)
	case <-time.After(100 * time.Millisecond):
	}

	wantError(t, r.do("POST", actionPath(h.ID, "s~redis-cache", "restart"), nil), http.StatusServiceUnavailable, "shutting_down")
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want no new action started during shutdown", calls)
	}

	close(block.release)
	select {
	case err := <-drained:
		if err != nil {
			t.Errorf("drain: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not return after the action finished")
	}
	if rec := <-running; rec.Code != 200 {
		t.Errorf("the running action: status %d", rec.Code)
	}
}

func TestDrainGivesUpAtItsDeadline(t *testing.T) {
	block := blockingAction{entered: make(chan struct{}), release: make(chan struct{})}
	r := newRig(t, func(d *Deps) {
		d.Actions = actions.Default()
		d.Actions.Register(block)
	})
	h, f := r.addHost("aws", false)
	shop(f)
	go r.do("POST", actionPath(h.ID, "c~shop~api", "block"), nil)
	<-block.entered
	defer close(block.release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.srv.drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("drain = %v, want it to give up at the deadline", err)
	}
}

func TestDrainWithNothingRunningReturnsAtOnce(t *testing.T) {
	r := newRig(t, nil)
	done := make(chan error, 1)
	go func() { done <- r.srv.drain(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain hung with no action running")
	}
}

// The panel must not replace the container it runs in.
func TestPullRestartOnThePanelItselfIsRefused(t *testing.T) {
	var own string
	r := newRig(t, func(d *Deps) { d.SelfContainerID = func() string { return own } })
	h, f := r.addHost("local", false)
	c := dockertest.Running("dockpanel", "ghcr.io/acme/dockpanel:latest")
	c.Image = "sha256:old"
	own = f.AddContainer(c)[:12]
	f.AddImage("ghcr.io/acme/dockpanel:latest", image.InspectResponse{ID: "sha256:old"})
	f.SetPullResult("ghcr.io/acme/dockpanel:latest", image.InspectResponse{ID: "sha256:new"})

	evs := events(t, r.do("POST", actionPath(h.ID, "s~dockpanel", "pull-restart"), nil))
	last := evs[len(evs)-1]
	if last.Level != "error" || !strings.Contains(last.Step, "the panel's own container") {
		t.Errorf("last event = %+v, want a refusal", last)
	}
	for _, call := range f.CallLog() {
		if strings.HasPrefix(call, "ContainerStop") || strings.HasPrefix(call, "ContainerRename") {
			t.Errorf("call %q: the panel started to replace itself", call)
		}
	}
}
