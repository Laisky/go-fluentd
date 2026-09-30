package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gofluentd/internal/controller"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

func edgeReceiptCount(t *testing.T, dir string) int {
	t.Helper()
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, n := range names {
		if len(n.Name()) == 69 && strings.HasSuffix(n.Name(), ".json") {
			count++
		}
	}
	return count
}

func edgeCheckpoint(t *testing.T, dir string) int64 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version  int   `json:"version"`
		Released int64 `json:"released_through"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 2 {
		t.Fatalf("no durable v2 checkpoint: %s", b)
	}
	return v.Released
}

func TestOTLPEdgeGCSteadyStateAndRestart(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(fmt.Sprint(compress), func(t *testing.T) {
			cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), Compress: compress, ReplayBatch: 2, ReceiptGC: true}
			var calls atomic.Int64
			peer := controller.OTLPDestination{ID: "home", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
				calls.Add(1)
				return lifecycleAccepted(ctx, e)
			}}
			p := lifecycleOpen(t, cfg, peer)
			for cycle := 0; cycle < 8; cycle++ {
				for i := 0; i < 5; i++ {
					if err := p.Admit(context.Background(), lifecycleRequest(t, cycle*5+i)); err != nil {
						t.Fatal(err)
					}
				}
				report, err := lifecycleDrain(t, p)
				if err != nil || report.Delivered != 5 {
					t.Fatal(report, err)
				}
				if n := edgeReceiptCount(t, cfg.Directory); n != 0 {
					t.Fatalf("completed receipt growth: %d", n)
				}
				if edgeCheckpoint(t, cfg.Directory) != int64((cycle+1)*5) {
					t.Fatal("frontier lost")
				}
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
				p = lifecycleOpen(t, cfg, peer)
			}
			if calls.Load() != 40 {
				t.Fatalf("loss or duplicate after GC: %d", calls.Load())
			}
		})
	}
}

func TestOTLPEdgeGCDoesNotCrossPendingGap(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReplayBatch: 1, ReceiptGC: true}
	var a, b atomic.Int64
	ready := false
	pa := controller.OTLPDestination{ID: "a", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		a.Add(1)
		return lifecycleAccepted(ctx, e)
	}}
	pb := controller.OTLPDestination{ID: "b", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		b.Add(1)
		if !ready && bytes.Contains(e.Payload, []byte(`"seq":1,`)) {
			return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
		}
		return lifecycleAccepted(ctx, e)
	}}
	p := lifecycleOpen(t, cfg, pa, pb)
	for i := 0; i < 3; i++ {
		if err := p.Admit(context.Background(), lifecycleRequest(t, i)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := lifecycleDrain(t, p)
	if !errors.Is(err, controller.ErrOTLPDeliveryPending) || report.Pending != 1 {
		t.Fatal(report, err)
	}
	if edgeCheckpoint(t, cfg.Directory) != 1 {
		t.Fatal("checkpoint crossed failed ID 2")
	}
	if edgeReceiptCount(t, cfg.Directory) != 3 {
		t.Fatal("discarded unresolved peer receipts")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	ready = true
	p = lifecycleOpen(t, cfg, pa, pb)
	report, err = lifecycleDrain(t, p)
	if err != nil || report.Delivered != 1 {
		t.Fatal(report, err)
	}
	if a.Load() != 3 || b.Load() != 4 {
		t.Fatalf("re-exported accepted peer: a=%d b=%d", a.Load(), b.Load())
	}
	if edgeReceiptCount(t, cfg.Directory) != 0 || edgeCheckpoint(t, cfg.Directory) != 3 {
		t.Fatal("resolved gap not collected")
	}
}

func TestOTLPEdgeGCUsesFrozenFrontier(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	peer := controller.OTLPDestination{ID: "home", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return lifecycleAccepted(ctx, e)
	}}
	p := lifecycleOpen(t, cfg, peer)
	if err := p.Admit(context.Background(), lifecycleRequest(t, 1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.ReplayBatch(context.Background()); done <- err }()
	<-entered
	if err := p.Admit(context.Background(), lifecycleRequest(t, 2)); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if edgeCheckpoint(t, cfg.Directory) != 1 {
		t.Fatal("new admission covered by old snapshot")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = lifecycleOpen(t, cfg, peer)
	report, err := lifecycleDrain(t, p)
	if err != nil || report.Delivered != 1 || calls.Load() != 2 {
		t.Fatal(report, err, calls.Load())
	}
}

func TestOTLPEdgeGCKeepsQuarantine(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "bad", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{Kind: otlpstate.Permanent, HTTPStatus: 400}, nil
	}})
	if err := p.Admit(context.Background(), lifecycleRequest(t, 0)); err != nil {
		t.Fatal(err)
	}
	report, err := lifecycleDrain(t, p)
	if err != nil || report.Quarantined != 1 || report.Delivered != 0 {
		t.Fatal(report, err)
	}
	if edgeReceiptCount(t, cfg.Directory) != 1 {
		t.Fatal("quarantined payload deleted")
	}
	if edgeCheckpoint(t, cfg.Directory) != 1 {
		t.Fatal("resolved quarantine not checkpointed")
	}
}

func TestOTLPEdgeWholeDirectoryAdmissionGuard(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), MaxWALBytes: 4096, MaxStorageBytes: 8192}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "home", Send: lifecycleAccepted})
	// The old WAL-only check cannot see this retained receipt/evidence file.
	if err := os.WriteFile(filepath.Join(cfg.Directory, "retained-evidence"), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	err := p.Admit(context.Background(), lifecycleRequest(t, 0))
	if !errors.Is(err, controller.ErrOTLPJournalCapacity) {
		t.Fatalf("ignored receipt/evidence bytes: %v", err)
	}
	if p.Err() != nil {
		t.Fatal("capacity pressure poisoned journal")
	}
	rejected, _ := p.StorageCounters()
	if rejected != 1 {
		t.Fatal("missing capacity observation")
	}
}

func TestOTLPEdgeCorruptCheckpointFailsClosed(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true}
	peer := controller.OTLPDestination{ID: "home", Send: lifecycleAccepted}
	p := lifecycleOpen(t, cfg, peer)
	if err := p.Admit(context.Background(), lifecycleRequest(t, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycleDrain(t, p); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Directory, "generation.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`"released_through":1`), []byte(`"released_through":9`), 1)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if q, err := controller.OpenOTLPJournal(context.Background(), cfg, []controller.OTLPDestination{peer}); err == nil {
		q.Close()
		t.Fatal("corrupt frontier accepted")
	}
}

func TestOTLPEdgeCheckpointFailurePreservesReceipts(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReceiptGC: true, ReplayBatch: 1}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "home", Send: lifecycleAccepted})
	if err := p.Admit(context.Background(), lifecycleRequest(t, 0)); err != nil {
		t.Fatal(err)
	}
	if r, err := p.ReplayBatch(context.Background()); err != nil || r.Released != 1 {
		t.Fatal(r, err)
	}
	path := filepath.Join(cfg.Directory, "generation.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReplayBatch(context.Background()); err == nil {
		t.Fatal("failed checkpoint reported success")
	}
	if edgeReceiptCount(t, cfg.Directory) != 1 {
		t.Fatal("receipt deleted before durable frontier")
	}
}

func TestOTLPEdgeByteGuardCountsPayloadNotItems(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), MaxWALBytes: 8192, MaxStorageBytes: 16384}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "home", Send: lifecycleAccepted})
	payload := []byte(`{"resourceLogs":[],"padding":"` + strings.Repeat("a", 12000) + `"}`)
	req, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(payload), otlpwire.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Admit(context.Background(), req); !errors.Is(err, controller.ErrOTLPJournalCapacity) {
		t.Fatal("large zero-item request bypassed byte budget", err)
	}
}
