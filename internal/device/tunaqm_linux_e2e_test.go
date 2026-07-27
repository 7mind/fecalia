//go:build e2e && linux

package device

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun"
	"golang.org/x/net/ipv4"
)

func TestLinuxTUNAQMReconciliationContract(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a temporary link")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal(err)
	}
	const name = "wbaqmtest0"
	_ = exec.Command(ip, "link", "delete", name).Run()
	if output, err := exec.Command(ip, "link", "add", name, "type", "dummy").CombinedOutput(); err != nil {
		t.Fatalf("create temporary link: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command(ip, "link", "delete", name).CombinedOutput(); err != nil {
			t.Errorf("delete temporary link: %v: %s", err, output)
		}
	})
	if output, err := exec.Command(ip, "link", "set", "dev", name, "up").CombinedOutput(); err != nil {
		t.Fatalf("bring temporary link up: %v: %s", err, output)
	}
	kernel, err := newLinuxTUNAQMKernel(name)
	if err != nil {
		t.Fatal(err)
	}
	testTUNAQMReconciliationContract(t, kernel)
}

func TestLinuxTUNAQMNativeTUNBoundedReaderStallContract(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a temporary TUN")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal(err)
	}
	const (
		name               = "wbaqmtun0"
		mtu                = 1395
		rateBytesPerSecond = 1_020_000
		dataBudgetBytes    = 51_000
	)
	tunDevice, err := tun.CreateTUN(name, mtu)
	if err != nil {
		t.Fatal(err)
	}
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range tunDevice.Events() {
		}
	}()
	t.Cleanup(func() {
		if err := tunDevice.Close(); err != nil {
			t.Errorf("close temporary TUN: %v", err)
		}
		select {
		case <-eventsDone:
		case <-time.After(time.Second):
			t.Error("temporary TUN event listener did not stop")
		}
	})
	for _, args := range [][]string{
		{"address", "add", "198.18.0.1/30", "dev", name},
		{"link", "set", "dev", name, "up"},
	} {
		if output, err := exec.Command(ip, args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	bounds, err := deriveEngineOutboundBounds(
		rateBytesPerSecond, 1, mtu, mtu, dataBudgetBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := deriveTUNAQMQueueGeometry(
		rateBytesPerSecond,
		bounds.AdmissionLimitBytes,
		1,
		tunDevice.BatchSize(),
		bounds.GSOMaxSize,
	)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := newLinuxTUNAQMKernel(name)
	if err != nil {
		t.Fatal(err)
	}
	kernel.readRingPending = func() (bool, error) {
		return tunRingPending(tunDevice.File())
	}
	target := tunAQMTargetState{
		RateBytesPerSecond:  rateBytesPerSecond,
		BurstBytes:          geometry.HTBBurstBytes,
		TxQueueLen:          geometry.RingSlots,
		MTU:                 mtu,
		QueueLimit:          geometry.FQLimit,
		GSOMaxSize:          bounds.GSOMaxSize,
		GSOMaxSegments:      bounds.GSOMaxSegments,
		AdmissionLimitBytes: bounds.AdmissionLimitBytes,
	}
	if _, err := kernel.Apply(target); err != nil {
		t.Fatal(err)
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if actual.TxQueueLen != geometry.RingSlots ||
		actual.Limit != geometry.FQLimit ||
		actual.BurstBytes != geometry.HTBBurstBytes {
		t.Fatalf(
			"native TUN queue readback ring/fq/burst = %d/%d/%d, want %d/%d/%d",
			actual.TxQueueLen,
			actual.Limit,
			actual.BurstBytes,
			geometry.RingSlots,
			geometry.FQLimit,
			geometry.HTBBurstBytes,
		)
	}
	t.Logf(
		"geometry ring=%d fq=%d htb_burst=%d maximum_service_time=%s",
		geometry.RingSlots,
		geometry.FQLimit,
		geometry.HTBBurstBytes,
		geometry.MaximumServiceTime,
	)
	driverDropsBefore := readLinkTXDrops(t, ip, name)
	qdiscDropsBefore := actual.Drops

	remote := &net.UDPAddr{IP: net.ParseIP("198.18.0.2"), Port: 9}
	buffers := make([][]byte, tunDevice.BatchSize())
	sizes := make([]int, tunDevice.BatchSize())
	for index := range buffers {
		buffers[index] = make([]byte, linuxDefaultGSOMaxSize)
	}

	boundedStallPackets := geometry.FQLimit + tunDevice.BatchSize()
	writeNativeTUNPackets(t, remote, boundedStallPackets, 1)
	time.Sleep(geometry.MaximumServiceTime)
	actual, err = kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	driverDropsAfterBatch := readLinkTXDrops(t, ip, name)
	if driverDropsAfterBatch != driverDropsBefore {
		t.Fatalf(
			"bounded B+C ownership stall plus full %d-entry native TUN batch driver drops = %d -> %d",
			tunDevice.BatchSize(),
			driverDropsBefore,
			driverDropsAfterBatch,
		)
	}
	if actual.Drops != qdiscDropsBefore {
		t.Fatalf(
			"bounded B+C ownership stall plus full native TUN batch qdisc drops = %d, want %d",
			actual.Drops,
			qdiscDropsBefore,
		)
	}
	t.Logf(
		"bounded_stall driver_drops=%d->%d qdisc_drops=%d->%d",
		driverDropsBefore,
		driverDropsAfterBatch,
		qdiscDropsBefore,
		actual.Drops,
	)
	drainNativeTUN(t, tunDevice, kernel, buffers, sizes)

	overloadTarget := target
	overloadTarget.RateBytesPerSecond = 75_000
	if _, err := kernel.Apply(overloadTarget); err != nil {
		t.Fatal(err)
	}
	actual, err = kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if actual.RateBytesPerSecond != overloadTarget.RateBytesPerSecond {
		t.Fatalf(
			"overload-phase HTB rate = %g B/s, want %g B/s",
			actual.RateBytesPerSecond,
			overloadTarget.RateBytesPerSecond,
		)
	}
	driverDropsBeforeOverload := readLinkTXDrops(t, ip, name)
	qdiscDropsBeforeOverload := actual.Drops
	directBurstSlots := geometry.RingSlots - geometry.FQLimit
	immediateOverloadPackets := directBurstSlots +
		geometry.FQLimit +
		tunDevice.BatchSize()
	writeNativeTUNPackets(t, remote, immediateOverloadPackets, 64)
	actual, err = kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	driverDropsAfterOverload := readLinkTXDrops(t, ip, name)
	if driverDropsAfterOverload != driverDropsBeforeOverload {
		t.Fatalf(
			"managed overload driver/qdisc drops = %d -> %d / %d -> %d",
			driverDropsBeforeOverload,
			driverDropsAfterOverload,
			qdiscDropsBeforeOverload,
			actual.Drops,
		)
	}
	if actual.Drops <= qdiscDropsBeforeOverload {
		rawQdisc, readbackErr := exec.Command(
			kernel.tc, "-j", "-s", "qdisc", "show", "dev", name,
		).CombinedOutput()
		t.Fatalf(
			"managed overload qdisc drops = %d, want greater than %d; detailed readback error=%v output=%s",
			actual.Drops,
			qdiscDropsBeforeOverload,
			readbackErr,
			rawQdisc,
		)
	}
	t.Logf(
		"immediate_overload driver_drops=%d->%d qdisc_drops=%d->%d",
		driverDropsBeforeOverload,
		driverDropsAfterOverload,
		qdiscDropsBeforeOverload,
		actual.Drops,
	)
	if _, err := kernel.Apply(target); err != nil {
		t.Fatal(err)
	}
	actual, err = kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if actual.RateBytesPerSecond != target.RateBytesPerSecond {
		t.Fatalf(
			"restored HTB rate = %g B/s, want %g B/s",
			actual.RateBytesPerSecond,
			target.RateBytesPerSecond,
		)
	}
	drainNativeTUN(t, tunDevice, kernel, buffers, sizes)

	driverDropsBeforeOverStall := readLinkTXDrops(t, ip, name)
	connection, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		t.Fatal(err)
	}
	overStallDeadline := time.Now().Add(2 * geometry.MaximumServiceTime)
	for time.Now().Before(overStallDeadline) {
		if _, err := connection.Write([]byte{1}); err != nil {
			_ = connection.Close()
			t.Fatal(err)
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(geometry.MaximumServiceTime)
	driverDropsAfterOverStall := readLinkTXDrops(t, ip, name)
	if driverDropsAfterOverStall <= driverDropsBeforeOverStall {
		t.Fatalf(
			"reader stall beyond %s did not expose the documented ptr-ring overflow boundary: driver drops %d -> %d",
			geometry.MaximumServiceTime,
			driverDropsBeforeOverStall,
			driverDropsAfterOverStall,
		)
	}
	actual, err = kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"over_stall driver_drops=%d->%d qdisc_drops=%d",
		driverDropsBeforeOverStall,
		driverDropsAfterOverStall,
		actual.Drops,
	)
}

func writeNativeTUNPackets(
	t testing.TB,
	remote *net.UDPAddr,
	packetCount int,
	workers int,
) {
	t.Helper()
	if packetCount <= 0 || workers <= 0 {
		t.Fatalf("packet count and workers must be positive: %d/%d", packetCount, workers)
	}
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := range workers {
		count := packetCount / workers
		if worker < packetCount%workers {
			count++
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			connection, err := net.ListenUDP("udp4", nil)
			if err != nil {
				errs <- err
				return
			}
			packetConnection := ipv4.NewPacketConn(connection)
			defer func() {
				if err := packetConnection.Close(); err != nil {
					errs <- err
				}
			}()
			<-start
			if err := connection.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
				errs <- err
				return
			}
			const maximumSendBatch = 1_024
			payload := []byte{1}
			messages := make([]ipv4.Message, count)
			for index := range messages {
				messages[index] = ipv4.Message{
					Buffers: [][]byte{payload},
					Addr:    remote,
				}
			}
			for offset := 0; offset < len(messages); {
				end := offset + maximumSendBatch
				if end > len(messages) {
					end = len(messages)
				}
				written, err := packetConnection.WriteBatch(messages[offset:end], 0)
				if err != nil {
					errs <- err
					return
				}
				offset += written
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func drainNativeTUN(
	t testing.TB,
	tunDevice tun.Device,
	kernel *linuxTUNAQMKernel,
	buffers [][]byte,
	sizes []int,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last tunAQMActualState
	lastPending := false
	readPackets := 0
	for time.Now().Before(deadline) {
		pending, err := tunRingPending(tunDevice.File())
		if err != nil {
			t.Fatal(err)
		}
		lastPending = pending
		if !pending {
			actual, err := kernel.Read()
			if err != nil {
				t.Fatal(err)
			}
			last = actual
			if actual.QueueLength == 0 && actual.BacklogBytes == 0 {
				if err := tunDevice.File().SetReadDeadline(time.Time{}); err != nil {
					t.Fatal(err)
				}
				return
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if err := tunDevice.File().SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		count, err := tunDevice.Read(buffers, sizes, 0)
		if err != nil {
			t.Fatal(err)
		}
		readPackets += count
	}
	t.Fatalf(
		"native TUN did not drain within 5s: read=%d ring_pending=%t qlen=%d backlog=%d drops=%d",
		readPackets,
		lastPending,
		last.QueueLength,
		last.BacklogBytes,
		last.Drops,
	)
}

func readLinkTXDrops(t testing.TB, ip, name string) uint64 {
	t.Helper()
	raw, err := exec.Command(ip, "-j", "-s", "link", "show", "dev", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	var links []struct {
		Stats struct {
			TX struct {
				Dropped uint64 `json:"dropped"`
			} `json:"tx"`
		} `json:"stats64"`
	}
	if err := json.Unmarshal(raw, &links); err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("link readback returned %d entries, want 1", len(links))
	}
	return links[0].Stats.TX.Dropped
}
