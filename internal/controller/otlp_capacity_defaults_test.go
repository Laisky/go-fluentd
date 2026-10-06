//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

func TestRegressionOTLPServiceDefaultUsesCheckpointReceipts(t *testing.T) {
	cfg, err := ParseOTLPServiceConfig(map[string]interface{}{"enabled": true})
	if err != nil || cfg == nil || !cfg.ReceiptGC {
		t.Fatalf("missing safe receipt default: %+v %v", cfg, err)
	}
	cfg, err = ParseOTLPServiceConfig(map[string]interface{}{"enabled": true, "receipt_gc": false})
	if err != nil || cfg.ReceiptGC {
		t.Fatal("explicit retain policy was not preserved")
	}
}

func TestRegressionOTLPDefaultRootCapacityIncludesReceipts(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(fmt.Sprint(compress), func(t *testing.T) {
			cfg := OTLPJournalConfig{Directory: t.TempDir(), Compress: compress, StorageScanMaxEntries: 16}
			p, err := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}, {ID: "b", Send: barrierAccepted}})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			refused := false
			for i := 0; i < 24; i++ {
				before := p.frontier
				err := p.Admit(context.Background(), journalBarrierRequest(t))
				if errors.Is(err, ErrOTLPJournalCapacity) {
					if p.frontier != before || p.Err() != nil {
						t.Fatal("capacity refusal changed identity or poisoned replay")
					}
					refused = true
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				for n := 0; n < 3; n++ {
					report, err := p.ReplayBatch(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if report.Complete {
						break
					}
				}
			}
			if !refused {
				t.Fatal("healthy destination receipts bypassed the default whole-root budget")
			}
		})
	}
}

func TestRegressionOTLPDefaultBytePolicyPreservesEvidence(t *testing.T) {
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	path := filepath.Join(p.cfg.Directory, "retained-evidence")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Logical sparse size only: no disk-fill experiment or payload allocation.
	if err = f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = p.Admit(context.Background(), journalBarrierRequest(t)); !errors.Is(err, ErrOTLPJournalCapacity) {
		t.Fatalf("default root bytes ignored: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() != 1<<30 || p.frontier != 0 || p.Err() != nil {
		t.Fatal("refusal changed evidence or acknowledged input")
	}
}

func capacityReceiptCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if len(entry.Name()) == 69 && strings.HasSuffix(entry.Name(), ".json") {
			n++
		}
	}
	return n
}

func capacityDrain(t *testing.T, p *OTLPJournal) {
	t.Helper()
	for n := 0; n < 1024; n++ {
		r, err := p.ReplayBatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.Complete {
			return
		}
	}
	t.Fatal("snapshot did not finish")
}

func TestRegressionOTLPDefaultGCSteadyStateFanoutAndRestart(t *testing.T) {
	service, err := ParseOTLPServiceConfig(map[string]interface{}{"enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, compressed := range []bool{false, true} {
		for _, payloadBytes := range []int{0, 64 << 10} {
			t.Run(fmt.Sprintf("gzip=%v/payload=%d", compressed, payloadBytes), func(t *testing.T) {
				cfg := OTLPJournalConfig{Directory: t.TempDir(), Compress: compressed, ReceiptGC: service.ReceiptGC, ReplayBatch: 2}
				var calls atomic.Int64
				send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
					calls.Add(1)
					return barrierAccepted(ctx, e)
				}
				peers := []OTLPDestination{{ID: "a", Send: send}, {ID: "b", Send: send}}
				open := func() *OTLPJournal {
					p, e := OpenOTLPJournal(context.Background(), cfg, peers)
					if e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() { p.Close() })
					return p
				}
				p := open()
				req, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(`{"resourceLogs":[],"future":"`+strings.Repeat("x", payloadBytes)+`"}`), otlpwire.DefaultLimits())
				if e != nil {
					t.Fatal(e)
				}
				for cycle := int64(1); cycle <= 16; cycle++ {
					for i := 0; i < 3; i++ {
						if e = p.Admit(context.Background(), req); e != nil {
							t.Fatal(e)
						}
					}
					capacityDrain(t, p)
					if capacityReceiptCount(t, cfg.Directory) != 0 || p.frontier != cycle*3 || p.generation.ReleasedThrough != cycle*3 || p.generation.Version != 2 {
						t.Fatal("receipt retention or checkpoint/identity drift")
					}
					used, files, e := otlpDirectoryUsage(context.Background(), cfg.Directory, 1<<40, 4096)
					if e != nil || used > 2<<20 || files > 12 {
						t.Fatalf("healthy storage did not settle: bytes=%d files=%d err=%v", used, files, e)
					}
					if e = p.Close(); e != nil {
						t.Fatal(e)
					}
					p = open()
				}
				if calls.Load() != 96 {
					t.Fatalf("lost/re-exported destination calls: %d", calls.Load())
				}
			})
		}
	}
}

