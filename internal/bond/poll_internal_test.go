package bond

import "time"

func poll(p *Transport, now time.Time) []Transmission {
	frames, err := p.Poll(now)
	if err != nil {
		panic(err)
	}
	return frames
}
