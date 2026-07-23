package device

import (
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/device/awg"
)

func newProtocolStateTestDevice() *Device {
	device := &Device{
		log:      NewLogger(LogLevelError, ""),
		protocol: defaultProtocolState(),
	}
	device.cookieChecker.SetMessageCookieReplyType(device.protocol.messageCookieReplyType)
	return device
}

func configureProtocolState(t *testing.T, device *Device, base uint32) {
	t.Helper()
	var protocol awg.Protocol
	protocol.ASecCfg.IsSet = true
	protocol.ASecCfg.InitPacketJunkSize = 15
	protocol.ASecCfg.ResponsePacketJunkSize = 92
	protocol.ASecCfg.InitPacketMagicHeader = base + 1
	protocol.ASecCfg.ResponsePacketMagicHeader = base + 2
	protocol.ASecCfg.UnderloadPacketMagicHeader = base + 3
	protocol.ASecCfg.TransportPacketMagicHeader = base + 4
	if err := device.handlePostConfig(&protocol); err != nil {
		t.Fatalf("handlePostConfig: %v", err)
	}
}

func TestProtocolStateIsPerDevice(t *testing.T) {
	first := newProtocolStateTestDevice()
	second := newProtocolStateTestDevice()
	configureProtocolState(t, first, 100)
	configureProtocolState(t, second, 200)

	second.resetProtocol()

	first.awg.ASecMux.RLock()
	defer first.awg.ASecMux.RUnlock()
	if first.protocol.messageInitiationType != 101 || first.protocol.messageTransportType != 104 {
		t.Fatalf("first protocol changed after second reset: %+v", first.protocol)
	}
	if got := first.protocol.packetSizeToMsgType[MessageInitiationSize+15]; got != 101 {
		t.Fatalf("first initiation size maps to %d, want 101", got)
	}

	second.awg.ASecMux.RLock()
	defer second.awg.ASecMux.RUnlock()
	if second.protocol.messageInitiationType != MessageInitiationType || second.protocol.messageTransportType != MessageTransportType {
		t.Fatalf("second protocol did not reset to defaults: %+v", second.protocol)
	}
}
