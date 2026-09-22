package library

import (
	"testing"
	"time"
)

func TestTimerTick(t *testing.T) {
	timer := NewTimer(NewTimerConfig(time.Millisecond, 4*time.Millisecond, time.Millisecond, time.Second, 0, 2))
	now := time.Unix(100, 0)
	timer.Reset(now)
	if timer.Tick(now.Add(time.Second - time.Nanosecond)) {
		t.Error("triggered before timeout")
	}
	if timer.Tick(now.Add(time.Second)) {
		t.Error("existing strict timeout boundary changed")
	}
	if !timer.Tick(now.Add(time.Second + time.Nanosecond)) {
		t.Error("did not trigger after timeout")
	}
	if timer.Tick(now.Add(time.Second + 2*time.Nanosecond)) {
		t.Error("did not reset trigger time")
	}
}

func TestTimerSleep(t *testing.T) {
	timer := NewTimer(NewTimerConfig(time.Millisecond, 4*time.Millisecond, time.Millisecond, time.Second, 0, 2))
	for _, want := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond} {
		start := time.Now()
		timer.Sleep()
		if timer.cfg.waitTs != want {
			t.Fatalf("backoff=%v, want %v", timer.cfg.waitTs, want)
		}
		if elapsed := time.Since(start); elapsed < want {
			t.Errorf("slept %v, want at least %v", elapsed, want)
		}
	}
	timer.Reset(time.Now())
	if timer.cfg.waitTs != time.Millisecond || timer.cfg.nWaits != 0 {
		t.Error("Reset did not reset backoff")
	}
}
