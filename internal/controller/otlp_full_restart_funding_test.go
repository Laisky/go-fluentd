//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"errors"
	"gofluentd/internal/diskbudget"
	"gofluentd/internal/otlpstate"
	"sync/atomic"
	"testing"
)

func TestSharedOTLPFullRestartDrainsFundedPendingOriginal(t *testing.T) {
	ctx := context.Background()
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	cfg := sharedConfig(root, group)
	cfg.RetainAccepted = true
	cfg.WALSegmentBytes = 4 << 20
	cfg.MaxWALBytes = 128 << 20
	cfg.MaxStorageBytes = 256 << 20
	var down atomic.Bool
	down.Store(true)
	send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		if down.Load() {
			return otlpstate.Outcome{Kind: otlpstate.Retryable}, errors.New("isolated sink held")
		}
		return barrierAccepted(ctx, e)
	}
	p, err := OpenOTLPJournal(ctx, cfg, []OTLPDestination{{ID: "a", Send: send}})
	if err != nil {
		t.Fatal(err)
	}
	request := boundedRequest(t, 1000)
	if err = p.Admit(ctx, request); err != nil {
		p.Close()
		t.Fatal(err)
	}
	if _, err = p.ReplayBatch(ctx); p.Err() != nil {
		p.Close()
		t.Fatal(err)
	}
	other, err := diskbudget.Open(ctx, group, sharedPrivateDir(t), cfg.SharedStorageBytes)
	if err != nil {
		p.Close()
		t.Fatal(err)
	}
	defer other.Close()
	snapshot, err := p.budget.Snapshot(ctx)
	if err != nil {
		p.Close()
		t.Fatal(err)
	}
	if err = other.Reserve(ctx, snapshot.Limit-snapshot.ChargedBytes-snapshot.ReservedBytes); err != nil {
		p.Close()
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	q, err := OpenOTLPJournal(ctx, cfg, []OTLPDestination{{ID: "a", Send: send}})
	if err != nil {
		t.Fatal("funded pending store could not restart", err)
	}
	defer q.Close()
	for i := 0; i < 3; i++ {
		_, err = q.ReplayBatch(ctx)
		if q.Err() != nil {
			t.Fatal("rewinding unchanged pending snapshot exhausted funded recovery", err)
		}
	}
	down.Store(false)
	capacityDrain(t, q)
	if q.generation.ReleasedThrough != 1 || q.Err() != nil {
		t.Fatal("funded accepted original did not drain", q.generation, q.Err())
	}
	n, err := q.store.WalkAcceptedArchive(ctx, q.namespace, func(_ otlpstate.Key, e otlpstate.Envelope) error {
		if !bytes.Equal(e.Payload, request.Payload()) {
			return errors.New("original changed")
		}
		return nil
	})
	if err != nil || n != 1 {
		t.Fatal("accepted original not preserved", n, err)
	}
	final, err := q.budget.Snapshot(ctx)
	if err != nil || final.ChargedBytes+final.ReservedBytes > final.Limit {
		t.Fatal(final, err)
	}
}
