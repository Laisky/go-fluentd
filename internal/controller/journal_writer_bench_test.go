package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
	"gofluentd/library/log"
)

type measuredJournal struct {
	*journal.Journal
	syncs int64 // read only after the single worker exits
}

func (s *measuredJournal) Sync() error { s.syncs++; return s.Journal.Sync() }

// Run with -benchtime=2048x (or 4096x) to bound WAL size. Request preparation is
// outside timing; completed durable receipts AND live publication are inside.
// Each operation is one 2 KiB record, not an entire batch or an unsynced append.
func BenchmarkJournalDurableService(b *testing.B) {
	_ = log.Logger.ChangeLevel("error")
	_ = journal.Logger.ChangeLevel("error")
	for _, compressed := range []bool{false, true} {
		for _, clients := range []int{1, 16, 64} {
			b.Run(fmt.Sprintf("gzip=%v/clients=%d", compressed, clients), func(b *testing.B) {
				if b.N > 262144 {
					b.Fatal("use bounded -benchtime=2048x or 4096x")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				jj, err := journal.NewJournal(journal.WithBufDirPath(b.TempDir()), journal.WithIsCompress(compressed), journal.WithBufSizeByte(1<<30), journal.WithIsAggresiveGC(false), journal.WithFlushInterval(time.Hour), journal.WithRotateDuration(time.Hour))
				if err != nil {
					b.Fatal(err)
				}
				if err = jj.Start(ctx); err != nil {
					b.Fatal(err)
				}
				defer jj.Close()
				backend := &measuredJournal{Journal: jj}
				j := &Journal{JournalCfg: &JournalCfg{MsgPool: &sync.Pool{}}, outChan: make(chan *library.FluentMsg, b.N)}
				in := make(chan *library.FluentMsg, clients*2)
				messages := make([]*library.FluentMsg, b.N)
				receipts := make([]chan error, b.N)
				payload := strings.Repeat("0123456789abcdef", 128)
				for i := range messages {
					messages[i], receipts[i] = writerMessage(int64(i+1), true)
					messages[i].Message["payload"] = payload
				}
				done := make(chan struct{})
				go func() { defer close(done); j.runDataWriter(ctx, "source", backend, in, utils.NewCounter()) }()
				errs := make(chan error, clients)
				b.SetBytes(2048)
				b.ReportAllocs()
				b.ResetTimer()
				var wg sync.WaitGroup
				for client := 0; client < clients; client++ {
					wg.Add(1)
					go func(index int) {
						defer wg.Done()
						for i := index; i < b.N; i += clients {
							in <- messages[i]
							if err := <-receipts[i]; err != nil {
								errs <- err
								return
							}
						}
					}(client)
				}
				wg.Wait()
				close(in)
				<-done
				b.StopTimer()
				close(errs)
				for err := range errs {
					b.Fatal(err)
				}
				if len(j.outChan) != b.N {
					b.Fatalf("only %d of %d records published", len(j.outChan), b.N)
				}
				seen := make(map[int64]bool, b.N)
				for len(j.outChan) > 0 {
					m := <-j.outChan
					if seen[m.ID] || m.ID < 1 || m.ID > int64(b.N) || m.Message["payload"] != payload {
						b.Fatal("missing/duplicate/corrupted output")
					}
					seen[m.ID] = true
				}
				b.ReportMetric(float64(backend.syncs)/float64(b.N), "syncs/msg")
				b.ReportMetric(1, "msgs/op")
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "msgs/s")
			})
		}
	}
}
