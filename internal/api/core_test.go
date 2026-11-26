package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"dockpanel/internal/actions"
	"dockpanel/internal/config"
	"dockpanel/internal/store"
	"dockpanel/internal/updates"
)

const testHost = "localhost:8080"

// Keep expected warnings (an unreachable host, a recovered panic) out of the
// test output, so a real problem stands out.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

var testUI = fstest.MapFS{
	"index.html":        {Data: []byte("<!doctype html><title>dockpanel</title>")},
	"assets/app-abc.js": {Data: []byte("console.log('app')")},
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// rig is a server under test with everything it depends on.
type rig struct {
	t     *testing.T
	srv   *server
	h     http.Handler
	store *store.Store
	clock *clock
	hosts *fakeHosts
}

func newRig(t *testing.T, adjust func(d *Deps)) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	hosts := newFakeHosts()
	d := Deps{
		Config:  config.Config{AllowedHosts: append([]string(nil), config.DefaultAllowedHosts...), HealthTimeout: time.Second},
		Store:   st,
		Hosts:   hosts,
		Actions: actions.Default(),
		Updates: updates.NewChecker(nil, clk.now),
		UI:      testUI,
		Now:     clk.now,
	}
	if adjust != nil {
		adjust(&d)
	}
	srv := newServer(d)
	return &rig{t: t, srv: srv, h: srv.handler(), store: st, clock: clk, hosts: hosts}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func withHost(h string) reqOpt      { return func(r *http.Request) { r.Host = h } }
func withCookie(c *http.Cookie) reqOpt {
	return func(r *http.Request) {
		if c != nil {
			r.AddCookie(c)
		}
	}
}

// do sends a request as a same-origin browser would, unless options say otherwise.
func (r *rig) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	r.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			r.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = testHost
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not the expected JSON (%v): %s", err, rec.Body.String())
	}
	return v
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	e := decode[errorBody](t, rec)
	if e.Code != code || e.Error == "" {
		t.Fatalf("error body = %+v, want code %q with a message", e, code)
	}
	return e
}

func TestHostAllowed(t *testing.T) {
	defaults := config.DefaultAllowedHosts
	yes := []string{"localhost:8080", "localhost", "127.0.0.1:8080", "[::1]:8080", "[::1]", "LOCALHOST:8080", "localhost.:8080"}
	for _, h := range yes {
		if !hostAllowed(h, defaults) {
			t.Errorf("hostAllowed(%q) = false, want true", h)
		}
	}
	no := []string{"evil.example", "localhost.evil.example", "evil.example:8080", "", "localhost:8080@evil.example", "localhost:port", "127.0.0.1.nip.io"}
	for _, h := range no {
		if hostAllowed(h, defaults) {
			t.Errorf("hostAllowed(%q) = true, want false", h)
		}
	}
	extra := append(append([]string(nil), defaults...), "panel.lan")
	if !hostAllowed("panel.lan:8080", extra) || !hostAllowed("Panel.LAN", extra) {
		t.Error("an added hostname is not accepted")
	}
}

