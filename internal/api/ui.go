package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const uiNotBuilt = `UI not built.

The API is running, but this binary was built without the frontend.
Run "make web" and rebuild, or use the Docker image, which includes it.
`

// uiHandler serves the built frontend. The frontend is a single-page app: its
// own routes (/h/aws, /settings/hosts) do not exist as files, so any path that
// is not a file gets index.html and the app takes it from there.
func (s *server) uiHandler() http.Handler {
	ui := s.deps.UI
	hasIndex := false
	if ui != nil {
		if info, err := fs.Stat(ui, "index.html"); err == nil && !info.IsDir() {
			hasIndex = true
		}
	}
	var files http.Handler
	if ui != nil {
		files = http.FileServerFS(ui)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasIndex {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(uiNotBuilt))
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(ui, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Build output: the file name contains a hash of its content.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			// A missing asset or top-level file (favicon.ico) is a 404, not
			// the app. Deeper paths are app routes even when they contain a
			// dot: a service can be called "my.app".
			if strings.HasPrefix(name, "assets/") || (!strings.Contains(name, "/") && path.Ext(name) != "") {
				http.NotFound(w, r)
				return
			}
		}

		// Never cached, so a new build is picked up on the next load.
		w.Header().Set("Cache-Control", "no-cache")
		index := r.Clone(r.Context())
		index.URL.Path = "/"
		files.ServeHTTP(w, index)
	})
}
