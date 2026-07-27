package device

import (
	"errors"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

func pacedGSOConfig(rates ...float64) *config.Config {
	paths := make([]config.Path, len(rates))
	shapers := make([]config.PathShaperConfig, len(rates))
	for i, rate := range rates {
		shapers[i].RateBytesPerSecond = rate
	}
	return &config.Config{
		Paths: paths,
		Scheduler: config.SchedulerConfig{
			PacingEnabled:  true,
			PerPathShapers: shapers,
		},
	}
}

func TestDesiredTUNGSOMaxSizeBoundsSlowestShaperServiceTime(t *testing.T) {
	cfg := pacedGSOConfig(4_000_000, 1_000_000)
	got, change, err := desiredTUNGSOMaxSize(cfg)
	if err != nil {
		t.Fatalf("derive TUN GSO size: %v", err)
	}
	if !change {
		t.Fatal("derived TUN GSO size reported no change")
	}
	const want = 5_000
	if got != want {
		t.Fatalf("GSO max size = %d, want %d", got, want)
	}
}

func TestDesiredTUNGSOMaxSizeClampsToInitialMTU(t *testing.T) {
	cfg := pacedGSOConfig(1)
	got, change, err := desiredTUNGSOMaxSize(cfg)
	if err != nil {
		t.Fatalf("derive TUN GSO size: %v", err)
	}
	if !change {
		t.Fatal("derived TUN GSO size reported no change")
	}
	if want := uint32(tunMTU(cfg)); got != want {
		t.Fatalf("GSO max size = %d, want initial TUN MTU %d", got, want)
	}
}

func TestDesiredTUNGSOMaxSizeLeavesKernelDefaultForFastShaper(t *testing.T) {
	cfg := pacedGSOConfig(20_000_000)
	got, change, err := desiredTUNGSOMaxSize(cfg)
	if err != nil {
		t.Fatalf("derive TUN GSO size: %v", err)
	}
	if change {
		t.Fatalf("derived TUN GSO size = %d with change=true, want kernel default untouched", got)
	}
}

func TestConfigureTUNGSOAppliesAndReadsBackBoundedContainerSize(t *testing.T) {
	cfg := pacedGSOConfig(1_000_000)
	const wantSize = 5_000
	const wantName = "wanbond7"

	var gotName string
	var gotSize uint32
	err := configureTUNGSO(
		cfg,
		wantName,
		func(name string) (uint32, error) {
			if name != wantName {
				t.Fatalf("readback interface name = %q, want %q", name, wantName)
			}
			return gotSize, nil
		},
		func(name string, size uint32) error {
			gotName = name
			gotSize = size
			return nil
		},
	)
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

func TestConfigureTUNGSOLeavesUnpacedConfigurationUntouched(t *testing.T) {
	called := false
	err := configureTUNGSO(
		&config.Config{},
		"wanbond7",
		func(string) (uint32, error) {
			called = true
			return 0, nil
		},
		func(string, uint32) error {
			called = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("configure unpaced TUN GSO: %v", err)
	}
	if called {
		t.Fatal("unpaced configuration invoked GSO reader or setter")
	}
}

func TestConfigureTUNGSOReappliesAfterPersistentTUNAdoption(t *testing.T) {
	cfg := pacedGSOConfig(1_000_000)
	current := uint32(linuxDefaultGSOMaxSize)
	sets := 0
	for range 2 {
		err := configureTUNGSO(
			cfg,
			"wanbond7",
			func(string) (uint32, error) { return current, nil },
			func(_ string, size uint32) error {
				current = size
				sets++
				return nil
			},
		)
		if err != nil {
			t.Fatalf("configure persistent TUN GSO: %v", err)
		}
	}
	if sets != 2 {
		t.Fatalf("setter calls = %d, want 2 startup applications", sets)
	}
	if current != 5_000 {
		t.Fatalf("persistent TUN GSO max size = %d, want 5000", current)
	}
}

func TestConfigureTUNGSOFailsOnReadbackMismatch(t *testing.T) {
	cfg := pacedGSOConfig(1_000_000)
	err := configureTUNGSO(
		cfg,
		"wanbond7",
		func(string) (uint32, error) { return linuxDefaultGSOMaxSize, nil },
		func(string, uint32) error { return nil },
	)
	if err == nil {
		t.Fatal("configure TUN GSO succeeded with mismatched readback")
	}
}

func TestConfigureTUNGSOPropagatesReadbackFailure(t *testing.T) {
	cfg := pacedGSOConfig(1_000_000)
	want := errors.New("netlink readback failed")
	err := configureTUNGSO(
		cfg,
		"wanbond7",
		func(string) (uint32, error) { return 0, want },
		func(string, uint32) error { return nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("configure TUN GSO error = %v, want %v", err, want)
	}
}

func TestConfigureTUNGSOPropagatesSetterFailure(t *testing.T) {
	cfg := pacedGSOConfig(1_000_000)
	want := errors.New("netlink rejected GSO size")
	err := configureTUNGSO(
		cfg,
		"wanbond7",
		func(string) (uint32, error) { return 0, nil },
		func(string, uint32) error { return want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("configure TUN GSO error = %v, want %v", err, want)
	}
}
