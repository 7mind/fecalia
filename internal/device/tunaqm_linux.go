//go:build linux

package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/congestion"
	"github.com/7mind/wanbond/internal/telemetry"
	"golang.org/x/sys/unix"
)

const tunAQMCommandTimeout = 3 * time.Second

var tunAQMRatePattern = regexp.MustCompile(`(?m)^class htb 1:1 .* rate ([0-9]+(?:\.[0-9]+)?)([KMG]?)bit(?: |$)`)

type linuxTUNAQMKernel struct {
	name            string
	tc              string
	command         func(args ...string) ([]byte, error)
	readTxQueueLen  func() (int, error)
	writeTxQueueLen func(int) error
	readGSOLimits   func() (linkGSOLimits, error)
	writeGSOLimits  func(linkGSOLimits) error
}

func newLinuxTUNAQMKernel(name string) (*linuxTUNAQMKernel, error) {
	if name == "" {
		return nil, errors.New("TUN AQM interface name is required")
	}
	tc, err := findTCBinary()
	if err != nil {
		return nil, err
	}
	kernel := &linuxTUNAQMKernel{name: name, tc: tc}
	kernel.command = kernel.execute
	kernel.readTxQueueLen = func() (int, error) {
		return linkTxQueueLen(name)
	}
	kernel.writeTxQueueLen = func(length int) error {
		return setLinkTxQueueLen(name, length)
	}
	kernel.readGSOLimits = func() (linkGSOLimits, error) {
		return readLinkGSOLimits(name)
	}
	kernel.writeGSOLimits = func(limits linkGSOLimits) error {
		return setLinkGSOLimits(name, limits)
	}
	return kernel, nil
}

