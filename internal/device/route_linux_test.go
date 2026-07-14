//go:build linux

package device

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestRouteMsgFlags pins the idempotency decision behind default-route install
// (criticism 1): an add MUST carry NLM_F_CREATE|NLM_F_REPLACE and MUST NOT carry
// NLM_F_EXCL. With EXCL, re-installing an already-present route (a leftover /1 after
// an unclean daemon death under tun_persist=true, or a duplicate prefix in the
// computed set) fails EEXIST and wedges every restart; REPLACE adopts/overwrites it
// so bring-up is idempotent. A privileged netlink socket is unavailable in the test
// sandbox, so the flag choice is asserted directly on the pure helper.
func TestRouteMsgFlags(t *testing.T) {
	add := routeMsgFlags(true)
	if add&unix.NLM_F_EXCL != 0 {
		t.Fatalf("add flags %#x carry NLM_F_EXCL: re-installing an existing route would fail EEXIST and wedge restart", add)
	}
	if add&unix.NLM_F_REPLACE == 0 {
		t.Fatalf("add flags %#x lack NLM_F_REPLACE: re-install would not overwrite a stale route", add)
	}
	if add&unix.NLM_F_CREATE == 0 {
		t.Fatalf("add flags %#x lack NLM_F_CREATE: a first-time install would not create the route", add)
	}
	if add&unix.NLM_F_REQUEST == 0 || add&unix.NLM_F_ACK == 0 {
		t.Fatalf("add flags %#x lack NLM_F_REQUEST|NLM_F_ACK: the kernel would not ACK the request", add)
	}

	del := routeMsgFlags(false)
	for _, f := range []struct {
		name string
		bit  int
	}{
		{"NLM_F_CREATE", unix.NLM_F_CREATE},
		{"NLM_F_REPLACE", unix.NLM_F_REPLACE},
		{"NLM_F_EXCL", unix.NLM_F_EXCL},
	} {
		if del&uint16(f.bit) != 0 {
			t.Fatalf("delete flags %#x carry %s: a withdrawal must not create/replace", del, f.name)
		}
	}
	if del&unix.NLM_F_REQUEST == 0 || del&unix.NLM_F_ACK == 0 {
		t.Fatalf("delete flags %#x lack NLM_F_REQUEST|NLM_F_ACK", del)
	}
}
