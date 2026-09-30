package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/library/log"
	"gofluentd/library/otlpwire"
)

func otlpBenchmarkOwner(b *testing.B, evidence int) (*OTLPJournal, *otlpwire.Request) {
	b.Helper()
	_ = log.Logger.ChangeLevel("error")
	_ = journal.Logger.ChangeLevel("error")
	p, err := OpenOTLPJournal(context.Background(), OTLPJournalConfig{
		Directory: b.TempDir(), MaxWALBytes: 1 << 30, MaxStorageBytes: 2 << 30, StorageScanMaxEntries: 65536, StorageScanTimeout: time.Second, ReceiptGC: true,
	}, []OTLPDestination{{ID: "home", Send: barrierAccepted}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { p.Close() })
	// Retained evidence models metadata pressure independently of payload size.
	// It is not inserted into the receipt API and never sent to a destination.
	for i := 0; i < evidence; i++ {
		if err = os.WriteFile(filepath.Join(p.cfg.Directory, fmt.Sprintf("evidence-%08d", i)), []byte("x"), 0600); err != nil {
			b.Fatal(err)
		}
	}
	body := []byte(fmt.Sprintf(`{"resourceLogs":[],"future":%q}`, strings.Repeat("x", 2048)))
	r, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(body), otlpwire.DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	return p, r
}

// Use fixed, bounded iterations, e.g. -benchtime=256x. Setup and shutdown are
// outside timing; all real admission and replay fsync barriers remain enabled.
func BenchmarkOTLPJournalAdmission(b *testing.B) {
	for _, evidence := range []int{0, 1000, 10000} {
		for _, clients := range []int{1, 8} {
			b.Run(fmt.Sprintf("evidence=%d/clients=%d", evidence, clients), func(b *testing.B) {
				if b.N > 8192 {
					b.Fatal("use bounded -benchtime=256x or 1024x")
				}
				p, req := otlpBenchmarkOwner(b, evidence)
				initial, err := p.storageBytes(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				var wg sync.WaitGroup
				errs := make(chan error, clients)
				b.SetBytes(int64(len(req.Payload())))
				b.ReportAllocs()
				b.ResetTimer()
				for client := 0; client < clients; client++ {
					wg.Add(1)
					go func(index int) {
						defer wg.Done()
						for i := index; i < b.N; i += clients {
							if e := p.Admit(context.Background(), req); e != nil {
								errs <- e
								return
							}
						}
					}(client)
				}
				wg.Wait()
				b.StopTimer()
				close(errs)
				for err := range errs {
					b.Fatal(err)
				}
				final, err := p.storageBytes(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(final-initial)/float64(b.N), "wal-B/msg")
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "msgs/s")
			})
		}
	}
}

func BenchmarkOTLPJournalReplay(b *testing.B) {
	if b.N > 8192 {
		b.Fatal("use bounded -benchtime=256x or 1024x")
	}
	p, req := otlpBenchmarkOwner(b, 0)
	for i := 0; i < b.N; i++ {
		if err := p.Admit(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
	delivered := 0
	b.SetBytes(int64(len(req.Payload())))
	b.ReportAllocs()
	b.ResetTimer()
	for {
		report, err := p.ReplayBatch(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		delivered += report.Delivered
		if report.Complete {
			break
		}
	}
	b.StopTimer()
	if delivered != b.N {
		b.Fatalf("delivered %d of %d admitted envelopes", delivered, b.N)
	}
	b.ReportMetric(float64(delivered)/b.Elapsed().Seconds(), "msgs/s")
}

// Unlike the throughput benchmark, these operations must refuse admission.
// This measures bounded metadata work with an intentionally excessive backlog.
func BenchmarkOTLPJournalScanBudgetRefusal(b *testing.B) {
	for _, entries := range []int{128, 1024, 4096} {
		b.Run(fmt.Sprintf("entries=%d", entries), func(b *testing.B) {
			if b.N > 8192 {
				b.Fatal("use bounded -benchtime=256x or 1024x")
			}
			p, req := otlpBenchmarkOwner(b, 10000)
			p.cfg.StorageScanMaxEntries = entries
			p.cfg.StorageScanTimeout = 25 * time.Millisecond
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := p.Admit(context.Background(), req); !errors.Is(err, ErrOTLPJournalScanBudget) {
					b.Fatalf("scan did not refuse telemetry: %v", err)
				}
			}
			b.StopTimer()
			if p.frontier != 0 {
				b.Fatal("refusal benchmark admitted work")
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "refusals/s")
		})
	}
}
