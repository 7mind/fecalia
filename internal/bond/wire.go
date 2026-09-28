package bond

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/7mind/wanbond/internal/frame"
)

const (
	DataType    = 0xa1
	ACKType     = 0xa2
	Version     = 1
	headerBytes = 19
	ackBytes    = 96
	// ExtraOverhead is the difference from a legacy DATA frame, including the MAC.
	ExtraOverhead         = 61
	interactiveBit uint64 = 1 << 63
)

type Epoch struct {
	Boot       uint64
	Generation uint64
}

type PathID uint16

type packetHeader struct {
	epoch Epoch
	lane  PathID
}

func appendHeader(h packetHeader) []byte {
	b := make([]byte, headerBytes)
	b[0] = Version
	binary.BigEndian.PutUint64(b[1:9], h.epoch.Boot)
	binary.BigEndian.PutUint64(b[9:17], h.epoch.Generation)
	binary.BigEndian.PutUint16(b[17:19], uint16(h.lane))
	return b
}

func parseHeader(b []byte) (packetHeader, error) {
	if len(b) < headerBytes || b[0] != Version {
		return packetHeader{}, errors.New("bond: invalid version or truncated header")
	}
	h := packetHeader{Epoch{binary.BigEndian.Uint64(b[1:9]), binary.BigEndian.Uint64(b[9:17])}, PathID(binary.BigEndian.Uint16(b[17:19]))}
	if h.epoch.Boot == 0 || h.epoch.Generation == 0 {
		return packetHeader{}, errors.New("bond: zero epoch")
	}
	return h, nil
}

func dataFrame(epoch, destination Epoch, lane PathID, attempt, seq, order uint64, payload []byte) frame.Control {
	b := binary.BigEndian.AppendUint64(appendHeader(packetHeader{epoch, lane}), destination.Boot)
	b = binary.BigEndian.AppendUint64(b, destination.Generation)
	b = binary.BigEndian.AppendUint64(b, seq)
	b = binary.BigEndian.AppendUint64(b, order)
	b = append(b, payload...)
	return frame.Control{ControlType: DataType, Seq: attempt, Payload: b}
}

type acknowledgement struct {
	observed     Epoch
	high         uint64
	mask         uint64
	bytes        uint64
	elapsed      uint64
	delay        uint64
	receivedHigh uint64
	receivedMask [4]uint64
}

func (a acknowledgement) received(seq uint64) bool {
	if seq == 0 || seq > a.receivedHigh || a.receivedHigh-seq >= uint64(len(a.receivedMask)*64) {
		return false
	}
	delta := a.receivedHigh - seq
	return a.receivedMask[delta/64]&(uint64(1)<<(delta%64)) != 0
}

func ackFrame(epoch Epoch, lane PathID, revision uint64, a acknowledgement) frame.Control {
	b := appendHeader(packetHeader{epoch, lane})
	for _, value := range []uint64{a.observed.Boot, a.observed.Generation, a.high, a.mask, a.bytes, a.elapsed, a.delay} {
		b = binary.BigEndian.AppendUint64(b, value)
	}
	b = binary.BigEndian.AppendUint64(b, a.receivedHigh)
	for _, mask := range a.receivedMask {
		b = binary.BigEndian.AppendUint64(b, mask)
	}
	return frame.Control{ControlType: ACKType, Seq: revision, Payload: b}
}

func parseACK(b []byte) (acknowledgement, error) {
	if len(b) != headerBytes+ackBytes {
		return acknowledgement{}, errors.New("bond: invalid ACK size")
	}
	b = b[headerBytes:]
	a := acknowledgement{observed: Epoch{binary.BigEndian.Uint64(b), binary.BigEndian.Uint64(b[8:])}, high: binary.BigEndian.Uint64(b[16:]), mask: binary.BigEndian.Uint64(b[24:]), bytes: binary.BigEndian.Uint64(b[32:]), elapsed: binary.BigEndian.Uint64(b[40:]), delay: binary.BigEndian.Uint64(b[48:]), receivedHigh: binary.BigEndian.Uint64(b[56:])}
	if a.elapsed > math.MaxInt64 || a.delay > math.MaxInt64 {
		return acknowledgement{}, errors.New("bond: ACK duration exceeds monotonic clock range")
	}
	for i := range a.receivedMask {
		a.receivedMask[i] = binary.BigEndian.Uint64(b[64+i*8:])
	}
	return a, nil
}

// Hello is carried inside the existing challenge-protected probe exchange.
func Hello(epoch Epoch, path uint8) []byte {
	b := []byte{'b', 'o', 'n', 'd', Version, path}
	b = binary.BigEndian.AppendUint64(b, epoch.Boot)
	return binary.BigEndian.AppendUint64(b, epoch.Generation)
}

func ParseHello(b []byte) (Epoch, uint8, bool) {
	if len(b) != 22 || string(b[:4]) != "bond" || b[4] != Version {
		return Epoch{}, 0, false
	}
	epoch := Epoch{binary.BigEndian.Uint64(b[6:14]), binary.BigEndian.Uint64(b[14:22])}
	return epoch, b[5], epoch.Boot != 0 && epoch.Generation != 0
}
