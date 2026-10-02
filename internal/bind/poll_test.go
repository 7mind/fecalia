package bind

import (
	"github.com/7mind/wanbond/internal/bond"
	"time"
)

func poll(p *bond.Transport, now time.Time) []bond.Transmission {
	frames, err := p.Poll(now)
	if err != nil {
		panic(err)
	}
	return frames
}
