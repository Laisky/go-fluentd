package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

func TestOTLPJournalRetainedReceiptsDrainRestoresSameScanCap(t *testing.T) {
	const records, capEntries = 32, 16
	cfg := OTLPJournalConfig{Directory: t.TempDir(), MaxWALBytes: 1 << 20, MaxStorageBytes: 2 << 20,
		StorageScanMaxEntries: capEntries, StorageScanTimeout: time.Second, ReceiptGC: true, ReplayBatch: 8}
	requests := make([]*otlpwire.Request, records+1)
	for i := range requests {
		body := []byte(fmt.Sprintf(`{"resourceLogs":[],"future":{"seq":%d}}`, i))
		request, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(body), otlpwire.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		requests[i] = request
	}
	gapReady := false
	calls := map[string]int{}
	peer := OTLPDestination{ID: "home", Send: func(_ context.Context, envelope otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls[string(envelope.Payload)]++
		if !gapReady && bytes.Equal(envelope.Payload, requests[0].Payload()) {
			return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
		}
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	}}
	open := func() *OTLPJournal {
		t.Helper()
		p, err := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{peer})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		return p
	}
	drain := func(p *OTLPJournal) (OTLPBatchReport, error) {
		t.Helper()
		var total OTLPBatchReport
		var passErrors []error
		for i := 0; i < records+2; i++ {
			report, err := p.ReplayBatch(context.Background())
			total.Seen += report.Seen
			total.Pending += report.Pending
			total.Delivered += report.Delivered
			passErrors = append(passErrors, err)
			if p.Err() != nil {
				t.Fatal("admission pressure poisoned replay", p.Err())
			}
			if report.Complete {
				total.Complete = true
				return total, errors.Join(passErrors...)
			}
		}
		t.Fatal("existing replay did not reach a bounded snapshot EOF")
		return total, nil
	}
	retainedReceipts := func() map[string][]byte {
		t.Helper()
		entries, err := os.ReadDir(cfg.Directory)
		if err != nil {
			t.Fatal(err)
		}
		receipts := map[string][]byte{}
		for _, entry := range entries {
			if len(entry.Name()) != 69 || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(cfg.Directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			receipts[entry.Name()] = data
		}
		return receipts
	}
	p := open()
	namespace := p.Namespace()
	for _, request := range requests[:records] {
		if err := p.Admit(context.Background(), request); err != nil {
			t.Fatal("initial empty-root admission failed", err)
		}
	}
	report, err := drain(p)
	if !errors.Is(err, ErrOTLPDeliveryPending) || report.Pending != 1 || report.Delivered != records-1 || p.generation.ReleasedThrough != 0 {
		t.Fatalf("did not retain accepted receipts behind the first retryable gap: %+v %v checkpoint=%d", report, err, p.generation.ReleasedThrough)
	}
	beforeReceipts := retainedReceipts()
	if len(beforeReceipts) != records-1 || len(beforeReceipts) <= capEntries {
		t.Fatalf("real retained receipt count did not exceed unchanged cap: receipts=%d cap=%d", len(beforeReceipts), capEntries)
	}
	beforeFrontier, beforeGeneration := p.frontier, p.generation
	writes := 0
	write := p.writeWAL
	p.writeWAL = func(data *journal.Data) error { writes++; return write(data) }
	if err := p.Admit(context.Background(), requests[records]); !errors.Is(err, ErrOTLPJournalScanBudget) || !errors.Is(err, ErrOTLPJournalCapacity) {
		t.Fatalf("retained receipts above cap did not refuse admission: %v", err)
	}
	if writes != 0 || p.frontier != beforeFrontier || p.generation != beforeGeneration || p.Err() != nil || p.ScanBudgetRejected() != 1 {
		t.Fatalf("refusal wrote/advanced identity or poisoned storage: writes=%d frontier=%d fault=%v", writes, p.frontier, p.Err())
	}
	afterReceipts := retainedReceipts()
	if len(afterReceipts) != len(beforeReceipts) {
		t.Fatal("admission refusal discarded retained receipts")
	}
	for name, data := range beforeReceipts {
		if !bytes.Equal(afterReceipts[name], data) {
			t.Fatal("admission refusal changed retained receipt", name)
		}
	}
	// Repair the isolated destination, without changing either scan budget or
	// deleting state. Existing replay must pass the admission entry cap and let
	// checkpoint-proven GC reclaim the retained receipts.
	p.writeWAL = write
	gapReady = true
	report, err = drain(p)
	if err != nil || report.Pending != 0 || report.Delivered != 1 || p.generation.ReleasedThrough != records || p.receiptsReclaimed.Load() != records || len(retainedReceipts()) != 0 {
		t.Fatalf("replay/checkpoint/GC did not recover above admission cap: %+v %v checkpoint=%d reclaimed=%d", report, err, p.generation.ReleasedThrough, p.receiptsReclaimed.Load())
	}
	if p.cfg.StorageScanMaxEntries != capEntries || p.cfg.StorageScanTimeout != cfg.StorageScanTimeout {
		t.Fatal("drain changed the scan budgets")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = open()
	if p.Namespace() != namespace || p.frontier != beforeFrontier || p.cfg.StorageScanMaxEntries != capEntries {
		t.Fatal("reopen changed generation, frontier or scan cap")
	}
	if err := p.Admit(context.Background(), requests[records]); err != nil || p.frontier != beforeFrontier+1 {
		t.Fatalf("admission failed to resume under the SAME cap after drain/reopen: %v frontier=%d", err, p.frontier)
	}
	report, err = drain(p)
	if err != nil || report.Delivered != 1 || p.generation.ReleasedThrough != records+1 || len(retainedReceipts()) != 0 {
		t.Fatalf("resumed admission did not complete durable lifecycle: %+v %v", report, err)
	}
	for i, request := range requests {
		want := 1
		if i == 0 {
			want = 2 // one retryable attempt, followed by the single successful repair
		}
		if calls[string(request.Payload())] != want {
			t.Fatalf("accepted peer called again or a request was lost: record=%d calls=%d want=%d", i, calls[string(request.Payload())], want)
		}
	}
}

func TestOTLPJournalScanBudgetRefusesWithoutWritingOrDiscarding(t *testing.T) {
	for _, scope := range []string{"wal", "root"} {
		t.Run(scope, func(t *testing.T) {
			p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
			p.cfg.MaxStorageBytes = 2 * p.cfg.MaxWALBytes
			p.cfg.StorageScanMaxEntries = 4
			dir := p.cfg.Directory
			if scope == "wal" {
				dir = p.walDir
			}
			for i := 0; i < 12; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("evidence-%d", i)), []byte("preserved"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			write := p.writeWAL
			p.writeWAL = func(d *journal.Data) error { writes++; return write(d) }
			if err := p.Admit(context.Background(), journalBarrierRequest(t)); !errors.Is(err, ErrOTLPJournalScanBudget) || !errors.Is(err, ErrOTLPJournalCapacity) {
				t.Fatalf("partial scan approved admission: %v", err)
			}
			if writes != 0 || p.frontier != 0 || p.Err() != nil {
				t.Fatalf("scan refusal mutated/poisoned WAL: writes=%d frontier=%d fault=%v", writes, p.frontier, p.Err())
			}
			for i := 0; i < 12; i++ {
				data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("evidence-%d", i)))
				if err != nil || string(data) != "preserved" {
					t.Fatalf("retained evidence discarded: %q %v", data, err)
				}
			}
			rejected, _ := p.StorageCounters()
			if rejected != 1 || p.ScanBudgetRejected() != 1 {
				t.Fatal("scan refusal was not counted")
			}
			// Operators can increase a measured budget without deleting evidence.
			p.cfg.StorageScanMaxEntries = 4096
			if err := p.Admit(context.Background(), journalBarrierRequest(t)); err != nil || writes != 1 {
				t.Fatalf("admission did not recover after budget change: %v writes=%d", err, writes)
			}
		})
	}
}

