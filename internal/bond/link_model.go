package bond

import "time"

const suspectACKIntervals = 2

type linkModel struct {
	progressAt    time.Time
	awaitingSince time.Time
}

func (m *linkModel) progressAge(now time.Time) time.Duration {
	if m.progressAt.IsZero() {
		return 0
	}
	return max(0, now.Sub(m.progressAt))
}

func (p *lane) liveness(now time.Time) LaneLiveness {
	if !p.up(now) {
		return LaneDead
	}
	if p.seq <= p.receivedHigh {
		return LaneLive
	}
	silence := now.Sub(maxTime(p.model.progressAt, p.model.awaitingSince))
	if silence >= p.rto() {
		return LaneDead
	}
	if silence >= suspectACKIntervals*p.peerACKInterval() {
		return LaneSuspect
	}
	return LaneLive
}

func (p *lane) eligible(c class, now time.Time) bool {
	state := p.liveness(now)
	return state == LaneLive || c != classBulk && state == LaneSuspect
}

func (p *lane) beginAttempt(now time.Time) {
	if p.seq == p.receivedHigh {
		p.model.awaitingSince = now
	}
	p.seq++
}
