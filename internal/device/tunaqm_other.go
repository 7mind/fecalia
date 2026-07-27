//go:build !linux

package device

func (t *Tunnel) startTUNAQM() error {
	return nil
}
