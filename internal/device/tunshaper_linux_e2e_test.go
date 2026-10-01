//go:build e2e && linux

package device

import (
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/7mind/wanbond/internal/log"
)

func TestStartupRemovesOnlyTheObsoleteTUNShaper(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root and a disposable Linux guest")
	}
	const name = "wbaqmret0"
	run := func(command string, args ...string) string {
		t.Helper()
		out, err := exec.Command(command, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", command, args, err, out)
		}
		return string(out)
	}
	run("ip", "link", "add", name, "type", "dummy")
	t.Cleanup(func() { run("ip", "link", "delete", name) })
	run("ip", "link", "set", name, "up")
	before, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	logger, err := log.New("info", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	run("tc", "qdisc", "replace", "dev", name, "root", "handle", "1:", "htb", "default", "1")
	run("tc", "class", "replace", "dev", name, "parent", "1:", "classid", "1:1", "htb", "rate", "100kbit")
	run("tc", "qdisc", "replace", "dev", name, "parent", "1:1", "handle", "10:", "bfifo", "limit", "65000")
	tunnel := &Tunnel{name: name, log: logger}
	if err := tunnel.removeObsoleteTUNShaper(); err != nil {
		t.Fatal(err)
	}
	if got := run("tc", "qdisc", "show", "dev", name); strings.Contains(got, "htb") || strings.Contains(got, "bfifo") {
		t.Fatalf("obsolete rate cap retained: %s", got)
	}
	if err := tunnel.removeObsoleteTUNShaper(); err != nil {
		t.Fatalf("second cleanup failed: %v", err)
	}
	run("tc", "qdisc", "replace", "dev", name, "root", "handle", "2:", "netem", "delay", "1ms")
	if err := tunnel.removeObsoleteTUNShaper(); err != nil {
		t.Fatal(err)
	}
	if got := run("tc", "qdisc", "show", "dev", name); !strings.Contains(got, "netem 2:") {
		t.Fatalf("operator qdisc changed: %s", got)
	}
	after, err := net.InterfaceByName(name)
	if err != nil || after.Index != before.Index {
		t.Fatalf("persistent interface replaced: before=%v after=%v error=%v", before, after, err)
	}
}
