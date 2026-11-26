package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"dockpanel/internal/appinfo"
)

const (
	sessionCookieName = appinfo.Name + "_session"
	sessionLifetime   = 7 * 24 * time.Hour

	// At most this many wrong passwords are accepted per minute, in total.
	maxLoginFailures = 10
	loginWindow      = time.Minute
	// How long a login request may take to send its few bytes.
	loginReadLimit = 10 * time.Second
)

// sessionStore keeps logged-in sessions in memory. Restarting the panel logs
// everyone out, which for a single-user tool is an acceptable price for having
// nothing to persist or leak.
type sessionStore struct {
	now func() time.Time
	mu  sync.Mutex
	// expiry by token
	tokens map[string]time.Time
}

func newSessionStore(now func() time.Time) *sessionStore {
	return &sessionStore{now: now, tokens: map[string]time.Time{}}
}

func (s *sessionStore) create() (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = hex.EncodeToString(raw)
	expires = s.now().Add(sessionLifetime)

	s.mu.Lock()
	defer s.mu.Unlock()
	for t, exp := range s.tokens { // drop the expired while we are here
		if !s.now().Before(exp) {
			delete(s.tokens, t)
		}
	}
	s.tokens[token] = expires
	return token, expires, nil
}

func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[token]
	if !ok {
		return false
	}
	if !s.now().Before(exp) {
		delete(s.tokens, token)
		return false
	}
	return true
}

func (s *sessionStore) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// failureLimiter slows password guessing: after a number of wrong passwords
// within a minute, logins are refused until the minute has passed.
//
// A guess takes a slot before its password is even read, and gives it back
// only if the password was right. Counting after the check instead would let
// any number of guesses that arrive together all be checked.
type failureLimiter struct {
	now      func() time.Time
	mu       sync.Mutex
	failures []time.Time
	pending  int // guesses that hold a slot and are not yet judged
}

func newFailureLimiter(now func() time.Time) *failureLimiter {
	return &failureLimiter{now: now}
}

// begin takes a slot for one guess, or reports that there is none.
func (l *failureLimiter) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-loginWindow)
	kept := l.failures[:0]
	for _, t := range l.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.failures = kept
	if len(l.failures)+l.pending >= maxLoginFailures {
		return false
	}
	l.pending++
	return true
}

// end settles a guess begun earlier: a wrong one keeps its slot for a minute.
func (l *failureLimiter) end(wrong bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending--
	if wrong {
		l.failures = append(l.failures, l.now())
	}
}

func (s *server) passwordSet() bool { return s.deps.Config.Password != "" }

func (s *server) authenticated(r *http.Request) bool {
	if !s.passwordSet() {
		return true
	}
	c, err := r.Cookie(sessionCookieName)
	return err == nil && s.sessions.valid(c.Value)
}

// requireSession guards the API when a password is set. The page itself, the
// session probe and the login call stay open: they are how one logs in.
func (s *server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		open := !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/session" || r.URL.Path == "/api/login"
		if !open && !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "log in to use the panel")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":          appinfo.Name,
		"version":       appinfo.Version,
		"authRequired":  s.passwordSet(),
		"authenticated": s.authenticated(r),
	})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.passwordSet() {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}
	if !s.logins.begin() {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many wrong passwords; wait a minute and try again")
		return
	}
	wrong := true
	defer func() { s.logins.end(wrong) }()

	// A guess may not sit on its slot, or a connection, by trickling its body.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(loginReadLimit))
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		wrong = false // malformed, not a guess
		return
	}
	// Compare digests, in constant time, so neither the length nor the
	// position of the first wrong character can be timed.
	given, want := sha256.Sum256([]byte(body.Password)), sha256.Sum256([]byte(s.deps.Config.Password))
	if subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "wrong password")
		return
	}
	wrong = false

	token, expires, err := s.sessions.create()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not start a session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   viaHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   viaHTTPS(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// viaHTTPS reports whether the browser reached the panel over HTTPS, directly
// or through a proxy that says so. A Secure cookie on plain HTTP would never
// be sent back, so it is set only then.
func viaHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