func TestRegressionOTLPCapacityExactAdmissionBoundaries(t *testing.T) {
	for _, dimension := range []string{"bytes", "files"} {
		for _, delta := range []int64{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/%d", dimension, delta), func(t *testing.T) {
				p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
				// Retained evidence is always counted and never removed to meet a cap.
				for i := 0; i < 10; i++ {
					if err := os.WriteFile(filepath.Join(p.cfg.Directory, fmt.Sprint("evidence-", i)), []byte("retain"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				req := journalBarrierRequest(t)
				r, err := p.producer.Plan(p.namespace, 1, otlpstate.Envelope{Signal: string(req.Signal()), ContentType: req.ContentType(), Items: int64(req.Items()), Payload: req.Payload()})
				if err != nil {
					t.Fatal(err)
				}
				d, err := r.JournalData()
				if err != nil {
					t.Fatal(err)
				}
				reserveBytes, reserveFiles := p.capacityRecord(r, len(d.Data["otlp_delivery"].([]byte)))
				used, files, err := otlpDirectoryUsage(context.Background(), p.cfg.Directory, 1<<40, 4096)
				if err != nil {
					t.Fatal(err)
				}
				// Configure exact limits after measuring stable synthetic initialization.
				if dimension == "bytes" {
					p.cfg.MaxStorageBytes = used + reserveBytes + 4096 + delta
				} else {
					p.cfg.MaxStorageFiles = int(files + reserveFiles + 4 + delta)
				}
				err = p.Admit(context.Background(), req)
				if delta < 0 {
					if !errors.Is(err, ErrOTLPJournalCapacity) || p.frontier != 0 || p.Err() != nil {
						t.Fatalf("unsafe boundary refusal: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					capacityDrain(t, p)
				}
				for i := 0; i < 10; i++ {
					b, e := os.ReadFile(filepath.Join(p.cfg.Directory, fmt.Sprint("evidence-", i)))
					if e != nil || string(b) != "retain" {
						t.Fatal("evidence changed")
					}
				}
			})
		}
	}
}

func TestRegressionOTLPConcurrentAdmissionReservesFanoutAndResumes(t *testing.T) {
	var delivered atomic.Int64
	send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		delivered.Add(1)
		return barrierAccepted(ctx, e)
	}
	p, err := OpenOTLPJournal(context.Background(), OTLPJournalConfig{Directory: t.TempDir(), MaxStorageFiles: 24, ReceiptGC: true}, []OTLPDestination{{ID: "a", Send: send}, {ID: "b", Send: send}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var accepted atomic.Int64
	var wg sync.WaitGroup
	req := journalBarrierRequest(t)
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			e := p.Admit(context.Background(), req)
			if e == nil {
				accepted.Add(1)
			} else if !errors.Is(e, ErrOTLPJournalCapacity) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	n := accepted.Load()
	if n == 0 || n == 32 || p.frontier != n || p.CapacitySnapshot().PendingUpperBound != n {
		t.Fatalf("concurrent reservation escaped: admitted=%d frontier=%d", n, p.frontier)
	}
	capacityDrain(t, p)
	if delivered.Load() != 2*n || p.generation.ReleasedThrough != n || capacityReceiptCount(t, p.cfg.Directory) != 0 {
		t.Fatal("accepted work lost under pressure")
	}
	if err = p.Admit(context.Background(), req); err != nil {
		t.Fatal("admission did not resume after checkpoint", err)
	}
	capacityDrain(t, p)
	if p.frontier != n+1 || delivered.Load() != 2*(n+1) {
		t.Fatal("identity reused or fanout lost")
	}
}

func TestRegressionOTLPCapacityRecoveryPrecedesNewAdmission(t *testing.T) {
	cfg := OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true, ReplayBatch: 1}
	var calls atomic.Int64
	peers := []OTLPDestination{{ID: "a", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		return barrierAccepted(ctx, e)
	}}}
	p, err := OpenOTLPJournal(context.Background(), cfg, peers)
	if err != nil {
		t.Fatal(err)
	}
	req := journalBarrierRequest(t)
	for i := 0; i < 3; i++ {
		if err = p.Admit(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	q, err := OpenOTLPJournal(context.Background(), cfg, peers)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if q.CapacitySnapshot().RecoveryReady {
		t.Fatal("retained state reported ready before reservation recovery")
	}
	for i := 0; i < 3; i++ {
		if e := q.Admit(context.Background(), req); !errors.Is(e, ErrOTLPJournalCapacity) || q.frontier != 3 || q.Err() != nil {
			t.Fatal("unrecovered reservation admitted new work", e)
		}
		if _, e := q.ReplayBatch(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	capacityDrain(t, q)
	if !q.CapacitySnapshot().RecoveryReady {
		t.Fatal("completed reservation recovery did not become ready")
	}
	if err = q.Admit(context.Background(), req); err != nil {
		t.Fatal("recovered admission refused", err)
	}
	capacityDrain(t, q)
	if calls.Load() != 4 || q.frontier != 4 {
		t.Fatal("recovery lost work or reused identity")
	}
}

func TestRegressionOTLPCapacityPreservesQuarantines(t *testing.T) {
	cfg := OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true, MaxStorageFiles: 16}
	p, err := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "bad", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{Kind: otlpstate.Permanent, HTTPStatus: 400, Response: []byte("evidence")}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	accepted := 0
	for i := 0; i < 32; i++ {
		err = p.Admit(context.Background(), journalBarrierRequest(t))
		if errors.Is(err, ErrOTLPJournalCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
		capacityDrain(t, p)
	}
	if accepted == 0 || accepted == 32 || capacityReceiptCount(t, cfg.Directory) != accepted || p.Err() != nil {
		t.Fatal("quarantine did not retain bounded evidence")
	}
	for _, e := range mustCapacityEntries(t, cfg.Directory) {
		if strings.HasSuffix(e.Name(), ".json") && len(e.Name()) == 69 {
			b, e := os.ReadFile(filepath.Join(cfg.Directory, e.Name()))
			if e != nil || !bytes.Contains(b, []byte("ZXZpZGVuY2U=")) {
				t.Fatal("quarantine evidence changed")
			}
		}
	}
}
func mustCapacityEntries(t *testing.T, root string) []os.DirEntry {
	t.Helper()
	e, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRegressionOTLPCapacityPolicyValidationAndObservation(t *testing.T) {
	peers := []OTLPDestination{{ID: "accepted", Send: barrierAccepted}}
	for _, files := range []int{-1, 1, 15, 1<<20 + 1} {
		root := t.TempDir()
		p, e := OpenOTLPJournal(context.Background(), OTLPJournalConfig{Directory: root, MaxStorageFiles: files}, peers)
		if p != nil {
			p.Close()
		}
		if e == nil || !strings.Contains(e.Error(), "max_storage_files") {
			t.Fatalf("files=%d was not rejected by its own validation: %v", files, e)
		}
		if len(mustCapacityEntries(t, root)) != 0 {
			t.Fatal("invalid capacity created storage before validation")
		}
	}
	for _, files := range []int{16, 1 << 20} {
		p, e := OpenOTLPJournal(context.Background(), OTLPJournalConfig{Directory: t.TempDir(), MaxStorageFiles: files}, peers)
		if e != nil {
			t.Fatalf("valid boundary files=%d: %v", files, e)
		}
		if e = p.Close(); e != nil {
			t.Fatal(e)
		}
	}
	for _, wal := range []int64{4096, 256 << 20, 1 << 30, 1 << 50} {
		if got := defaultOTLPStorageBytes(wal); got < wal || got < 512<<20 || got > 1<<50 {
			t.Fatalf("unbounded default: %d", got)
		}
	}
	p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
	if err := p.Admit(context.Background(), journalBarrierRequest(t)); err != nil {
		t.Fatal(err)
	}
	snapshot := p.CapacitySnapshot()
	if snapshot.Bytes <= 0 || snapshot.Files <= 0 || snapshot.PendingUpperBound != 1 {
		t.Fatal("missing capacity observations")
	}
	b, err := json.Marshal(snapshot)
	if err != nil || !bytes.Contains(b, []byte(`"pending_upper_bound":1`)) {
		t.Fatal("unstable management field names")
	}
	if _, _, ok := otlpFilesystemCapacity(filepath.Join(t.TempDir(), "absent")); ok {
		t.Fatal("missing filesystem falsely reported as available")
	}
}
