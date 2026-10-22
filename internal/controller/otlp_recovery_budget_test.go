//go:build linux

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// recoveryPayloadsMatch compares exact payload multiplicities, not total counts.
func recoveryPayloadsMatch(want, got map[string]int) bool {
	if len(want) != len(got) {
		return false
	}
	for payload, count := range want {
		if got[payload] != count {
			return false
		}
	}
	return true
}

func TestRegressionOTLPRecoveryOracleRejectsBalancedLossAndDuplicate(t *testing.T) {
	want := map[string]int{"one": 1, "two": 1}
	if !recoveryPayloadsMatch(want, map[string]int{"one": 1, "two": 1}) {
		t.Fatal("healthy payloads rejected")
	}
	for name, got := range map[string]map[string]int{
		"balanced-duplicate-loss": {"one": 2},
		"changed-payload":         {"one": 1, "different": 1},
		"missing":                 {"one": 1},
		"unexpected":              {"one": 1, "two": 1, "third": 1},
	} {
		if recoveryPayloadsMatch(want, got) {
			t.Fatalf("%s was incorrectly accepted by exact-payload oracle", name)
		}
	}
}

// A legacy queue may exceed a newly configured limit. Recovery must retain and
// drain that accepted work; fresh admission must not bypass its capacity guards.
// This fixture separates that policy from the production 10-ms scan budget.
func TestRegressionOTLPOverBudgetRecoveryDrainsBeforeFreshAdmission(t *testing.T) {
	ctx := context.Background()
	cfg := OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true, Compress: true, ReplayBatch: 1, MaxWALBytes: 1 << 20, MaxStorageBytes: 64 << 20, StorageScanTimeout: time.Second, Limits: otlpstate.Limits{PayloadBytes: 4 << 20, ResponseBytes: 65536}}
	requests := make([]*otlpwire.Request, 25)
	want := make(map[string]int, len(requests))
	for i := range requests {
		body := fmt.Sprintf(`{"resourceLogs":[],"future":"retained-%02d"}`, i)
		request, parseErr := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(body), otlpwire.DefaultLimits())
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		requests[i] = request
		want[string(request.Payload())]++
	}
	var calls atomic.Int64
	var receivedMu sync.Mutex
	received := make(map[string]int, len(requests))
	peers := []OTLPDestination{{ID: "home", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		receivedMu.Lock()
		received[string(e.Payload)]++
		receivedMu.Unlock()
		return barrierAccepted(ctx, e)
	}}}
	p, err := OpenOTLPJournal(ctx, cfg, peers)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 24; i++ {
		if err = p.Admit(ctx, requests[i]); err != nil {
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
	if err = q.Admit(ctx, requests[24]); !errors.Is(err, ErrOTLPJournalCapacity) {
		t.Fatalf("unsafe fresh admission before recovery: %v", err)
	}
	report, err := q.ReplayBatch(ctx)
	if err != nil || report.Delivered != 1 || report.Complete {
		t.Fatalf("bounded replay did not progress: %+v %v", report, err)
	}
	if err = q.Admit(ctx, requests[24]); !errors.Is(err, ErrOTLPJournalCapacity) {
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
	if err = q.Admit(ctx, requests[24]); err != nil {
		t.Fatalf("fresh admission did not recover after safe drain/reclaim: %v", err)
	}
	capacityDrain(t, q)
	if calls.Load() != 25 || q.frontier != 25 || q.generation.ReleasedThrough != 25 || q.Namespace() != namespace {
		t.Fatal("post-recovery admission reused identity or lost delivery")
	}
	receivedMu.Lock()
	defer receivedMu.Unlock()
	if !recoveryPayloadsMatch(want, received) {
		t.Fatalf("replay lost, duplicated or changed accepted payload identities: expected=%v actual=%v", want, received)
	}
}
