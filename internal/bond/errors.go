package bond

import (
	"fmt"
	"time"
)

type RejectionCause uint8

const (
	RejectMalformed RejectionCause = iota
	RejectEpoch
	RejectPath
	RejectLane
	RejectACK
	RejectType
	RejectTime
	RejectionCauses
)

func (c RejectionCause) String() string {
	switch c {
	case RejectMalformed:
		return "malformed"
	case RejectEpoch:
		return "epoch"
	case RejectPath:
		return "path"
	case RejectLane:
		return "lane"
	case RejectACK:
		return "ack"
	case RejectType:
		return "type"
	case RejectTime:
		return "time"
	default:
		panic(fmt.Sprintf("bond: invalid rejection cause %d", c))
	}
}

type RejectionError struct {
	Cause   RejectionCause
	message string
}

func (e *RejectionError) Error() string { return e.message }

func reject(cause RejectionCause, message string) error {
	return &RejectionError{Cause: cause, message: message}
}

func (t *Transport) checkTime(now time.Time) error {
	if now.Before(t.lastTime) {
		return reject(RejectTime, "bond: regressing transport time")
	}
	t.lastTime = now
	return nil
}
