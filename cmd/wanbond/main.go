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

	// SIGINT/SIGTERM terminate; SIGHUP reloads the config file to add/remove paths
	// on the running tunnel (T30). SIGHUP is idiomatic for a daemon config reload and
	// needs no extra socket or privilege.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	stopped := deviceStopped(tun)
	for {
		select {
		case s := <-sig:
			if s == syscall.SIGHUP {
				reloadTunnel(main, tun, *configPath)
				continue
			}
			main.Info("shutting down", "signal", s.String())
			return nil
		case <-stopped:
			// Unrecoverable engine teardown: fail loud so the exit status reflects it
			// and a supervisor (systemd Restart=on-failure) restarts the daemon.
			return fmt.Errorf("tunnel device stopped unexpectedly")
		}
	}
}

// reloadTunnel re-reads and validates the config file and applies its path diff to
// the running tunnel. A bad reload (unreadable, insecure-mode, invalid, or a failed
// path change) is LOGGED and the running tunnel is left intact — a reload must never
// tear down a live session. config.Load performs the fail-fast validation.
func reloadTunnel(lg log.Logger, tun *device.Tunnel, configPath string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		lg.Error("config reload rejected; keeping running config", "error", err.Error())
		return
	}
	if err := tun.Reload(cfg); err != nil {
		lg.Error("config reload failed to apply; tunnel still running", "error", err.Error())
		return
	}
	lg.Info("config reloaded")
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
