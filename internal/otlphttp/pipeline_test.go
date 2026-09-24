package otlphttp_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/controller"
	"gofluentd/internal/otlphttp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// Real HTTP handlers/exporters, producer accounting, files and reopen cycles.
// This is not the configured application binary or a physical power-loss test.
func TestOTLPHTTPDurablePipeline(t *testing.T) {
	for _, signal := range signals {
		for _, ct := range encodings {
			for _, jgzip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/journal-gzip=%v", signal, ct, jgzip), func(t *testing.T) {
					ctx := context.Background()
					env := fixture(t, signal, ct)
					var calls [3]atomic.Int32
					var retryOK atomic.Bool
					bad := make(chan string, 16)
					peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var i int
						fmt.Sscanf(r.URL.Path, "/peer%d", &i)
						if i < 0 || i > 2 {
							w.WriteHeader(400)
							return
						}
						calls[i].Add(1)
						b, _ := io.ReadAll(r.Body)
						if r.Header.Get("Content-Encoding") == "gzip" {
							z, err := gzip.NewReader(bytes.NewReader(b))
							if err != nil {
								bad <- err.Error()
								w.WriteHeader(400)
								return
							}
							b, err = io.ReadAll(z)
							z.Close()
							if err != nil {
								bad <- err.Error()
							}
						}
						if r.Header.Get("Content-Type") != ct || !bytes.Equal(b, env.Payload) {
							bad <- "downstream bytes or type changed"
						}
						w.Header().Set("Content-Type", ct)
						if i == 1 && !retryOK.Load() {
							w.WriteHeader(503)
							return
						}
						response := []byte{}
						if ct == otlpwire.JSON {
							response = []byte(`{}`)
						}
						if i == 2 {
							response = []byte{0x0a, 0x02, 0x08, 0x01}
							if ct == otlpwire.JSON {
								field := map[otlpwire.Signal]string{otlpwire.Logs: "rejectedLogRecords", otlpwire.Metrics: "rejectedDataPoints", otlpwire.Traces: "rejectedSpans"}[signal]
								response = []byte(fmt.Sprintf(`{"partialSuccess":{"%s":"1"}}`, field))
							}
						}
						w.Write(response)
					}))
					defer peer.Close()
					var destinations []controller.OTLPDestination
					for i := 0; i < 3; i++ {
						c := config(fmt.Sprintf("%s/peer%d", peer.URL, i))
						c.MaxAttempts = 1
						c.Gzip = !jgzip
						e := exporter(t, c)
						destinations = append(destinations, controller.OTLPDestination{ID: fmt.Sprintf("peer%d", i), Send: e.Send})
					}
					wal, receipts := t.TempDir(), t.TempDir()
					open := func() (*journal.Journal, *otlpstate.Store, *controller.OTLPProducer) {
						t.Helper()
						j, err := journal.NewJournal(journal.WithBufDirPath(wal), journal.WithIsCompress(jgzip), journal.WithIsAggresiveGC(false), journal.WithFlushInterval(time.Hour), journal.WithRotateDuration(time.Hour))
						if err != nil {
							t.Fatal(err)
						}
						if err = j.Start(ctx); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(j.Close)
						store, err := otlpstate.Open(receipts, otlpstate.DefaultLimits())
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { store.Close() })
						p, err := controller.NewOTLPProducer(store, destinations)
						if err != nil {
							t.Fatal(err)
						}
						return j, store, p
					}
					j, store, p := open()
					admissions := make(chan controller.OTLPRecord, 1)
					handler := receiver(t, otlphttp.ReceiverConfig{}, func(ctx context.Context, r *otlpwire.Request) error {
						admitted, err := p.Plan("persistent-pipeline-generation", 1, otlpstate.Envelope{Signal: string(r.Signal()), ContentType: r.ContentType(), Items: int64(r.Items()), Payload: r.Payload()})
						if err != nil {
							return err
						}
						d, err := admitted.JournalData()
						if err != nil {
							return err
						}
						if err = j.WriteData(d); err != nil {
							return err
						}
						if err = j.Sync(); err != nil {
							return err
						}
						admissions <- admitted
						return nil
					})
					ingress := httptest.NewServer(handler)
					body := compressed(t, env.Payload)
					req, _ := http.NewRequest("POST", ingress.URL+"/v1/"+string(signal), bytes.NewReader(body))
					req.Header.Set("Content-Type", ct)
					req.Header.Set("Content-Encoding", "gzip")
					resp, err := ingress.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					ingress.Close()
					if resp.StatusCode != 200 {
						t.Fatalf("durable ingress rejected: %d", resp.StatusCode)
					}
					admitted := <-admissions
					report, err := p.Process(ctx, admitted, func(context.Context, int64) error { t.Error("released pending destinations"); return nil })
					if err == nil || report.Resolved || report.JournalReleased {
						t.Fatal("unresolved peer reported complete", report, err)
					}
					j.Close()
					store.Close()
					j, store, p = open()
					if !j.LockLegacy() {
						t.Fatal("replay lock")
					}
					d := new(journal.Data)
					if err = j.LoadLegacyBuf(d); err != nil {
						t.Fatal(err)
					}
					restored, err := controller.OTLPRecordFromJournal(d)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(restored.Envelope.Payload, env.Payload) {
						t.Fatal("journal changed wire payload")
					}
					if err = j.WriteData(d); err != nil {
						t.Fatal(err)
					}
					if err = j.Sync(); err != nil {
						t.Fatal(err)
					}
					if err = j.LoadLegacyBuf(new(journal.Data)); err != io.EOF {
						t.Fatal("replay cleanup", err)
					}
					retryOK.Store(true)
					report, err = p.Process(ctx, restored, func(_ context.Context, id int64) error {
						if err := j.WriteId(id); err != nil {
							return err
						}
						return j.Sync()
					})
					if err != nil || !report.Resolved || report.FullyDelivered || !report.JournalReleased {
						t.Fatal("incorrect quarantine accounting", report, err)
					}
					for i, n := range []int32{1, 2, 1} {
						if calls[i].Load() != n {
							t.Fatalf("peer %d: got %d calls want %d", i, calls[i].Load(), n)
						}
					}
					if p.Counters().Accepted != 1 || p.Counters().Quarantined != 0 || p.Counters().ReplayHits != 2 {
						t.Fatal("reopened counters misreport deliveries", p.Counters())
					}
					j.Close()
					store.Close()
					j, store, _ = open()
					if !j.LockLegacy() {
						t.Fatal("second replay lock")
					}
					if err = j.LoadLegacyBuf(new(journal.Data)); err != io.EOF {
						t.Fatal("released record replayed", err)
					}
					select {
					case why := <-bad:
						t.Fatal(why)
					default:
					}
				})
			}
		}
	}
}
