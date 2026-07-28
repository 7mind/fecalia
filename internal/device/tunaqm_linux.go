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

var (
	tunAQMRatePattern = regexp.MustCompile(
		`(?m)^class htb 1:1 .* rate ([0-9]+(?:\.[0-9]+)?)([KMG]?)bit(?: |$)`,
	)
	tunAQMBurstPattern = regexp.MustCompile(
		`(?m)^class htb 1:1 .* burst ([0-9]+(?:\.[0-9]+)?)([KMG]?)b(?: |$)`,
	)
)

type linuxTUNAQMKernel struct {
	name            string
	tc              string
	command         func(args ...string) ([]byte, error)
	readTxQueueLen  func() (int, error)
	writeTxQueueLen func(int) error
	readRingPending func() (bool, error)
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
	kernel.readRingPending = func() (bool, error) {
		return false, nil
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

func (k *linuxTUNAQMKernel) Apply(target tunAQMTargetState) (tunAQMApplyResult, error) {
	var result tunAQMApplyResult
	current, readErr := k.Read()
	topologyReady := readErr == nil &&
		current.RootKind == "htb"
	liveLeaf := topologyReady && current.LeafKind == "bfifo"
	queueOccupied := current.QueueLength != 0 || current.BacklogBytes != 0
	gsoLimitsChanging := current.GSOMaxSize != target.GSOMaxSize ||
		current.GSOMaxSegments != target.GSOMaxSegments
	gsoSizeShrinking := target.GSOMaxSize < current.GSOMaxSize
	gsoShrinkDeferred := gsoLimitsChanging && gsoSizeShrinking &&
		queueOccupied
	result.GSOLimitsDeferred = gsoShrinkDeferred
	postGSOWriteOccupied := false
	if gsoLimitsChanging && gsoSizeShrinking && !gsoShrinkDeferred {
		if err := k.writeGSOLimits(linkGSOLimits{
			MaxSize:     uint32(target.GSOMaxSize),
			MaxSegments: uint32(target.GSOMaxSegments),
		}); err != nil {
			return result, err
		}
		postGSO, err := k.Read()
		if err != nil {
			return result, fmt.Errorf(
				"device: read TUN AQM after GSO shrink: %w",
				err,
			)
		}
		if postGSO.GSOMaxSize != target.GSOMaxSize ||
			postGSO.GSOMaxSegments != target.GSOMaxSegments {
			return result, errors.New(
				"device: GSO shrink readback does not match target",
			)
		}
		current = postGSO
		topologyReady = current.RootKind == "htb"
		liveLeaf = topologyReady && current.LeafKind == "bfifo"
		queueOccupied = current.QueueLength != 0 ||
			current.BacklogBytes != 0
		postGSOWriteOccupied = queueOccupied
	}

	queueLimit := target.QueueLimitBytes
	if liveLeaf &&
		target.QueueLimitBytes < current.LimitBytes &&
		(current.BacklogBytes > target.QueueLimitBytes ||
			postGSOWriteOccupied) {
		queueLimit = current.LimitBytes
		result.QueueLimitDeferred = true
	}
	burstBytes := target.BurstBytes
	if postGSOWriteOccupied && burstBytes < current.BurstBytes {
		burstBytes = current.BurstBytes
		result.QueueLimitDeferred = true
	}
	if gsoShrinkDeferred {
		if queueLimit < current.LimitBytes {
			queueLimit = current.LimitBytes
			result.QueueLimitDeferred = true
		}
		installedGSOBurst, err := exactTCHTBBurstBytes(current.GSOMaxSize)
		if err != nil {
			return result, err
		}
		if burstBytes < installedGSOBurst {
			burstBytes = installedGSOBurst
		}
	}
	leafReady := liveLeaf && current.LimitBytes == queueLimit
	if !topologyReady {
		if readErr == nil &&
			queueOccupied {
			return result, errors.New(
				"device: refuse to replace live TUN qdisc topology with backlog",
			)
		}
		if err := k.run(
			"qdisc", "replace", "dev", k.name,
			"root", "handle", "1:", "htb", "default", "1",
		); err != nil {
			if readErr != nil {
				return result, fmt.Errorf("device: restore TUN AQM after readback failure: %w",
					errors.Join(readErr, err))
			}
			return result, err
		}
	}

	if !topologyReady ||
		current.BurstBytes != burstBytes ||
		math.Abs(current.RateBytesPerSecond-target.RateBytesPerSecond) >
			target.RateBytesPerSecond*0.01 {
		rateBits := strconv.FormatInt(int64(math.Round(target.RateBytesPerSecond*8)), 10) + "bit"
		burstText := strconv.Itoa(burstBytes) + "b"
		if err := k.run(
			"class", "replace", "dev", k.name,
			"parent", "1:", "classid", "1:1", "htb",
			"rate", rateBits, "ceil", rateBits,
			"burst", burstText, "cburst", burstText,
		); err != nil {
			return result, err
		}
	}

	if !leafReady {
		action := "add"
		if liveLeaf {
			action = "change"
		} else if topologyReady && current.LeafKind != "" {
			if current.QueueLength != 0 || current.BacklogBytes != 0 {
				return result, errors.New(
					"device: refuse to replace live TUN leaf qdisc with backlog",
				)
			}
			if err := k.run(
				"qdisc", "delete", "dev", k.name, "parent", "1:1",
			); err != nil {
				return result, err
			}
		}
		if err := k.run(
			"qdisc", action, "dev", k.name,
			"parent", "1:1", "handle", "10:", "bfifo",
			"limit", strconv.Itoa(queueLimit),
		); err != nil {
			return result, err
		}
	}
	installedRing, err := k.readTxQueueLen()
	if err != nil {
		return result, err
	}
	if installedRing < target.TxQueueLen {
		if err := k.writeTxQueueLen(target.TxQueueLen); err != nil {
			return result, err
		}
	}
	if current.GSOMaxSize != target.GSOMaxSize ||
		current.GSOMaxSegments != target.GSOMaxSegments {
		if !gsoShrinkDeferred {
			if err := k.writeGSOLimits(linkGSOLimits{
				MaxSize:     uint32(target.GSOMaxSize),
				MaxSegments: uint32(target.GSOMaxSegments),
			}); err != nil {
				return result, err
			}
		}
	}
	result.AppliedBurstBytes = burstBytes
	return result, nil
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
	ringPending := false
	if k.readRingPending != nil {
		ringPending, err = k.readRingPending()
		if err != nil {
			return tunAQMActualState{}, err
		}
	}
	rawQdisc, err := k.output("-j", "-s", "qdisc", "show", "dev", k.name)
	if err != nil {
		return tunAQMActualState{}, err
	}
	var qdiscs []struct {
		Kind    string `json:"kind"`
		Root    bool   `json:"root"`
		Parent  string `json:"parent"`
		Drops   uint64 `json:"drops"`
		Backlog int    `json:"backlog"`
		QLen    int    `json:"qlen"`
		Options struct {
			LimitBytes int `json:"limit"`
		} `json:"options"`
	}
	if err := json.Unmarshal(rawQdisc, &qdiscs); err != nil {
		return tunAQMActualState{}, fmt.Errorf("device: decode tc qdisc readback: %w", err)
	}
	rootKind := ""
	leafKind := ""
	var drops uint64
	var backlog int
	var queueLength int
	var leafLimitBytes int
	for _, qdisc := range qdiscs {
		if qdisc.Drops > drops {
			drops = qdisc.Drops
		}
		if qdisc.QLen > queueLength {
			queueLength = qdisc.QLen
		}
		if qdisc.Backlog > backlog {
			backlog = qdisc.Backlog
		}
		if qdisc.Root {
			rootKind = qdisc.Kind
		}
		if qdisc.Parent == "1:1" {
			leafKind = qdisc.Kind
			leafLimitBytes = qdisc.Options.LimitBytes
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
	burstBytes, err := parseTUNAQMBurst(string(rawClass))
	if err != nil {
		return tunAQMActualState{}, err
	}
	return tunAQMActualState{
		RateBytesPerSecond: rate,
		BurstBytes:         burstBytes,
		TxQueueLen:         txQueueLen,
		RootKind:           rootKind,
		LeafKind:           leafKind,
		LimitBytes:         leafLimitBytes,
		GSOMaxSize:         int(gsoLimits.MaxSize),
		GSOMaxSegments:     int(gsoLimits.MaxSegments),
		QueueLength:        queueLength,
		BacklogBytes:       backlog,
		RingPending:        ringPending,
		Drops:              drops,
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

func parseTUNAQMBurst(output string) (int, error) {
	match := tunAQMBurstPattern.FindStringSubmatch(output)
	if match == nil {
		return 0, errors.New("device: tc class readback has no htb 1:1 burst")
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, fmt.Errorf("device: parse tc class burst: %w", err)
	}
	switch match[2] {
	case "":
	case "K":
		value *= 1 << 10
	case "M":
		value *= 1 << 20
	case "G":
		value *= 1 << 30
	default:
		return 0, fmt.Errorf("device: unsupported tc class burst unit %q", match[2])
	}
	if value > float64(int(^uint(0)>>1)) {
		return 0, errors.New("device: tc class burst overflows int")
	}
	return int(math.Round(value)), nil
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

func tunRingPending(file *os.File) (bool, error) {
	if file == nil {
		return false, errors.New("TUN file is required for ptr-ring readback")
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return false, fmt.Errorf("access TUN file descriptor: %w", err)
	}
	pending := false
	var pollErr error
	if err := raw.Control(func(fd uintptr) {
		pollFDs := []unix.PollFd{{
			Fd:     int32(fd),
			Events: unix.POLLIN,
		}}
		_, pollErr = unix.Poll(pollFDs, 0)
		if pollErr == nil {
			revents := pollFDs[0].Revents
			if revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				pollErr = fmt.Errorf(
					"TUN ptr-ring poll returned revents %#x",
					revents,
				)
			} else {
				pending = revents&unix.POLLIN != 0
			}
		}
	}); err != nil {
		return false, fmt.Errorf("poll TUN file descriptor: %w", err)
	}
	if pollErr != nil {
		return false, fmt.Errorf("read TUN ptr-ring state: %w", pollErr)
	}
	return pending, nil
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
	kernel.readRingPending = func() (bool, error) {
		return tunRingPending(t.tun.File())
	}
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		return err
	}
	initial, err := congestion.ConservativeSeed(
		t.cfg.Scheduler.PerPathShapers[0].RateBytesPerSecond,
		t.cfg.Scheduler.PerPathShapers[0].RateLimitBytesPerSecond,
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
	queueGeometry, err := deriveTUNAQMQueueGeometry(
		initial.IngressRateBytesPerSecond*float64(peerCount),
		bounds.AdmissionLimitBytes,
		peerCount,
		bounds.GSOMaxSize,
		bounds.MaxBatchServiceTime,
		mtu,
		maximumMTU,
	)
	if err != nil {
		return err
	}
	target := tunAQMTargetState{
		RateBytesPerSecond:  initial.IngressRateBytesPerSecond * float64(peerCount),
		BurstBytes:          queueGeometry.HTBBurstBytes,
		TxQueueLen:          queueGeometry.RingSlots,
		MTU:                 mtu,
		QueueLimitBytes:     queueGeometry.LeafLimitBytes,
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
	transition, err := newTUNAQMTransition(
		reconciler,
		t.dev.TrySetOutboundAdmissionLimit,
		t.dev.OutboundAdmissionLimit,
	)
	if err != nil {
		return fmt.Errorf("device: install TUN AQM transition: %w", err)
	}
	if err := t.bind.ObserveTUNIngressActual(
		initialSnapshot.Actual.RateBytesPerSecond,
		initialSnapshot.Actual.Epoch,
		initialSnapshot.Actual.ObservedAt,
		initialSnapshot.RateFresh,
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
	initialOutboundStats := t.dev.OutboundStats()
	pressureSampler := tunIngressPressureSampler{
		previous: tunIngressPressureCounters{
			ObservedAt:               time.Now(),
			TUNBytes:                 initialOutboundStats.TUNBytes,
			AdmissionWaitNanoseconds: initialOutboundStats.AdmissionWaitNanoseconds,
		},
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(telemetry.DefaultProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				outboundStats := t.dev.OutboundStats()
				currentPressureCounters := tunIngressPressureCounters{
					ObservedAt:               time.Now(),
					TUNBytes:                 outboundStats.TUNBytes,
					AdmissionWaitNanoseconds: outboundStats.AdmissionWaitNanoseconds,
				}
				pressureDelta, pressureErr := pressureSampler.Sample(
					currentPressureCounters,
				)
				if pressureErr != nil {
					t.log.Error(
						"TUN ingress pressure derivation failed",
						"error",
						pressureErr.Error(),
					)
				}
				rate, epoch, dataBurstBytes, ok := t.bind.TUNIngressTarget()
				if ok {
					target.RateBytesPerSecond = rate
					target.Epoch = epoch
					maxDataBurstBytes = dataBurstBytes
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
				queueGeometry, err := deriveTUNAQMQueueGeometry(
					target.RateBytesPerSecond,
					bounds.AdmissionLimitBytes,
					peerCount,
					bounds.GSOMaxSize,
					bounds.MaxBatchServiceTime,
					target.MTU,
					maximumMTU,
				)
				if err != nil {
					t.log.Error("TUN AQM queue-geometry derivation failed", "error", err.Error())
					continue
				}
				target.BurstBytes = queueGeometry.HTBBurstBytes
				target.TxQueueLen = queueGeometry.RingSlots
				target.QueueLimitBytes = queueGeometry.LeafLimitBytes
				target.GSOMaxSize = bounds.GSOMaxSize
				target.GSOMaxSegments = bounds.GSOMaxSegments
				target.AdmissionLimitBytes = bounds.AdmissionLimitBytes
				snapshot, err := transition.Reconcile(target)
				if err != nil {
					t.log.Error("TUN AQM reconciliation failed", "error", err.Error())
					continue
				}
				if err := t.bind.ObserveTUNIngressActual(
					snapshot.Actual.RateBytesPerSecond,
					snapshot.Actual.Epoch,
					snapshot.Actual.ObservedAt,
					snapshot.RateFresh,
				); err != nil {
					t.log.Error("TUN AQM readback acknowledgment failed", "error", err.Error())
					continue
				}
				if pressureErr != nil || !ok {
					continue
				}
				if err := t.bind.ObserveTUNIngressPressure(
					pressureDelta.AdmissionWaitDuration,
					pressureDelta.TUNBytes,
					pressureDelta.Interval,
					snapshot.Actual.RingPending,
					target.Epoch,
					currentPressureCounters.ObservedAt,
				); err != nil {
					t.log.Error(
						"TUN ingress pressure observation failed",
						"error",
						err.Error(),
					)
				}
			}
		}
	}()
	var once sync.Once
	t.stopTUNAQM = func() { once.Do(func() { close(done) }) }
	return nil
}
