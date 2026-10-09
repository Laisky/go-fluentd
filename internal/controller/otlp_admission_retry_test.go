//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// A wall-clock metadata budget can refuse a healthy, small ownership directory
// when a caller is descheduled. Only this transient refusal permits a retry.
func admitWithScanBudgetRetry(p *OTLPJournal, request *otlpwire.Request, attempts int) (int, error) {
	frontier, generation, reserved := p.frontier, p.generation, p.reservedRecords.Load()
	cfg := p.cfg
	reserveBytes, reserveFiles := p.maxRecordReserve, p.maxRecordFiles
	type evidence struct {
		Mode fs.FileMode
		Sum  [32]byte
	}
	snapshot := func() (map[string]evidence, error) {
		files := map[string]evidence{}
		err := filepath.WalkDir(cfg.Directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = evidence{Mode: info.Mode(), Sum: sha256.Sum256(data)}
			return nil
		})
		return files, err
	}
	before, err := snapshot()
	if err != nil {
		return 0, err
	}
	writes := 0
	write := p.writeWAL
	p.writeWAL = func(data *journal.Data) error { writes++; return write(data) }
	defer func() { p.writeWAL = write }()
	for n := 1; n <= attempts; n++ {
		rejected, _ := p.StorageCounters()
		scanRejected := p.ScanBudgetRejected()
		err = p.Admit(context.Background(), request)
		if err == nil {
			if writes != 1 || p.frontier != frontier+1 || p.generation != generation {
				return n, fmt.Errorf("successful retry changed durable admission identity: writes=%d frontier=%d", writes, p.frontier)
			}
			return n, nil
		}
		if !errors.Is(err, ErrOTLPJournalScanBudget) || !errors.Is(err, ErrOTLPJournalCapacity) {
			return n, err
		}
		after, snapshotErr := snapshot()
		nowRejected, _ := p.StorageCounters()
		if snapshotErr != nil {
			return n, snapshotErr
		}
		if writes != 0 || p.frontier != frontier || p.generation != generation ||
			p.reservedRecords.Load() != reserved || p.maxRecordReserve != reserveBytes || p.maxRecordFiles != reserveFiles || p.Err() != nil || !reflect.DeepEqual(before, after) ||
			nowRejected != rejected+1 || p.ScanBudgetRejected() != scanRejected+1 ||
			p.cfg.StorageScanTimeout != cfg.StorageScanTimeout || p.cfg.StorageScanMaxEntries != cfg.StorageScanMaxEntries ||
			p.cfg.MaxWALBytes != cfg.MaxWALBytes || p.cfg.MaxStorageBytes != cfg.MaxStorageBytes || p.cfg.MaxStorageFiles != cfg.MaxStorageFiles {
			return n, fmt.Errorf("scan-budget refusal mutated durable state, budgets, or counters: %v", err)
		}
		if n == attempts {
			return n, fmt.Errorf("admission retry exhausted %d attempts: %w", attempts, err)
		}
	}
	return 0, errors.New("admission retry requires at least one attempt")
}

