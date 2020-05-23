package libs

import (
	"time"
)

type TimerConfig struct {
	lastTriggerT                             time.Time
	initWaitTs, maxWaitTs, waitTs, timeoutTs time.Duration
	nWaits, nWaitsToDouble                   int
}

func NewTimerConfig(initWaitTs, maxWaitTs, waitTs, timeoutTs time.Duration, nWaits, nWaitsToDouble int) *TimerConfig {
	return &TimerConfig{
		lastTriggerT:   time.Unix(0, 0),
		initWaitTs:     initWaitTs,
		maxWaitTs:      maxWaitTs,
		waitTs:         waitTs,
		timeoutTs:      timeoutTs,
		nWaits:         nWaits,
		nWaitsToDouble: nWaitsToDouble,
	}
}

type Timer struct {
	cfg *TimerConfig
}

func NewTimer(cfg *TimerConfig) *Timer {
	if cfg.waitTs < 1*time.Millisecond {
		Logger.Panic("timer interval should not less than 1ms")
	}

	if cfg.maxWaitTs <= cfg.waitTs {
		Logger.Panic("maxWaitTs should bigger than waitTs")
	}

	return &Timer{
		cfg: cfg,
	}
}

func (t *Timer) Tick(now time.Time) bool {
	if now.Sub(t.cfg.lastTriggerT) > t.cfg.timeoutTs {
		t.Reset(now)
		return true
	}

	return false
}

func (t *Timer) Sleep() {
	t.cfg.nWaits++
	if t.cfg.nWaits == t.cfg.nWaitsToDouble {
		if t.cfg.waitTs < t.cfg.maxWaitTs-t.cfg.waitTs {
			t.cfg.waitTs += t.cfg.waitTs
			// Logger.Debug("timer double waitTs", zap.Duration("waitTs", t.cfg.waitTs))
		} else {
			t.cfg.waitTs = t.cfg.maxWaitTs
			t.cfg.nWaits = t.cfg.nWaitsToDouble + 1
		}

		t.cfg.nWaits = 0
	}

	time.Sleep(t.cfg.waitTs)
}

func (t *Timer) Reset(lastT time.Time) {
	t.cfg.lastTriggerT = lastT
	t.cfg.nWaits = 0
	t.cfg.waitTs = t.cfg.initWaitTs
}
