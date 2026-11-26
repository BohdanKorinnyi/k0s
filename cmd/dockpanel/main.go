// Command dockpanel serves the web control panel.
package main

import (
	"flag"
	"fmt"

	"dockpanel/internal/appinfo"
)

func main() {
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *version {
		fmt.Println(appinfo.Name, appinfo.Version)
		return
	}
	fmt.Println(appinfo.Name, appinfo.Version)
}