func TestRefusedHostGetsAnExplanation(t *testing.T) {
	r := newRig(t, nil)
	for _, path := range []string{"/", "/api/session"} {
		rec := r.do("GET", path, nil, withHost("evil.example"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "DOCKPANEL_ALLOWED_HOSTS") {
			t.Errorf("%s: body %q does not say how to allow the name", path, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "<title>dockpanel</title>") {
			t.Errorf("%s: the page was served to a refused host", path)
		}
	}
}

func TestCrossSiteOK(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		want    bool
	}{
		{"same-origin fetch", "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"typed or bookmarked", "POST", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"another site", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"another port on localhost counts as another site", "POST", map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"fetch metadata wins over a matching Origin", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://" + testHost}, false},
		{"older browser, same origin", "POST", map[string]string{"Origin": "http://" + testHost}, true},
		{"older browser, other origin", "POST", map[string]string{"Origin": "http://evil.example"}, false},
		{"older browser, other port", "POST", map[string]string{"Origin": "http://localhost:3000"}, false},
		{"opaque origin", "POST", map[string]string{"Origin": "null"}, false},
		{"not a browser", "POST", nil, true},
		{"reading is always allowed", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"DELETE from another site", "DELETE", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"PUT from another site", "PUT", map[string]string{"Origin": "http://evil.example"}, false},
		{"live connection from another site", "GET", map[string]string{"Upgrade": "websocket", "Origin": "http://evil.example"}, false},
		{"live connection from this site", "GET", map[string]string{"Upgrade": "WebSocket", "Origin": "http://" + testHost}, true},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/api/x", nil)
		req.Host = testHost
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if got := crossSiteOK(req); got != c.want {
			t.Errorf("%s: crossSiteOK = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCrossSiteRequestIsRefused(t *testing.T) {
	r := newRig(t, nil)
	rec := r.do("POST", "/api/login", map[string]string{"password": "x"}, withHeader("Sec-Fetch-Site", "cross-site"))
	wantError(t, rec, http.StatusForbidden, "cross_site")
}

type sessionBody struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	AuthRequired  bool   `json:"authRequired"`
	Authenticated bool   `json:"authenticated"`
}

func TestNoPassword(t *testing.T) {
	r := newRig(t, nil)
	s := decode[sessionBody](t, r.do("GET", "/api/session", nil))
	if s.AuthRequired || !s.Authenticated || s.Name != "dockpanel" || s.Version == "" {
		t.Errorf("session = %+v, want no login needed", s)
	}
	// Without a password every API route is open: an unknown one is simply not found.
	wantError(t, r.do("GET", "/api/nope", nil), http.StatusNotFound, "not_found")
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "dockpanel_session" {
			return c
		}
	}
	return nil
}

func TestPasswordFlow(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })

	s := decode[sessionBody](t, r.do("GET", "/api/session", nil))
	if !s.AuthRequired || s.Authenticated {
		t.Errorf("session before login = %+v", s)
	}
	wantError(t, r.do("GET", "/api/nope", nil), http.StatusUnauthorized, "unauthenticated")
	wantError(t, r.do("POST", "/api/login", map[string]string{"password": "wrong"}), http.StatusUnauthorized, "unauthenticated")
	wantError(t, r.do("POST", "/api/login", "{not json"), http.StatusBadRequest, "invalid")

	rec := r.do("POST", "/api/login", map[string]string{"password": "s3cret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(rec)
	if cookie == nil {
		t.Fatal("no dockpanel_session cookie after a correct password")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/", cookie)
	}
	if cookie.Secure {
		t.Error("cookie is Secure on a plain HTTP request; the browser would never send it back")
	}
	if len(cookie.Value) < 32 {
		t.Errorf("session token %q is too short to be unguessable", cookie.Value)
	}

	// Authenticated: past the login check, the unknown route is merely not found.
	wantError(t, r.do("GET", "/api/nope", nil, withCookie(cookie)), http.StatusNotFound, "not_found")
	s = decode[sessionBody](t, r.do("GET", "/api/session", nil, withCookie(cookie)))
	if !s.Authenticated {
		t.Error("session does not report the login")
	}

	// A made-up token is not a session.
	wantError(t, r.do("GET", "/api/nope", nil, withCookie(&http.Cookie{Name: "dockpanel_session", Value: strings.Repeat("a", 64)})),
		http.StatusUnauthorized, "unauthenticated")

	if rec := r.do("POST", "/api/logout", nil, withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Errorf("logout status = %d", rec.Code)
	}
	wantError(t, r.do("GET", "/api/nope", nil, withCookie(cookie)), http.StatusUnauthorized, "unauthenticated")
}

func TestSessionExpires(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })
	cookie := sessionCookie(r.do("POST", "/api/login", map[string]string{"password": "s3cret"}))

	r.clock.t = r.clock.t.Add(7*24*time.Hour - time.Minute)
	wantError(t, r.do("GET", "/api/nope", nil, withCookie(cookie)), http.StatusNotFound, "not_found")
	r.clock.t = r.clock.t.Add(2 * time.Minute)
	wantError(t, r.do("GET", "/api/nope", nil, withCookie(cookie)), http.StatusUnauthorized, "unauthenticated")
}

