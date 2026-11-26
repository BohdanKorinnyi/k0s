// Package webui carries the built frontend inside the binary.
//
// The dist directory is filled by "make web" (or the Docker build). In a fresh
// checkout it holds only a placeholder, and the server then answers
// "UI not built" instead of a page.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built frontend, with index.html at its root.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the directory is embedded above; this cannot fail
	}
	return sub
}