func TestOTLPStorageScanNeverReturnsPartialSumOnCancellationOrEntryBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), []byte("1234"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if size, err := otlpDirectoryBytes(context.Background(), dir, 1000, 3); size != 0 || !errors.Is(err, ErrOTLPJournalScanBudget) {
		t.Fatalf("partial scan looked complete: size=%d err=%v", size, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if size, err := otlpDirectoryBytes(ctx, dir, 1000, 100); size != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan looked complete: size=%d err=%v", size, err)
	}
	// Crossing the byte threshold is already sufficient proof to refuse, so a
	// lower-bound result is safe even if the entry budget would later exhaust.
	if size, err := otlpDirectoryBytes(context.Background(), dir, 1, 1); size <= 1 || err != nil {
		t.Fatalf("full scan failed to stop after proving capacity: size=%d err=%v", size, err)
	}
}

func TestOTLPJournalScanTimeoutDoesNotPoisonOrAdmit(t *testing.T) {
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	p.cfg.StorageScanTimeout = time.Millisecond
	p.scanWAL = func(ctx context.Context) (int64, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	writes := 0
	write := p.writeWAL
	p.writeWAL = func(d *journal.Data) error { writes++; return write(d) }
	if err := p.Admit(context.Background(), journalBarrierRequest(t)); !errors.Is(err, ErrOTLPJournalScanBudget) {
		t.Fatalf("expired metadata budget admitted: %v", err)
	}
	if writes != 0 || p.frontier != 0 || p.Err() != nil || p.ScanBudgetRejected() != 1 {
		t.Fatal("scan timeout mutated or poisoned journal")
	}
	p.scanWAL = p.storageBytes
	p.cfg.StorageScanTimeout = time.Second
	if err := p.Admit(context.Background(), journalBarrierRequest(t)); err != nil || writes != 1 {
		t.Fatalf("healthy metadata did not recover: %v writes=%d", err, writes)
	}
}

func TestOTLPJournalScanBudgetValidation(t *testing.T) {
	for _, change := range []func(*OTLPJournalConfig){
		func(c *OTLPJournalConfig) { c.StorageScanMaxEntries = -1 },
		func(c *OTLPJournalConfig) { c.StorageScanMaxEntries = 1<<20 + 1 },
		func(c *OTLPJournalConfig) { c.StorageScanTimeout = time.Microsecond },
		func(c *OTLPJournalConfig) { c.StorageScanTimeout = 2 * time.Second },
	} {
		cfg := OTLPJournalConfig{Directory: t.TempDir()}
		change(&cfg)
		if p, err := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "home", Send: barrierAccepted}}); err == nil {
			p.Close()
			t.Fatal("invalid budget accepted")
		}
		entries, err := os.ReadDir(cfg.Directory)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid budget changed storage", entries, err)
		}
	}
}
