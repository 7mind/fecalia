package adaptivefec

import "errors"

// errNilClock is returned by NewController when the injected Clock is nil. The
// controller reads time only through the Clock, so a nil one is a construction
// fault, not a runtime condition.
var errNilClock = errors.New("adaptivefec: Clock must not be nil")
