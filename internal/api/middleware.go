package api

import (
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"

	"dockpanel/internal/appinfo"
)

// recoverPanics turns a crash in a handler into a 500 instead of a dropped
// connection, and keeps the server up.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil || v == http.ErrAbortHandler {
				return
			}
			slog.Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
			writeError(w, http.StatusInternalServerError, "internal", "something went wrong inside the panel; see its log")
		}()
		next.ServeHTTP(w, r)
	})
}

// baseHeaders sets the response headers that hold for every response.
func baseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// The panel has buttons that stop services; it must never be drawn
		// inside another page where clicks could be steered onto them.
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// requireAllowedHost answers only requests addressed to a hostname on the
// allow list.
//
// Without this, any website could point a hostname of its own at 127.0.0.1
// ("DNS rebinding") and have the visitor's browser talk to the panel as if it
// were that site, reading env vars and running shells.
func (s *server) requireAllowedHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, s.deps.Config.AllowedHosts) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("This panel only answers requests addressed to: " +
				strings.Join(s.deps.Config.AllowedHosts, ", ") + ".\n" +
				"To reach it under another name, add that name to " + appinfo.EnvPrefix + "ALLOWED_HOSTS.\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether a Host header names an allowed hostname. The
// port is ignored; anything malformed is refused.
func hostAllowed(hostHeader string, allowed []string) bool {
	name, ok := hostName(hostHeader)
	if !ok {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	return false
}

// hostName extracts the lower-case hostname from "name", "name:port",
// "[v6]" or "[v6]:port".
func hostName(hostHeader string) (string, bool) {
	name, port, hasPort := hostHeader, "", false
	if strings.HasPrefix(hostHeader, "[") {
		end := strings.IndexByte(hostHeader, ']')
		if end < 0 {
			return "", false
		}
		name = hostHeader[1:end]
		if rest := hostHeader[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", false
			}
			port, hasPort = rest[1:], true
		}
	} else if i := strings.LastIndexByte(hostHeader, ':'); i >= 0 {
		name, port, hasPort = hostHeader[:i], hostHeader[i+1:], true
	}
	if hasPort && !allDigits(port) {
		return "", false
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	return name, name != ""
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// contentSecurityPolicy restricts what the page may load and connect to: only
// this server. It runs after the host check, so the Host header it echoes into
// the policy is one we answer to, in a known-safe form.
func contentSecurityPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				// The terminal component sets inline styles.
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				// Named explicitly: not every browser lets 'self' cover WebSockets.
				"connect-src 'self' ws://"+r.Host+" wss://"+r.Host+"; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// refuseCrossSite turns away state-changing requests and live connections
// that another website caused the browser to send.
func refuseCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !crossSiteOK(r) {
			writeError(w, http.StatusForbidden, "cross_site", "this request came from another website and was refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossSiteOK decides whether a request may proceed with respect to where it
// came from.
//
// Plain reads are always fine: the browser will not let another site see the
// answer. What must be stopped is another site making the browser *do*
// something here — a POST, or opening a WebSocket, which is a GET but gives a
// two-way channel that ordinary cross-site rules do not cover.
//
// Browsers say where a request came from in Sec-Fetch-Site, and older ones in
// Origin. A request with neither was not sent by a browser (curl, a script),
// and cross-site forgery is not a concern for it.
func crossSiteOK(r *http.Request) bool {
	safeMethod := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
	websocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if safeMethod && !websocket {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		// "same-site" is not enough: another port on localhost is the same
		// site but a different program.
		return site == "same-origin" || site == "none"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
	}
	return true
}
