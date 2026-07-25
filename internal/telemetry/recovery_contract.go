package telemetry

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	recoveryContractMagic   = "WBRC"
	recoveryContractVersion = 1
	recoveryContractSize    = 27

	// RecoveryContractLifetime is the sole v1 validity interval carried on wire.
	RecoveryContractLifetime = 1200 * time.Millisecond
)

// ErrRecoveryContractMalformed reports a payload with the v1 magic but invalid
// v1 structure. Payloads without the magic or with another version are ignored
// for forward and legacy compatibility.
var ErrRecoveryContractMalformed = errors.New("telemetry: malformed recovery contract")

type RecoveryContractMessageType uint8

const (
	RecoveryContractOffer RecoveryContractMessageType = 1
	RecoveryContractACK   RecoveryContractMessageType = 2
)

// RecoveryContractMessage is the immutable v1 service record carried in an
// authenticated, unpadded Probe.Payload.
type RecoveryContractMessage struct {
	Type         RecoveryContractMessageType
	Enabled      bool
	ServiceBound time.Duration
	Lifetime     time.Duration
	ContractID   uint64
}

func (m RecoveryContractMessage) Valid() error {
	if m.Type != RecoveryContractOffer && m.Type != RecoveryContractACK {
		return fmt.Errorf("%w: message type %d", ErrRecoveryContractMalformed, m.Type)
	}
	if m.ContractID == 0 {
		return fmt.Errorf("%w: zero ContractID", ErrRecoveryContractMalformed)
	}
	if m.Lifetime != RecoveryContractLifetime {
		return fmt.Errorf("%w: lifetime %s", ErrRecoveryContractMalformed, m.Lifetime)
	}
	if m.Enabled {
		if m.ServiceBound <= 0 || m.ServiceBound >= 250*time.Millisecond {
			return fmt.Errorf("%w: enabled service bound %s outside (0,250ms)", ErrRecoveryContractMalformed, m.ServiceBound)
		}
	} else if m.ServiceBound != 0 {
		return fmt.Errorf("%w: disabled record carries service bound %s", ErrRecoveryContractMalformed, m.ServiceBound)
	}
	return nil
}

// EncodeRecoveryContract returns the canonical big-endian v1 payload.
func EncodeRecoveryContract(m RecoveryContractMessage) ([]byte, error) {
	if err := m.Valid(); err != nil {
		return nil, err
	}
	payload := make([]byte, recoveryContractSize)
	copy(payload[:4], recoveryContractMagic)
	payload[4] = recoveryContractVersion
	payload[5] = byte(m.Type)
	if m.Enabled {
		payload[6] = 1
	}
	binary.BigEndian.PutUint64(payload[7:15], uint64(m.ServiceBound))
	binary.BigEndian.PutUint32(payload[15:19], uint32(m.Lifetime/time.Millisecond))
	binary.BigEndian.PutUint64(payload[19:27], m.ContractID)
	return payload, nil
}

// DecodeRecoveryContract distinguishes an ignored legacy/unknown payload from a
// recognized malformed v1 record.
func DecodeRecoveryContract(payload []byte) (RecoveryContractMessage, bool, error) {
	if len(payload) < len(recoveryContractMagic) || string(payload[:4]) != recoveryContractMagic {
		return RecoveryContractMessage{}, false, nil
	}
	if len(payload) >= 5 && payload[4] != recoveryContractVersion {
		return RecoveryContractMessage{}, false, nil
	}
	if len(payload) != recoveryContractSize {
		return RecoveryContractMessage{}, true, fmt.Errorf("%w: payload length %d, want %d", ErrRecoveryContractMalformed, len(payload), recoveryContractSize)
	}
	if payload[6]&^byte(1) != 0 {
		return RecoveryContractMessage{}, true, fmt.Errorf("%w: flags %#x", ErrRecoveryContractMalformed, payload[6])
	}
	m := RecoveryContractMessage{
		Type:         RecoveryContractMessageType(payload[5]),
		Enabled:      payload[6]&1 != 0,
		ServiceBound: time.Duration(binary.BigEndian.Uint64(payload[7:15])),
		Lifetime:     time.Duration(binary.BigEndian.Uint32(payload[15:19])) * time.Millisecond,
		ContractID:   binary.BigEndian.Uint64(payload[19:27]),
	}
	if err := m.Valid(); err != nil {
		return RecoveryContractMessage{}, true, err
	}
	return m, true, nil
}
