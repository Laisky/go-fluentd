package library

import (
	"testing"
	"time"

	"gofluentd/library/log"

	"github.com/Laisky/zap"
)

var (
	initWaitTs     = 200 * time.Millisecond
	maxWaitTs      = 1 * time.Second
	waitTs         = initWaitTs
	nWaits         = 0
	nWaitsToDouble = 2
	timeoutTs      = 5 * time.Second
	timer          = NewTimer(
		NewTimerConfig(
			initWaitTs,
			maxWaitTs,
			waitTs,
			timeoutTs,
			nWaits,
			nWaitsToDouble,
		),
	)
)

func TestTimerTick(t *testing.T) {
	now := time.Now()
	timer.Reset(now)

	if timer.Tick(now.Add(timeoutTs).Truncate(1 * time.Millisecond)) {
		t.Error("expect false, got true")
	}

	if timer.Tick(now.Add(timeoutTs)) {
		t.Error("expect true, got false")
	}
}

func TestTimerSleep(t *testing.T) {
	var (
		start, end   time.Time
		expectWaitTs = initWaitTs
	)
	start = time.Now()
	timer.Sleep()
	end = time.Now()
	if end.Sub(start) < expectWaitTs {
		t.Errorf("except %v, got %v", expectWaitTs, end.Sub(start))
	}

	start = time.Now()
	timer.Sleep()
	expectWaitTs += expectWaitTs
	end = time.Now()
	if end.Sub(start) < expectWaitTs {
		t.Errorf("except %v, got %v", expectWaitTs, end.Sub(start))
	}

	timer.Sleep()
	start = time.Now()
	timer.Sleep()
	expectWaitTs += expectWaitTs
	end = time.Now()
	if end.Sub(start) < expectWaitTs {
		t.Errorf("except %v, got %v", expectWaitTs, end.Sub(start))
	}

	for i := 0; i < 8; i++ {
		timer.Sleep()
	}
	expectWaitTs = 1100 * time.Millisecond
	start = time.Now()
	timer.Sleep()
	end = time.Now()
	if end.Sub(start) > expectWaitTs {
		t.Errorf("except %v, got %v", expectWaitTs, end.Sub(start))
	}
}

func init() {
	if err := log.Logger.ChangeLevel("debug"); err != nil {
		log.Logger.Panic("change level", zap.Error(err))
	}
}
