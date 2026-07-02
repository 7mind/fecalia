// Command wanbond is the wanbond tunnel daemon. A single binary serves both
// roles (edge and concentrator); the role is selected from configuration at
// runtime, not by which binary is invoked.
package main

import (
	"fmt"
	"os"

	"github.com/7mind/wanbond/internal/log"
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
// reports the build version and exercises the structured logger so the skeleton
// binary is runnable and its logging path is live.
func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("wanbond", version)
		return nil
	}

	lg, err := log.New("info", os.Stderr)
	if err != nil {
		return err
	}
	main := lg.Component("main")
	main.Info("wanbond starting", "version", version)
	defer main.Info("wanbond stopped")

	fmt.Println("wanbond", version, "- config-driven WAN bonding tunnel (edge|concentrator)")
	return nil
}
