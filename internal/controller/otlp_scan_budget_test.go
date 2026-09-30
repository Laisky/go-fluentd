package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
)

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
