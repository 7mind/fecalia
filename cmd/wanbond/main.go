// Command wanbond is the wanbond tunnel daemon. A single binary serves both
// roles (edge and concentrator); the role is selected from configuration at
// runtime, not by which binary is invoked.
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wanbond:", err)
		os.Exit(1)
	}
}

// run is the testable entry point. The full command-line surface (config path,
// role dispatch to edge/concentrator) is implemented in later tasks; for now it
// only reports the build version so the skeleton binary is runnable.
func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("wanbond", version)
		return nil
	}
	fmt.Println("wanbond", version, "- config-driven WAN bonding tunnel (edge|concentrator)")
	return nil
}
