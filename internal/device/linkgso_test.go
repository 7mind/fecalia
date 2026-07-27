package device

import (
	"errors"
	"testing"
)

func TestConfigureTUNGSOAppliesBoundedContainerSize(t *testing.T) {
	const wantSize = 16 * 1024
	const wantName = "wanbond7"

	var gotName string
	var gotSize uint32
	err := configureTUNGSO(wantName, func(name string, size uint32) error {
		gotName = name
		gotSize = size
		return nil
	})
	if err != nil {
		t.Fatalf("configure TUN GSO: %v", err)
	}
	if gotName != wantName {
		t.Fatalf("interface name = %q, want %q", gotName, wantName)
	}
	if gotSize != wantSize {
		t.Fatalf("GSO max size = %d, want %d", gotSize, wantSize)
	}
}

func TestConfigureTUNGSOPropagatesSetterFailure(t *testing.T) {
	want := errors.New("netlink rejected GSO size")
	err := configureTUNGSO("wanbond7", func(string, uint32) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("configure TUN GSO error = %v, want %v", err, want)
	}
}
