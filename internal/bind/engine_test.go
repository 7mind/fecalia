package bind

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/frame"
)

// randKeyHex returns 32 random bytes as lowercase hex, a valid UAPI key field.
// A curve25519 point need not be derived: the engine clamps the private key and
// treats any 32-byte peer public key as a valid DH input, which is all a
// handshake INITIATION (the transmit we assert) requires.
func randKeyHex(t *testing.T) string {
	t.Helper()
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	return hex.EncodeToString(k[:])
}

// ipv4Packet builds a minimal well-formed IPv4 header (no payload) from src to
// dst so the engine routes it to the configured peer (allowed_ip 0.0.0.0/0) and,
// finding no session, transmits a handshake initiation.
func ipv4Packet(src, dst [4]byte) []byte {
	p := make([]byte, 20)
	p[0] = 0x45 // version 4, IHL 5 (20-byte header)
	binary.BigEndian.PutUint16(p[2:4], 20)
	p[8] = 64 // TTL
	p[9] = 17 // protocol: UDP (irrelevant to routing)
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	return p
}

// TestMultipathEngineUpCanTransmit is the unprivileged engine-integration test
// for the bind-lifecycle defect. It drives a real amneziawg-go Device over the
// Multipath Bind using a channel-based TUN (no root, no real interface) and
// asserts that AFTER device.Up() the bind can actually transmit: an outbound
// packet fed into the TUN triggers a handshake initiation, which must arrive at
// the peer endpoint wrapped in an outer DATA frame.
//
// This FAILS on the pre-fix code: device.Up() runs BindUpdate → Close (pre-open,
// sets the sticky closed flag) → Open (never resets it), so every Send returns
// net.ErrClosed and NOTHING is transmitted — the read below times out. It PASSES
// once Close clears state and Open rebuilds it. The defect class caught here is
// exactly "Send returns ErrClosed after Up".
func TestMultipathEngineUpCanTransmit(t *testing.T) {
	// D20 leak gate: assert no goroutine outlives the test. Registered first so it
	// runs LAST — after close(done), dev.Close, and peer.Close below — observing a
	// fully quiesced package. Against the pre-fix bare `ctun.Outbound <-` send this
	// FAILS, flagging the producer goroutine parked on the channel send; it passes
	// once the send gains a done-channel escape.
	defer goleak.VerifyNone(t)

	psk := testKey(t, 0x7E) // outer DATA-frame PSK

	// Stand-in remote (the concentrator's listen socket): the engine sends its
	// handshake initiation here, framed by the Multipath's outer codec.
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	peerAP := peer.LocalAddr().(*net.UDPAddr).AddrPort()

	m, err := newMultipath(t, loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}

	ctun := tuntest.NewChannelTUN()
	silent := &awgdevice.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(string, ...any) {},
	}
	dev := awgdevice.NewDevice(ctun.TUN(), m, silent)
	// defer (not t.Cleanup) so device shutdown is ordered BEFORE goleak.VerifyNone:
	// t.Cleanup callbacks run after all deferred calls, which would make the leak
	// check observe the device's own live goroutines.
	defer dev.Close()

	// Minimal edge UAPI: our private key, one peer with the stand-in endpoint and a
	// catch-all allowed-ip. persistent_keepalive nudges the engine to transmit
	// promptly; the injected packet below is the deterministic trigger.
	uapi := fmt.Sprintf(
		"private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=1\n",
		randKeyHex(t), randKeyHex(t), peerAP.String(),
	)
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet: %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("device.Up: %v", err)
	}

	// Feed an outbound packet so the engine routes it to the peer and initiates a
	// handshake (→ Multipath.Send → the peer socket below). The send races the
	// device shutdown: tuntest's Outbound is unbuffered and "blocks forever on TUN
	// close", so a bare `Outbound <-` would park the producer forever once the read
	// loop is gone (the D20 leak, asserted absent by goleak.VerifyNone above). The
	// done escape lets the producer exit when the test tears the device down.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case ctun.Outbound <- ipv4Packet([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}):
		case <-done:
		}
	}()

	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	n, _, err := peer.ReadFromUDPAddrPort(buf)
	if err != nil {
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			t.Fatalf("no datagram transmitted within deadline after device.Up(): the bind-lifecycle defect makes Send return net.ErrClosed: %v", err)
		}
		t.Fatalf("peer read: %v", err)
	}

	// What arrived must be our outer DATA frame carrying the WG handshake.
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	fr, err := codec.Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode transmitted datagram as outer frame: %v", err)
	}
	if _, ok := fr.(frame.Data); !ok {
		t.Fatalf("transmitted frame is %T, want frame.Data (a wrapped WG handshake)", fr)
	}
}