func findTCBinary() (string, error) {
	if path, err := exec.LookPath("tc"); err == nil {
		return path, nil
	}
	for _, path := range []string{"/usr/sbin/tc", "/sbin/tc"} {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("device: tc binary is required for TUN AQM")
}

func (k *linuxTUNAQMKernel) Apply(target tunAQMTargetState) error {
	current, readErr := k.Read()
	topologyReady := readErr == nil &&
		current.RootKind == "htb"
	leafReady := topologyReady &&
		current.LeafKind == "fq" &&
		current.Limit == target.QueueLimit &&
		current.FlowLimit == target.QueueLimit &&
		current.Quantum == target.MTU &&
		current.InitialQuantum == target.MTU
	if !topologyReady {
		if err := k.run(
			"qdisc", "replace", "dev", k.name,
			"root", "handle", "1:", "htb", "default", "1",
		); err != nil {
			if readErr != nil {
				return fmt.Errorf("device: restore TUN AQM after readback failure: %w",
					errors.Join(readErr, err))
			}
			return err
		}
	}

	if !topologyReady ||
		math.Abs(current.RateBytesPerSecond-target.RateBytesPerSecond) >
			target.RateBytesPerSecond*0.01 {
		rateBits := strconv.FormatInt(int64(math.Round(target.RateBytesPerSecond*8)), 10) + "bit"
		if err := k.run(
			"class", "replace", "dev", k.name,
			"parent", "1:", "classid", "1:1", "htb",
			"rate", rateBits, "ceil", rateBits,
		); err != nil {
			return err
		}
	}

	if !leafReady {
		if topologyReady && current.LeafKind != "" {
			if err := k.run(
				"qdisc", "delete", "dev", k.name, "parent", "1:1",
			); err != nil {
				return err
			}
		}
		if err := k.run(
			"qdisc", "add", "dev", k.name,
			"parent", "1:1", "handle", "10:", "fq",
			"limit", strconv.Itoa(target.QueueLimit),
			"flow_limit", strconv.Itoa(target.QueueLimit),
			"quantum", strconv.Itoa(target.MTU),
			"initial_quantum", strconv.Itoa(target.MTU),
		); err != nil {
			return err
		}
	}
	if !topologyReady || current.TxQueueLen != target.TxQueueLen {
		if err := k.writeTxQueueLen(target.TxQueueLen); err != nil {
			return err
		}
	}
	if current.GSOMaxSize != target.GSOMaxSize ||
		current.GSOMaxSegments != target.GSOMaxSegments {
		if err := k.writeGSOLimits(linkGSOLimits{
			MaxSize:     uint32(target.GSOMaxSize),
			MaxSegments: uint32(target.GSOMaxSegments),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (k *linuxTUNAQMKernel) Read() (tunAQMActualState, error) {
	txQueueLen, err := k.readTxQueueLen()
	if err != nil {
		return tunAQMActualState{}, err
	}
	gsoLimits, err := k.readGSOLimits()
	if err != nil {
		return tunAQMActualState{}, err
	}
	rawQdisc, err := k.output("-j", "qdisc", "show", "dev", k.name)
	if err != nil {
		return tunAQMActualState{}, err
	}
	var qdiscs []struct {
		Kind    string `json:"kind"`
		Root    bool   `json:"root"`
		Parent  string `json:"parent"`
		Options struct {
			Limit          int `json:"limit"`
			FlowLimit      int `json:"flow_limit"`
			Quantum        int `json:"quantum"`
			InitialQuantum int `json:"initial_quantum"`
		} `json:"options"`
	}
	if err := json.Unmarshal(rawQdisc, &qdiscs); err != nil {
		return tunAQMActualState{}, fmt.Errorf("device: decode tc qdisc readback: %w", err)
	}
	rootKind := ""
	leafKind := ""
	var leafOptions struct {
		Limit          int
		FlowLimit      int
		Quantum        int
		InitialQuantum int
	}
	for _, qdisc := range qdiscs {
		if qdisc.Root {
			rootKind = qdisc.Kind
		}
		if qdisc.Parent == "1:1" {
			leafKind = qdisc.Kind
			leafOptions.Limit = qdisc.Options.Limit
			leafOptions.FlowLimit = qdisc.Options.FlowLimit
			leafOptions.Quantum = qdisc.Options.Quantum
			leafOptions.InitialQuantum = qdisc.Options.InitialQuantum
		}
	}
	rawClass, err := k.output("class", "show", "dev", k.name)
	if err != nil {
		return tunAQMActualState{}, err
	}
	rate, err := parseTUNAQMRate(string(rawClass))
	if err != nil {
		return tunAQMActualState{}, err
	}
	return tunAQMActualState{
		RateBytesPerSecond: rate,
		TxQueueLen:         txQueueLen,
		RootKind:           rootKind,
		LeafKind:           leafKind,
		Limit:              leafOptions.Limit,
		FlowLimit:          leafOptions.FlowLimit,
		Quantum:            leafOptions.Quantum,
		InitialQuantum:     leafOptions.InitialQuantum,
		GSOMaxSize:         int(gsoLimits.MaxSize),
		GSOMaxSegments:     int(gsoLimits.MaxSegments),
		ObservedAt:         time.Now(),
	}, nil
}

func parseTUNAQMRate(output string) (float64, error) {
	match := tunAQMRatePattern.FindStringSubmatch(output)
	if match == nil {
		return 0, errors.New("device: tc class readback has no htb 1:1 rate")
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, fmt.Errorf("device: parse tc class rate: %w", err)
	}
	switch match[2] {
	case "":
	case "K":
		value *= 1_000
	case "M":
		value *= 1_000_000
	case "G":
		value *= 1_000_000_000
	default:
		return 0, fmt.Errorf("device: unsupported tc class rate unit %q", match[2])
	}
	return value / 8, nil
}

func (k *linuxTUNAQMKernel) run(args ...string) error {
	_, err := k.output(args...)
	return err
}

func (k *linuxTUNAQMKernel) output(args ...string) ([]byte, error) {
	return k.command(args...)
}

func (k *linuxTUNAQMKernel) execute(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tunAQMCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, k.tc, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("device: tc %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func setLinkTxQueueLen(name string, length int) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	ifr.SetUint32(uint32(length))
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFTXQLEN, ifr); err != nil {
		return fmt.Errorf("SIOCSIFTXQLEN %q=%d: %w", name, length, err)
	}
	return nil
}

func linkTxQueueLen(name string) (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return 0, fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return 0, fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFTXQLEN, ifr); err != nil {
		return 0, fmt.Errorf("SIOCGIFTXQLEN %q: %w", name, err)
	}
	return int(ifr.Uint32()), nil
}

func (t *Tunnel) startTUNAQM() error {
	if !t.cfg.Scheduler.PacingEnabled ||
		t.cfg.Scheduler.Policy != config.PolicyActiveBackup ||
		len(t.cfg.Scheduler.PerPathShapers) == 0 {
		return nil
	}
	kernel, err := newLinuxTUNAQMKernel(t.name)
	if err != nil {
		return err
	}
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		return err
	}
	initial, err := congestion.ConservativeSeed(
		t.cfg.Scheduler.PerPathShapers[0].RateBytesPerSecond,
	)
	if err != nil {
		return err
	}
	peerCount := len(t.cfg.WireGuard.Peers)
	if peerCount == 0 {
		peerCount = 1
	}
	maxDataBurstBytes := 0
	for _, shaper := range t.cfg.Scheduler.PerPathShapers {
		if shaper.DataBurstBytes > maxDataBurstBytes {
			maxDataBurstBytes = shaper.DataBurstBytes
		}
	}
	mtu := t.currentTunMTU()
	maximumMTU := tunMTU(t.cfg)
	bounds, err := deriveEngineOutboundBounds(
		initial.IngressRateBytesPerSecond*float64(peerCount),
		peerCount, mtu, maximumMTU, maxDataBurstBytes,
	)
	if err != nil {
		return err
	}
	queueLimit, err := deriveTUNAQMQueueLimit(
		bounds.AdmissionLimitBytes, peerCount, mtu,
	)
	if err != nil {
		return err
	}
	target := tunAQMTargetState{
		RateBytesPerSecond:  initial.IngressRateBytesPerSecond * float64(peerCount),
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 mtu,
		QueueLimit:          queueLimit,
		GSOMaxSize:          bounds.GSOMaxSize,
		GSOMaxSegments:      bounds.GSOMaxSegments,
		AdmissionLimitBytes: bounds.AdmissionLimitBytes,
	}
	initialSnapshot, err := reconciler.Reconcile(target)
	if err != nil {
		return fmt.Errorf("device: install TUN AQM: %w", err)
	}
	if err := t.dev.SetOutboundAdmissionLimit(target.AdmissionLimitBytes); err != nil {
		return fmt.Errorf("device: install engine outbound admission: %w", err)
	}
	if err := t.bind.ObserveTUNIngressActual(
		initialSnapshot.Actual.RateBytesPerSecond,
		initialSnapshot.Actual.Epoch,
		initialSnapshot.Actual.ObservedAt,
		initialSnapshot.Actual.Fresh,
	); err != nil {
		return fmt.Errorf("device: acknowledge initial TUN AQM: %w", err)
	}
	t.tunAQM = reconciler
	if source, ok := t.metricsSrc.(*metricsSource); ok {
		source.setTUNAQMLookup(reconciler.MetricsSnapshot)
	}
	if source, ok := t.monitorSrc.(*metricsSource); ok {
		source.setTUNAQMLookup(reconciler.MetricsSnapshot)
	}
	t.log.Info("TUN AQM installed",
		"interface", t.name,
		"rate_bytes_per_second", target.RateBytesPerSecond,
		"tx_queue_len", target.TxQueueLen)

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(telemetry.DefaultProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				rate, epoch, ok := t.bind.TUNIngressTarget()
				if ok {
					target.RateBytesPerSecond = rate
					target.Epoch = epoch
				}
				target.MTU = t.currentTunMTU()
				bounds, err := deriveEngineOutboundBounds(
					target.RateBytesPerSecond, peerCount, target.MTU, maximumMTU,
					maxDataBurstBytes,
				)
				if err != nil {
					t.log.Error("engine outbound bound derivation failed", "error", err.Error())
					continue
				}
				queueLimit, err := deriveTUNAQMQueueLimit(
					bounds.AdmissionLimitBytes, peerCount, target.MTU,
				)
				if err != nil {
					t.log.Error("TUN AQM queue-limit derivation failed", "error", err.Error())
					continue
				}
				target.QueueLimit = queueLimit
				target.GSOMaxSize = bounds.GSOMaxSize
				target.GSOMaxSegments = bounds.GSOMaxSegments
				target.AdmissionLimitBytes = bounds.AdmissionLimitBytes
				snapshot, err := reconciler.Reconcile(target)
				if err != nil {
					t.log.Error("TUN AQM reconciliation failed", "error", err.Error())
					continue
				}
				if err := t.dev.SetOutboundAdmissionLimit(target.AdmissionLimitBytes); err != nil {
					t.log.Error("engine outbound admission reconciliation failed", "error", err.Error())
					continue
				}
				if err := t.bind.ObserveTUNIngressActual(
					snapshot.Actual.RateBytesPerSecond,
					snapshot.Actual.Epoch,
					snapshot.Actual.ObservedAt,
					snapshot.Actual.Fresh,
				); err != nil {
					t.log.Error("TUN AQM readback acknowledgment failed", "error", err.Error())
				}
			}
		}
	}()
	var once sync.Once
	t.stopTUNAQM = func() { once.Do(func() { close(done) }) }
	return nil
}
