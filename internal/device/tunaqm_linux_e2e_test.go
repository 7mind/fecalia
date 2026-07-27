//go:build e2e && linux

package device

import (
	"os"
	"os/exec"
	"testing"
)

func TestLinuxTUNAQMReconciliationContract(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a temporary link")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal(err)
	}
	const name = "wbaqmtest0"
	_ = exec.Command(ip, "link", "delete", name).Run()
	if output, err := exec.Command(ip, "link", "add", name, "type", "dummy").CombinedOutput(); err != nil {
		t.Fatalf("create temporary link: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command(ip, "link", "delete", name).CombinedOutput(); err != nil {
			t.Errorf("delete temporary link: %v: %s", err, output)
		}
	})
	if output, err := exec.Command(ip, "link", "set", "dev", name, "up").CombinedOutput(); err != nil {
		t.Fatalf("bring temporary link up: %v: %s", err, output)
	}
	kernel, err := newLinuxTUNAQMKernel(name)
	if err != nil {
		t.Fatal(err)
	}
	testTUNAQMReconciliationContract(t, kernel)
}
