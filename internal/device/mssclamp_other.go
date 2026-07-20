//go:build !linux

package device

import "errors"

// tcpV4MSSOverhead / clampMSS are the platform-independent MSS-derivation half of the
// clamp (inner IPv4+TCP header cost); they carry no OS dependency, so the same definition
// serves every platform and keeps TestClampMSS building off Linux too.
const tcpV4MSSOverhead = 40

func clampMSS(innerMTU int) int { return innerMTU - tcpV4MSSOverhead }

// installMSSClamp/removeMSSClamp are unavailable off Linux (the clamp is programmed via
// the iptables/ip6tables netfilter front-ends, a Linux facility). wanbond targets Linux;
// these stubs only keep the package cross-compilable, mirroring route_other.go. They are
// reached only on the edge-role bring-up path, which on a non-Linux host has already
// failed at ifUp (SIOCSIFFLAGS), so a default config still cross-compiles unchanged.
func installMSSClamp(string) error {
	return errors.New("device: installing the MSS clamp is only supported on Linux")
}

func removeMSSClamp(string) error {
	return errors.New("device: removing the MSS clamp is only supported on Linux")
}
