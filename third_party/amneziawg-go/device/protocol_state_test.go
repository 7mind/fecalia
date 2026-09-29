package device

import (
	"sync"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn/bindtest"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/tuntest"
)

func newProtocolStateTestDevice(t *testing.T) *Device {
	t.Helper()
	binds := bindtest.NewChannelBinds()
	device := NewDevice(tuntest.NewChannelTUN().TUN(), binds[0], NewLogger(LogLevelError, ""))
	t.Cleanup(device.Close)
	return device
}

func configureProtocolState(t *testing.T, device *Device, config string) {
	t.Helper()
	if err := device.IpcSet(config); err != nil {
		t.Fatalf("IpcSet: %v", err)
	}
}

// Amnezia message headers and paddings were package globals in v1.0.4
// (upstream #155); concurrent devices must keep independent wire settings.
func TestProtocolStateIsPerDevice(t *testing.T) {
	first := newProtocolStateTestDevice(t)
	second := newProtocolStateTestDevice(t)
	configureProtocolState(t, first, "jc=3\njmin=40\njmax=70\ns1=15\ns2=18\ns3=20\ns4=25\nh1=101\nh2=102\nh3=103\nh4=104\n")
	configureProtocolState(t, second, "jc=4\njmin=50\njmax=80\ns1=16\ns2=19\ns3=21\ns4=26\nh1=201\nh2=202\nh3=203\nh4=204\n")
	configureProtocolState(t, second, "jc=0\njmin=0\njmax=0\ns1=0\ns2=0\ns3=0\ns4=0\nh1=1\nh2=2\nh3=3\nh4=4\n")

	if got := first.headers.init.Load(); !got.Contains(101) || got.Contains(1) {
		t.Fatalf("first init header changed after second reset: %+v", got)
	}
	if got := first.headers.transport.Load(); !got.Contains(104) || got.Contains(4) {
		t.Fatalf("first transport header changed after second reset: %+v", got)
	}
	if first.paddings.init.Load() != 15 || first.paddings.transport.Load() != 25 || first.junk.count.Load() != 3 {
		t.Fatal("first paddings or junk changed after second reset")
	}
	if got := second.headers.init.Load(); !got.Contains(MessageInitiationType) {
		t.Fatalf("second init header did not reset to default: %+v", got)
	}
	if second.paddings.init.Load() != 0 || second.paddings.transport.Load() != 0 || second.junk.count.Load() != 0 {
		t.Fatal("second paddings or junk did not reset")
	}
}

func TestJunkPacketsConcurrentUse(t *testing.T) {
	device := newProtocolStateTestDevice(t)
	configureProtocolState(t, device, "jc=4\njmin=40\njmax=90\n")
	const (
		workers    = 8
		iterations = 100
	)
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(workers)
	for range workers {
		go func() {
			defer done.Done()
			<-start
			for range iterations {
				for _, junk := range device.JunkPackets() {
					if len(junk) < 40 || len(junk) >= 90 {
						t.Errorf("junk packet length %d outside [40, 90)", len(junk))
						return
					}
				}
			}
		}()
	}
	close(start)
	done.Wait()
}
