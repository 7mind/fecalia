//go:build linux

package bind

import "syscall"

// bindToDevice pins the socket to the named interface via SO_BINDTODEVICE, so its
// egress and ingress are confined to that device regardless of the source address
// currently assigned to it. On Linux >=5.7 (kernel commit c427bfec18f21, "net:
// core: enable SO_BINDTODEVICE for non-root users") this needs no capability at
// all for a not-yet-bound socket, which is the only case this daemon exercises;
// empirically confirmed (D40) with SO_BINDTODEVICE succeeding under a
// zero-capability, non-root process on a 6.17 kernel. Pre-5.7 kernels still
// require CAP_NET_RAW, which the shipped systemd units don't grant (only
// CAP_NET_ADMIN). Either way, a permission error (EPERM, on a pre-5.7 kernel
// lacking that capability) is returned to the caller, which falls back to
// source-IP binding.
func bindToDevice(fd uintptr, dev string) error {
	return syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, dev)
}

// setDontFragment sets the outer path socket's MTU-discovery policy to PMTUDISC_DO,
// which pins the Don't-Fragment bit on every outgoing datagram (T201). This is the
// precondition for meaningful padded-probe PMTU discovery — without DF the kernel
// silently fragments an oversized probe or data datagram, so the probe can never
// learn the real path MTU — and it converts today's silent-fragmentation loss into an
// explicit EMSGSIZE at send, which the caller counts and rate-limits (accountSendError)
// rather than swallowing. isV6 selects the AF_INET6 option (IPV6_MTU_DISCOVER) over the
// AF_INET one (IP_MTU_DISCOVER); the two share the numeric PMTUDISC_DO level. It mirrors
// bindToDevice: Linux-only, applied on the raw fd in the socket's Control hook, with a
// portable no-op stub (pathsock_other.go) so non-Linux builds still compile.
func setDontFragment(fd uintptr, isV6 bool) error {
	if isV6 {
		return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MTU_DISCOVER, syscall.IPV6_PMTUDISC_DO)
	}
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER, syscall.IP_PMTUDISC_DO)
}
