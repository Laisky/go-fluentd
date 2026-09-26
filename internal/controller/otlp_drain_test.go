package controller_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gofluentd/internal/controller"
	"gofluentd/internal/otlpstate"
)

func TestOTLPHealthyFullBatchesDoNotWaitForRetryInterval(t *testing.T) {
	delivered := make(chan struct{}, 3)
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReplayBatch: 1, ReplayInterval: 5 * time.Second}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		delivered <- struct{}{}
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	}})
	for n := 1; n <= 3; n++ {
		if e := p.Admit(context.Background(), lifecycleRequest(t, n)); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	defer func() {
		cancel()
		p.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("scheduler did not stop")
		}
	}()
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("first batch did not start")
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for n := 0; n < 2; n++ {
		select {
		case <-delivered:
		case <-deadline.C:
			t.Fatal("healthy backlog was delayed by the retry interval")
		}
	}
}

func TestOTLPIdleSchedulerDoesNotRotateAndAdmissionWakesIt(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReplayInterval: 20 * time.Millisecond}
	delivered := make(chan struct{}, 1)
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		delivered <- struct{}{}
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	defer func() { cancel(); p.Close(); <-done }()
	names := func() []string {
		xs, e := filepath.Glob(filepath.Join(cfg.Directory, "wal", "*.buf*"))
		if e != nil {
			t.Fatal(e)
		}
		return xs
	}
	time.Sleep(100 * time.Millisecond)
	before := names()
	if len(before) == 0 {
		t.Fatal("no active WAL")
	}
	time.Sleep(150 * time.Millisecond)
	if !reflect.DeepEqual(before, names()) {
		t.Fatal("idle scheduler kept rotating empty WAL snapshots")
	}
	if e := p.Admit(ctx, lifecycleRequest(t, 1)); e != nil {
		t.Fatal(e)
	}
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("idle admission lost its wakeup")
	}
}
