package throttle

import (
	"sync"
	"time"
)

type Gate interface {
	Acquire(now time.Time) (bool, time.Duration)
}

type intervalGate struct {
	interval time.Duration

	mu   sync.Mutex
	last time.Time
}

func NewIntervalGate(interval time.Duration) Gate {
	return &intervalGate{interval: interval}
}

func (g *intervalGate) Acquire(now time.Time) (bool, time.Duration) {
	if g.interval <= 0 {
		return true, 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.last.IsZero() {
		if elapsed := now.Sub(g.last); elapsed < g.interval {
			return false, g.interval - elapsed
		}
	}
	g.last = now
	return true, 0
}

type openGate struct{}

func NewOpenGate() Gate { return openGate{} }

func (openGate) Acquire(time.Time) (bool, time.Duration) { return true, 0 }
