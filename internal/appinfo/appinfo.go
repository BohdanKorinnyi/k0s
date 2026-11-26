// Package appinfo is the one place the product's name is defined.
//
// To rename the project, change Name and EnvPrefix here, the constants in
// web/src/appinfo.ts, the Go module path, and the image name in the Makefile
// and compose.yaml.
package appinfo

const (
	// Name is the product name, used for the binary, cookies and log lines.
	Name = "dockpanel"
	// EnvPrefix starts every environment variable the program reads.
	EnvPrefix = "DOCKPANEL_"
)

// Version is set at build time with
// -ldflags "-X dockpanel/internal/appinfo.Version=1.2.3".
var Version = "dev"
