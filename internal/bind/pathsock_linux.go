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
