// Command wanbond is the wanbond tunnel daemon. A single binary serves both
// roles (edge and concentrator); the role is selected from configuration at
// runtime, not by which binary is invoked.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/device"
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

// run is the testable entry point: it parses the command line, loads the
// configuration, brings the tunnel up for the configured role, and blocks until
// a termination signal, then tears the tunnel down.
func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("wanbond", version)
		return nil
	}

	fs := flag.NewFlagSet("wanbond", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the TOML configuration file (mode 0600)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("--config is required (or `wanbond version`)")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	lg, err := log.New(cfg.Log.Level, os.Stderr)
	if err != nil {
		return err
	}
	main := lg.Component("main")
	main.Info("wanbond starting", "version", version, "role", string(cfg.Role))

	tun, err := device.Up(cfg, lg)
	if err != nil {
		return err
	}
	defer tun.Close()

	main.Info("tunnel interface up", "interface", tun.Name(), "role", string(cfg.Role))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		main.Info("shutting down", "signal", s.String())
		return nil
	case <-deviceStopped(tun):
		// Unrecoverable engine teardown: fail loud so the exit status reflects it
		// and a supervisor (systemd Restart=on-failure) restarts the daemon.
		return fmt.Errorf("tunnel device stopped unexpectedly")
	}
}

// deviceStopped returns a channel that closes when the tunnel device tears down
// on its own (an unrecoverable engine error), so run can exit rather than block
// on a dead device.
func deviceStopped(t *device.Tunnel) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		t.Wait()
		close(done)
	}()
	return done
}
