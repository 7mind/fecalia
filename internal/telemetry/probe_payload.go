package telemetry

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	probePayloadMagic       = "WBPP"
	probePayloadVersion     = 1
	probePayloadHeaderSize  = 7
	dataLossFeedbackSize    = 49
	probePayloadRecoveryBit = 1 << 0
	probePayloadDataLossBit = 1 << 1
)

var ErrProbePayloadMalformed = errors.New("telemetry: malformed probe payload")

// DataLossFeedback reports one bounded interval of final receiver DATA outcomes.
// ObservedSessionID and ContractID bind the report to the exact remote sender
// service; CarrierGeneration and ReportID prevent a delayed path epoch or report
// from becoming current again.
type DataLossFeedback struct {
	ObservedSessionID uint64
	ContractID        uint64
	CarrierPathID     uint8
	CarrierGeneration uint64
	ReportID          uint64
	Received          uint64
	Lost              uint64
}

func (f DataLossFeedback) Valid() error {
	if f.ObservedSessionID == 0 || f.ContractID == 0 ||
		f.CarrierGeneration == 0 || f.ReportID == 0 {
		return fmt.Errorf("%w: zero identity field", ErrProbePayloadMalformed)
	}
	if f.Received == 0 && f.Lost == 0 {
		return fmt.Errorf("%w: empty DATA-loss interval", ErrProbePayloadMalformed)
	}
	if f.Received > ^uint64(0)-f.Lost {
		return fmt.Errorf("%w: DATA-loss interval count overflow", ErrProbePayloadMalformed)
	}
	return nil
}

func (f DataLossFeedback) Loss() float64 {
	return float64(f.Lost) / float64(f.Received+f.Lost)
}

// EncodeProbePayload preserves the legacy recovery-only payload byte-for-byte.
// DATA-loss feedback uses a separate feedback-only envelope so recovery OFFERs
// and ACKs remain directly parseable by peers that predate the envelope.
func EncodeProbePayload(recovery []byte, feedback *DataLossFeedback) ([]byte, error) {
	if feedback == nil {
		return append([]byte(nil), recovery...), nil
	}
	if len(recovery) != 0 {
		return nil, fmt.Errorf("%w: recovery and DATA-loss feedback require separate probes", ErrProbePayloadMalformed)
	}
	if err := feedback.Valid(); err != nil {
		return nil, err
	}
	flags := byte(probePayloadDataLossBit)
	payload := make([]byte, probePayloadHeaderSize+dataLossFeedbackSize)
	copy(payload[:4], probePayloadMagic)
	payload[4] = probePayloadVersion
	payload[5] = flags
	off := probePayloadHeaderSize
	binary.BigEndian.PutUint64(payload[off:off+8], feedback.ObservedSessionID)
	binary.BigEndian.PutUint64(payload[off+8:off+16], feedback.ContractID)
	payload[off+16] = feedback.CarrierPathID
	binary.BigEndian.PutUint64(payload[off+17:off+25], feedback.CarrierGeneration)
	binary.BigEndian.PutUint64(payload[off+25:off+33], feedback.ReportID)
	binary.BigEndian.PutUint64(payload[off+33:off+41], feedback.Received)
	binary.BigEndian.PutUint64(payload[off+41:off+49], feedback.Lost)
	return payload, nil
}

// DecodeProbePayload recognizes the versioned envelope. Legacy/unknown payloads
// remain unrecognized so callers can retain their prior reflection behavior.
func DecodeProbePayload(payload []byte) (recovery []byte, feedback *DataLossFeedback, recognized bool, err error) {
	if len(payload) < len(probePayloadMagic) || string(payload[:4]) != probePayloadMagic {
		return nil, nil, false, nil
	}
	if len(payload) < probePayloadHeaderSize {
		return nil, nil, true, fmt.Errorf("%w: header length %d", ErrProbePayloadMalformed, len(payload))
	}
	if payload[4] != probePayloadVersion {
		return nil, nil, false, nil
	}
	flags := payload[5]
	if flags&^(probePayloadRecoveryBit|probePayloadDataLossBit) != 0 {
		return nil, nil, true, fmt.Errorf("%w: flags %#x", ErrProbePayloadMalformed, flags)
	}
	recoveryLen := int(payload[6])
	if flags&probePayloadRecoveryBit == 0 && recoveryLen != 0 {
		return nil, nil, true, fmt.Errorf("%w: recovery length without flag", ErrProbePayloadMalformed)
	}
	want := probePayloadHeaderSize + recoveryLen
	if flags&probePayloadDataLossBit != 0 {
		want += dataLossFeedbackSize
	}
	if len(payload) != want {
		return nil, nil, true, fmt.Errorf("%w: payload length %d, want %d", ErrProbePayloadMalformed, len(payload), want)
	}
	recovery = append([]byte(nil), payload[probePayloadHeaderSize:probePayloadHeaderSize+recoveryLen]...)
	if flags&probePayloadDataLossBit == 0 {
		return recovery, nil, true, nil
	}
	off := probePayloadHeaderSize + recoveryLen
	report := &DataLossFeedback{
		ObservedSessionID: binary.BigEndian.Uint64(payload[off : off+8]),
		ContractID:        binary.BigEndian.Uint64(payload[off+8 : off+16]),
		CarrierPathID:     payload[off+16],
		CarrierGeneration: binary.BigEndian.Uint64(payload[off+17 : off+25]),
		ReportID:          binary.BigEndian.Uint64(payload[off+25 : off+33]),
		Received:          binary.BigEndian.Uint64(payload[off+33 : off+41]),
		Lost:              binary.BigEndian.Uint64(payload[off+41 : off+49]),
	}
	if err := report.Valid(); err != nil {
		return nil, nil, true, err
	}
	return recovery, report, true, nil
}
