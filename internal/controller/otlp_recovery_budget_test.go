//go:build linux

package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/otlpstate"
)

// A legacy queue may exceed a newly configured limit. Recovery must retain and
// drain that accepted work; fresh admission must not bypass its capacity guards.
// This fixture separates that policy from the production 10-ms scan budget.
func TestRegressionOTLPOverBudgetRecoveryDrainsBeforeFreshAdmission(t *testing.T) {
	ctx := context.Background()
	cfg := OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true, Compress: true, ReplayBatch: 1, MaxWALBytes: 1 << 20, MaxStorageBytes: 64 << 20, StorageScanTimeout: time.Second, Limits: otlpstate.Limits{PayloadBytes: 4 << 20, ResponseBytes: 65536}}
	var calls atomic.Int64
	peers := []OTLPDestination{{ID: "home", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		return barrierAccepted(ctx, e)
	}}}
	p, err := OpenOTLPJournal(ctx, cfg, peers)
	if err != nil {
		t.Fatal(err)
	}
	request := journalBarrierRequest(t)
	for i := 0; i < 24; i++ {
		if err = p.Admit(ctx, request); err != nil {
			p.Close()
			t.Fatal(err)
		}
	}
	namespace := p.Namespace()
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.MaxWALBytes = 4096
	q, err := OpenOTLPJournal(ctx, cfg, peers)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	size, err := q.storageBytes(ctx)
	if err != nil || size <= cfg.MaxWALBytes {
		t.Fatalf("fixture is not over budget: size=%d err=%v", size, err)
	}
	if q.CapacitySnapshot().RecoveryReady {
		t.Fatal("retained snapshot incorrectly marked recovered")
	}
	if err = q.Admit(ctx, request); !errors.Is(err, ErrOTLPJournalCapacity) {
		t.Fatalf("unsafe fresh admission before recovery: %v", err)
	}
	report, err := q.ReplayBatch(ctx)
	if err != nil || report.Delivered != 1 || report.Complete {
		t.Fatalf("bounded replay did not progress: %+v %v", report, err)
	}
	if err = q.Admit(ctx, request); !errors.Is(err, ErrOTLPJournalCapacity) {
		t.Fatalf("partial replay bypassed capacity: %v", err)
	}
	if q.frontier != 24 || q.Namespace() != namespace {
		t.Fatal("refusal changed accepted identities")
	}
	capacityDrain(t, q)
	if calls.Load() != 24 || q.generation.ReleasedThrough != 24 || !q.CapacitySnapshot().RecoveryReady {
		t.Fatalf("accepted work lost or checkpoint incomplete: calls=%d checkpoint=%d", calls.Load(), q.generation.ReleasedThrough)
	}
	// The first pass copied retained records into the active WAL before sending.
	// A checkpoint-skipping pass must reclaim those copies before its smaller
	// byte limit has room; reservation readiness alone is not free capacity.
	capacityDrain(t, q)
	if err = q.Admit(ctx, request); err != nil {
		t.Fatalf("fresh admission did not recover after safe drain/reclaim: %v", err)
	}
	capacityDrain(t, q)
	if calls.Load() != 25 || q.frontier != 25 || q.generation.ReleasedThrough != 25 || q.Namespace() != namespace {
		t.Fatal("post-recovery admission reused identity or lost delivery")
	}
}
