//go:build linux

package bind

import "syscall"

// bindToDevice pins the socket to the named interface via SO_BINDTODEVICE, so its
// egress and ingress are confined to that device regardless of the source address
// currently assigned to it. It requires CAP_NET_RAW; a permission error is
// returned to the caller, which falls back to source-IP binding.
func bindToDevice(fd uintptr, dev string) error {
	return syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, dev)
}
