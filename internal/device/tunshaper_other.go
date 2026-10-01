//go:build !linux

package device

func (t *Tunnel) removeObsoleteTUNShaper() error {
	return nil
}