func TestRegressionOTLPDefaultScanBudgetRefusalPreservesLifecycle(t *testing.T) {
	p, err := OpenOTLPJournal(context.Background(), OTLPJournalConfig{
		Directory: t.TempDir(), ReceiptGC: true, ReplayBatch: 2,
	}, []OTLPDestination{{ID: "a", Send: barrierAccepted}, {ID: "b", Send: barrierAccepted}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.cfg.StorageScanTimeout != 25*time.Millisecond || p.cfg.StorageScanMaxEntries != 4096 {
		t.Fatal("production scan defaults changed")
	}
	scan := p.scanWAL
	scanned := 0
	p.scanWAL = func(ctx context.Context) (int64, error) {
		scanned++
		if scanned == 1 {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return scan(ctx)
	}
	n, err := admitWithScanBudgetRetry(p, journalBarrierRequest(t), 32)
	if err != nil || n < 2 || p.ScanBudgetRejected() != uint64(n-1) {
		t.Fatalf("valid scan refusal failed same-budget recovery: attempts=%d err=%v rejected=%d", n, err, p.ScanBudgetRejected())
	}
	capacityDrain(t, p)
	if p.frontier != 1 || p.generation.ReleasedThrough != 1 || capacityReceiptCount(t, p.cfg.Directory) != 0 || p.producer.Counters().Accepted != 2 {
		t.Fatal("same-budget recovery lost identity, fanout, or checkpoint reclamation")
	}
}

func TestRegressionOTLPScanBudgetRetryRemainsBounded(t *testing.T) {
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	scanned := 0
	p.scanWAL = func(ctx context.Context) (int64, error) {
		scanned++
		<-ctx.Done()
		return 0, ctx.Err()
	}
	n, err := admitWithScanBudgetRetry(p, journalBarrierRequest(t), 3)
	if n != 3 || scanned != 3 || !errors.Is(err, ErrOTLPJournalScanBudget) || !errors.Is(err, ErrOTLPJournalCapacity) || p.frontier != 0 || p.Err() != nil {
		t.Fatalf("permanent exhaustion was swallowed or unbounded: attempts=%d scanned=%d err=%v", n, scanned, err)
	}
}

func TestRegressionOTLPScanBudgetRetryRejectsOtherErrors(t *testing.T) {
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	failed := errors.New("synthetic metadata I/O failure")
	scanned := 0
	p.scanWAL = func(context.Context) (int64, error) { scanned++; return 0, failed }
	n, err := admitWithScanBudgetRetry(p, journalBarrierRequest(t), 32)
	if n != 1 || scanned != 1 || !errors.Is(err, failed) || p.frontier != 0 || !errors.Is(p.Err(), failed) {
		t.Fatalf("non-budget error retried or swallowed: attempts=%d scanned=%d err=%v fault=%v", n, scanned, err, p.Err())
	}
}

// The retry harness must fail if a budget refusal changes evidence. A wrapped
// budget sentinel here would let a negative exhaustion control accept the defect.
func TestRegressionOTLPScanBudgetRetryRejectsRefusalMutation(t *testing.T) {
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	path := filepath.Join(p.cfg.Directory, "retained-evidence")
	if err := os.WriteFile(path, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	p.scanWAL = func(ctx context.Context) (int64, error) {
		if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
			return 0, err
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	n, err := admitWithScanBudgetRetry(p, journalBarrierRequest(t), 3)
	if n != 1 || err == nil || errors.Is(err, ErrOTLPJournalScanBudget) ||
		!strings.Contains(err.Error(), "mutated durable state") {
		t.Fatalf("refusal mutation mistaken for recoverable budget exhaustion: attempts=%d err=%v", n, err)
	}
}

func TestRegressionOTLPExpiredFinalScanCannotRaiseReservationHighWater(t *testing.T) {
	p, err := OpenOTLPJournal(context.Background(), OTLPJournalConfig{
		Directory: t.TempDir(), MaxWALBytes: 1 << 20, MaxStorageBytes: 2 << 20,
		Limits: otlpstate.Limits{PayloadBytes: 1 << 20, ResponseBytes: 256},
	}, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	small := journalBarrierRequest(t)
	if _, err = admitWithScanBudgetRetry(p, small, 32); err != nil {
		t.Fatal(err)
	}
	beforeBytes, beforeFiles := p.maxRecordReserve, p.maxRecordFiles
	large, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "",
		strings.NewReader("{\"resourceLogs\":[],\"future\":\""+strings.Repeat("x", 128<<10)+"\"}"), otlpwire.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	record, err := p.producer.Plan(p.namespace, 2, otlpstate.Envelope{
		Signal: string(large.Signal()), ContentType: large.ContentType(), Items: int64(large.Items()), Payload: large.Payload(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := record.JournalData()
	if err != nil {
		t.Fatal(err)
	}
	largeReserve, _ := p.capacityRecord(record, len(data.Data["otlp_delivery"].([]byte)))
	if 2*largeReserve+4096 >= p.cfg.MaxStorageBytes || 3*largeReserve+4096 <= p.cfg.MaxStorageBytes ||
		3*beforeBytes+4096 >= p.cfg.MaxStorageBytes {
		t.Fatalf("fixture does not separate useful small work from rejected large high-water: small=%d large=%d cap=%d", beforeBytes, largeReserve, p.cfg.MaxStorageBytes)
	}
	scan := p.scanRoot
	p.scanRoot = func(ctx context.Context, stopAfter int64, maxEntries int) (int64, int64, error) {
		used, files, err := scan(ctx, stopAfter, maxEntries)
		if err != nil {
			return used, files, err
		}
		// A completed inventory can race the final deadline check. Return its
		// complete result only once the unchanged real 25ms budget has expired.
		<-ctx.Done()
		return used, files, nil
	}
	writes := 0
	write := p.writeWAL
	p.writeWAL = func(data *journal.Data) error { writes++; return write(data) }
	err = p.Admit(context.Background(), large)
	if !errors.Is(err, ErrOTLPJournalScanBudget) || !errors.Is(err, ErrOTLPJournalCapacity) ||
		writes != 0 || p.frontier != 1 || p.reservedRecords.Load() != 1 || p.Err() != nil {
		t.Fatalf("final deadline did not safely refuse before writing: %v writes=%d frontier=%d", err, writes, p.frontier)
	}
	if p.maxRecordReserve != beforeBytes || p.maxRecordFiles != beforeFiles {
		t.Errorf("refused envelope raised effective reservation high-water: bytes=%d->%d files=%d->%d",
			beforeBytes, p.maxRecordReserve, beforeFiles, p.maxRecordFiles)
	}
	p.scanRoot, p.writeWAL = scan, write
	for i := 0; i < 2; i++ {
		if _, err = admitWithScanBudgetRetry(p, small, 32); err != nil {
			t.Fatalf("refused large envelope poisoned later small-envelope capacity under SAME limits: %v", err)
		}
	}
	capacityDrain(t, p)
	if p.frontier != 3 || p.producer.Counters().Accepted != 3 || p.cfg.StorageScanTimeout != 25*time.Millisecond ||
		p.cfg.MaxStorageBytes != 2<<20 {
		t.Fatal("post-refusal small work was lost or limits changed")
	}
}

func TestRegressionOTLPCanceledFinalScanCannotRaiseReservationHighWater(t *testing.T) {
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scan := p.scanRoot
	p.scanRoot = func(scanCtx context.Context, stopAfter int64, maxEntries int) (int64, int64, error) {
		used, files, err := scan(scanCtx, stopAfter, maxEntries)
		if err != nil {
			return used, files, err
		}
		cancel()
		return used, files, nil
	}
	writes := 0
	write := p.writeWAL
	p.writeWAL = func(data *journal.Data) error { writes++; return write(data) }
	err := p.Admit(ctx, journalBarrierRequest(t))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrOTLPJournalScanBudget) ||
		writes != 0 || p.frontier != 0 || p.reservedRecords.Load() != 0 || p.Err() != nil ||
		p.maxRecordReserve != 0 || p.maxRecordFiles != 0 || p.ScanBudgetRejected() != 0 {
		t.Fatalf("parent cancellation changed effective reservations or error meaning: %v writes=%d frontier=%d reserve=%d/%d",
			err, writes, p.frontier, p.maxRecordReserve, p.maxRecordFiles)
	}
	p.scanRoot, p.writeWAL = scan, write
	if _, err = admitWithScanBudgetRetry(p, journalBarrierRequest(t), 32); err != nil {
		t.Fatal(err)
	}
	capacityDrain(t, p)
}
