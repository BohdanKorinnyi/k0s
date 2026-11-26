// Package appinfo is the one place the product's name is defined.
//
// The name also appears in a handful of other places (the frontend, the module
// path, image and volume names); CONTRIBUTING.md lists them under "The name".
package appinfo

const (
	// Name is the product name, used for the binary, cookies and log lines.
	Name = "dc0s"
	// EnvPrefix starts every environment variable the program reads.
	EnvPrefix = "DC0S_"
)

// Version is set at build time with
// -ldflags "-X dc0s/internal/appinfo.Version=1.2.3".
var Version = "dev"