func TestSecureCookieBehindTLS(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })
	rec := r.do("POST", "/api/login", map[string]string{"password": "s3cret"}, withHeader("X-Forwarded-Proto", "https"))
	if c := sessionCookie(rec); c == nil || !c.Secure {
		t.Errorf("cookie = %+v, want Secure when the browser reached us over HTTPS", c)
	}
}

func TestLoginRateLimit(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })
	for i := 1; i <= 10; i++ {
		wantError(t, r.do("POST", "/api/login", map[string]string{"password": "wrong"}), http.StatusUnauthorized, "unauthenticated")
	}
	rec := r.do("POST", "/api/login", map[string]string{"password": "wrong"})
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	// Even the right password waits while the limit is in force.
	wantError(t, r.do("POST", "/api/login", map[string]string{"password": "s3cret"}), http.StatusTooManyRequests, "rate_limited")

	r.clock.t = r.clock.t.Add(61 * time.Second)
	if rec := r.do("POST", "/api/login", map[string]string{"password": "s3cret"}); rec.Code != http.StatusOK {
		t.Errorf("after a minute: status = %d, want the correct password accepted", rec.Code)
	}
}

func TestUI(t *testing.T) {
	r := newRig(t, nil)

	// "s~my.app" is a service named after a container with a dot in its name:
	// it must reach the app, not be mistaken for a missing file.
	for _, path := range []string{"/", "/h/aws/s/c~shop~api", "/h/aws/s/s~my.app", "/settings/hosts", "/login"} {
		rec := r.do("GET", path, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>dockpanel</title>") {
			t.Errorf("%s: status %d, body %q; want the app page", path, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache so a new build is picked up", path, cc)
		}
	}

	rec := r.do("GET", "/assets/app-abc.js", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('app')" {
		t.Errorf("asset: status %d, body %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset Cache-Control = %q, want long-lived", cc)
	}

	if rec := r.do("GET", "/assets/missing.js", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: status = %d, want 404 rather than the app page", rec.Code)
	}
	wantError(t, r.do("GET", "/api/nope", nil), http.StatusNotFound, "not_found")
	if rec := r.do("POST", "/h/aws", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to a page: status = %d, want 405", rec.Code)
	}
}

func TestUINotBuilt(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.UI = fstest.MapFS{".gitkeep": {}} })
	rec := r.do("GET", "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "UI not built") {
		t.Errorf("status %d, body %q; want a note that the UI is not built", rec.Code, rec.Body.String())
	}
	// The API still works without the UI.
	if rec := r.do("GET", "/api/session", nil); rec.Code != http.StatusOK {
		t.Errorf("/api/session status = %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	r := newRig(t, nil)
	for _, path := range []string{"/", "/api/session", "/assets/app-abc.js"} {
		h := r.do("GET", path, nil).Header()
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q", path, got)
		}
		want := "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
			"connect-src 'self' ws://localhost:8080 wss://localhost:8080; frame-ancestors 'none'"
		if got := h.Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: Content-Security-Policy =\n  %q\nwant\n  %q", path, got, want)
		}
	}
	// A refused host still gets the headers that do not depend on the host.
	h := r.do("GET", "/", nil, withHost("evil.example")).Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers on a refused request = %v", h)
	}
}

func TestPanicRecovered(t *testing.T) {
	r := newRig(t, nil)
	r.srv.mux.HandleFunc("GET /api/boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	wantError(t, r.do("GET", "/api/boom", nil), http.StatusInternalServerError, "internal")
	if rec := r.do("GET", "/api/session", nil); rec.Code != http.StatusOK {
		t.Errorf("the server stopped answering after a panic: %d", rec.Code)
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })
	big := `{"password":"` + strings.Repeat("x", 2<<20) + `"}`
	rec := r.do("POST", "/api/login", big)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want the oversized body refused", rec.Code)
	}
}
