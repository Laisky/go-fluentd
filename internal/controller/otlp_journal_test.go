//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/controller"
	"gofluentd/internal/otlphttp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

func lifecycleOpen(t *testing.T, c controller.OTLPJournalConfig, ds ...controller.OTLPDestination) *controller.OTLPJournal {
	t.Helper()
	p, e := controller.OpenOTLPJournal(context.Background(), c, ds)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func lifecycleRequest(t *testing.T, n int) *otlpwire.Request {
	t.Helper()
	b := []byte(fmt.Sprintf(`{"resourceLogs":[],"future":{"seq":%d,"value":"18446744073709551615","label":"é🙂"}}`, n))
	r, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(b), otlpwire.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func lifecycleAccepted(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
	return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
}
func lifecycleDrain(t *testing.T, p *controller.OTLPJournal) (controller.OTLPBatchReport, error) {
	t.Helper()
	var total controller.OTLPBatchReport
	var errs []error
	for i := 0; i < 100; i++ {
		r, e := p.ReplayBatch(context.Background())
		total.Seen += r.Seen
		total.Released += r.Released
		total.Delivered += r.Delivered
		total.Quarantined += r.Quarantined
		total.Pending += r.Pending
		if e != nil {
			errs = append(errs, e)
		}
		if r.Complete {
			total.Complete = true
			return total, errors.Join(errs...)
		}
		if p.Err() != nil {
			t.Fatal(p.Err())
		}
	}
	t.Fatal("bounded replay never completed its snapshot")
	return total, nil
}
func TestOTLPJournalAdmissionRecoveryAndIdentity(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(fmt.Sprint(gzip), func(t *testing.T) {
			root := t.TempDir()
			cfg := controller.OTLPJournalConfig{Directory: root, Compress: gzip, ReplayBatch: 2}
			var mu sync.Mutex
			seen := map[string]int{}
			peer := controller.OTLPDestination{ID: "archive", Send: func(_ context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
				mu.Lock()
				seen[string(e.Payload)]++
				mu.Unlock()
				return lifecycleAccepted(context.Background(), e)
			}}
			p := lifecycleOpen(t, cfg, peer)
			namespace := p.Namespace()
			for i := 0; i < 5; i++ {
				if e := p.Admit(context.Background(), lifecycleRequest(t, i)); e != nil {
					t.Fatal(e)
				}
			}
			if e := p.Close(); e != nil {
				t.Fatal(e)
			}
			p = lifecycleOpen(t, cfg, peer)
			if p.Namespace() != namespace {
				t.Fatal("generation changed across restart")
			}
			r, e := lifecycleDrain(t, p)
			if e != nil || r.Seen != 5 || r.Delivered != 5 || r.Released != 5 {
				t.Fatalf("lost accepted envelopes: %+v %v", r, e)
			}
			if e = p.Close(); e != nil {
				t.Fatal(e)
			}
			p = lifecycleOpen(t, cfg, peer)
			r, e = lifecycleDrain(t, p)
			if e != nil || r.Seen != 0 {
				t.Fatal("released record remained pending", r, e)
			}
			// All earlier records are acknowledged and reclaimed. New identities must
			// still advance, or old accepted receipts could silently suppress new data.
			if e = p.Admit(context.Background(), lifecycleRequest(t, 5)); e != nil {
				t.Fatal(e)
			}
			r, e = lifecycleDrain(t, p)
			if e != nil || r.Delivered != 1 {
				t.Fatal("identity reused after cleanup", r, e)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 6 {
				t.Fatalf("want 6 distinct caller payloads, got %d", len(seen))
			}
			for payload, n := range seen {
				if n != 1 {
					t.Fatalf("re-exported accepted envelope %s: %d", payload, n)
				}
			}
		})
	}
}
func TestOTLPJournalBoundedReplayDoesNotRestartCursor(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), ReplayBatch: 1}
	var mu sync.Mutex
	var got []string
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "offline", Send: func(_ context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		mu.Lock()
		got = append(got, string(e.Payload))
		mu.Unlock()
		return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
	}})
	for i := 0; i < 3; i++ {
		if e := p.Admit(context.Background(), lifecycleRequest(t, i)); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 3; i++ {
		r, e := p.ReplayBatch(context.Background())
		if r.Seen != 1 || r.Pending != 1 || !errors.Is(e, controller.ErrOTLPDeliveryPending) {
			t.Fatal(r, e)
		}
		if i == 0 {
			if e = p.Admit(context.Background(), lifecycleRequest(t, 3)); e != nil {
				t.Fatal(e)
			}
		}
	}
	mu.Lock()
	if len(got) != 3 {
		t.Fatal(got)
	}
	for i := 0; i < 3; i++ {
		if got[i] != string(lifecycleRequest(t, i).Payload()) {
			t.Fatalf("bounded replay restarted before unread records: %v", got)
		}
	}
	mu.Unlock()
	r, e := p.ReplayBatch(context.Background())
	if e != nil || !r.Complete {
		t.Fatal("snapshot failed to reach EOF", r, e)
	}
	// The next snapshot contains every unresolved copy and the new admission.
	r, e = lifecycleDrain(t, p)
	if r.Seen != 4 || r.Pending != 4 || !errors.Is(e, controller.ErrOTLPDeliveryPending) {
		t.Fatal("pending snapshot lost records", r, e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	p = lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "offline", Send: lifecycleAccepted})
	r, e = lifecycleDrain(t, p)
	if e != nil || r.Delivered != 4 {
		t.Fatal("cleanup erased unresolved data", r, e)
	}
}
func TestOTLPJournalFrozenDestinationsAcrossReconfiguration(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir()}
	var a, b, extra atomic.Int32
	good := func(c *atomic.Int32) func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
			c.Add(1)
			return lifecycleAccepted(ctx, e)
		}
	}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "a", Send: good(&a)}, controller.OTLPDestination{ID: "b", Send: good(&b)})
	if e := p.Admit(context.Background(), lifecycleRequest(t, 1)); e != nil {
		t.Fatal(e)
	}
	p.Close()
	p = lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "a", Send: good(&a)})
	r, e := lifecycleDrain(t, p)
	if e == nil || r.Released != 0 || a.Load() != 0 {
		t.Fatal("missing obligation silently dropped", r, e)
	}
	p.Close()
	p = lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "a", Send: good(&a)}, controller.OTLPDestination{ID: "b", Send: good(&b)}, controller.OTLPDestination{ID: "new", Send: good(&extra)})
	r, e = lifecycleDrain(t, p)
	if e != nil || r.Delivered != 1 || a.Load() != 1 || b.Load() != 1 || extra.Load() != 0 {
		t.Fatal("saved obligations changed", r, e, a.Load(), b.Load(), extra.Load())
	}
}
func TestOTLPJournalConcurrentAdmissionAndOwnership(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir()}
	var calls atomic.Int32
	peer := controller.OTLPDestination{ID: "a", Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		return lifecycleAccepted(ctx, e)
	}}
	p := lifecycleOpen(t, cfg, peer)
	other, e := controller.OpenOTLPJournal(context.Background(), cfg, []controller.OTLPDestination{peer})
	if e == nil {
		other.Close()
		t.Fatal("second owner acquired same directory")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		req := lifecycleRequest(t, i)
		wg.Add(1)
		go func() { defer wg.Done(); errs <- p.Admit(context.Background(), req) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	r, e := lifecycleDrain(t, p)
	if e != nil || r.Delivered != 32 || calls.Load() != 32 {
		t.Fatal("concurrent admission lost identities", r, e, calls.Load())
	}
}
func TestOTLPJournalRejectsCapacityAndCanceledAdmission(t *testing.T) {
	cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), MaxWALBytes: 4096}
	p := lifecycleOpen(t, cfg, controller.OTLPDestination{ID: "a", Send: lifecycleAccepted})
	body := []byte(fmt.Sprintf(`{"resourceLogs":[],"future":%q}`, string(bytes.Repeat([]byte{'x'}, 4096))))
	req, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(body), otlpwire.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Admit(context.Background(), req); !errors.Is(e, controller.ErrOTLPJournalCapacity) {
		t.Fatal("oversized admission succeeded", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = p.Admit(ctx, lifecycleRequest(t, 1)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	r, e := lifecycleDrain(t, p)
	if e != nil || r.Seen != 0 {
		t.Fatal("rejected admission leaked to WAL", r, e)
	}
	if e = p.Admit(context.Background(), lifecycleRequest(t, 2)); e != nil {
		t.Fatal("capacity rejection poisoned usable writer", e)
	}
}
func TestOTLPJournalRefusesMissingGenerationAndForeignWrapper(t *testing.T) {
	t.Run("missing-generation", func(t *testing.T) {
		cfg := controller.OTLPJournalConfig{Directory: t.TempDir()}
		peer := controller.OTLPDestination{ID: "a", Send: lifecycleAccepted}
		p := lifecycleOpen(t, cfg, peer)
		p.Admit(context.Background(), lifecycleRequest(t, 1))
		p.Close()
		if e := os.Remove(filepath.Join(cfg.Directory, "generation.json")); e != nil {
			t.Fatal(e)
		}
		if q, e := controller.OpenOTLPJournal(context.Background(), cfg, []controller.OTLPDestination{peer}); e == nil {
			q.Close()
			t.Fatal("missing namespace silently regenerated")
		}
	})
	t.Run("foreign-wrapper", func(t *testing.T) {
		cfg := controller.OTLPJournalConfig{Directory: t.TempDir()}
		var calls atomic.Int32
		peer := controller.OTLPDestination{ID: "a", Send: func(c context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
			calls.Add(1)
			return lifecycleAccepted(c, e)
		}}
		p := lifecycleOpen(t, cfg, peer)
		p.Close()
		j, e := journal.NewJournal(journal.WithBufDirPath(filepath.Join(cfg.Directory, "wal")), journal.WithIsAggresiveGC(false))
		if e != nil {
			t.Fatal(e)
		}
		if e = j.Start(context.Background()); e != nil {
			t.Fatal(e)
		}
		d, e := (controller.OTLPRecord{Namespace: "another-generation", ID: 1, Required: []string{"a"}, Envelope: otlpstate.Envelope{Signal: "logs", ContentType: otlpwire.JSON, Payload: []byte(`{}`)}}).JournalData()
		if e != nil {
			t.Fatal(e)
		}
		if e = j.WriteData(d); e != nil {
			t.Fatal(e)
		}
		if e = j.Sync(); e != nil {
			t.Fatal(e)
		}
		j.Close()
		p = lifecycleOpen(t, cfg, peer)
		if _, e = p.ReplayBatch(context.Background()); !errors.Is(e, controller.ErrOTLPJournalFault) || calls.Load() != 0 {
			t.Fatal("foreign WAL identity exported", e)
		}
		if e = p.Admit(context.Background(), lifecycleRequest(t, 3)); !errors.Is(e, controller.ErrOTLPJournalFault) {
			t.Fatal("faulted journal accepted new data", e)
		}
	})
}
func TestOTLPJournalInvalidConfigurationAndGeneration(t *testing.T) {
	for _, name := range []string{"legacy-data", "corrupt-generation", "symlink-generation", "symlink-wal", "bad-interval", "bad-batch", "bad-capacity", "nil-context"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c := controller.OTLPJournalConfig{Directory: dir}
			ctx := context.Background()
			switch name {
			case "legacy-data":
				os.WriteFile(filepath.Join(dir, "legacy.data"), []byte("preserve"), 0600)
			case "corrupt-generation":
				os.WriteFile(filepath.Join(dir, "generation.json"), []byte(`{"version":9}`), 0600)
			case "symlink-generation":
				target := filepath.Join(t.TempDir(), "target")
				os.WriteFile(target, []byte("{}"), 0600)
				os.Symlink(target, filepath.Join(dir, "generation.json"))
			case "symlink-wal":
				g := map[string]interface{}{"version": 1, "namespace": fmt.Sprintf("%064x", 1)}
				b, _ := json.Marshal(g)
				os.WriteFile(filepath.Join(dir, "generation.json"), b, 0600)
				os.Symlink(t.TempDir(), filepath.Join(dir, "wal"))
			case "bad-interval":
				c.ReplayInterval = time.Nanosecond
			case "bad-batch":
				c.ReplayBatch = -1
			case "bad-capacity":
				c.MaxWALBytes = -1
			case "nil-context":
				ctx = nil
			}
			if p, e := controller.OpenOTLPJournal(ctx, c, []controller.OTLPDestination{{ID: "a", Send: lifecycleAccepted}}); e == nil {
				p.Close()
				t.Fatal("unsafe configuration/storage accepted")
			}
			if name == "legacy-data" {
				b, e := os.ReadFile(filepath.Join(dir, "legacy.data"))
				if e != nil || string(b) != "preserve" {
					t.Fatal("legacy evidence changed")
				}
			}
		})
	}
}
func TestOTLPJournalRunCadenceAndClose(t *testing.T) {
	var mu sync.Mutex
	var attempts []time.Time
	third := make(chan struct{})
	c := controller.OTLPJournalConfig{Directory: t.TempDir(), ReplayInterval: 20 * time.Millisecond}
	p := lifecycleOpen(t, c, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		if len(attempts) == 3 {
			close(third)
		}
		mu.Unlock()
		return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
	}})
	if e := p.Admit(context.Background(), lifecycleRequest(t, 1)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()
	select {
	case <-third:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not retry")
	}
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, controller.ErrOTLPJournalClosed) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	if e := p.Admit(context.Background(), lifecycleRequest(t, 2)); !errors.Is(e, controller.ErrOTLPJournalClosed) {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(attempts); i++ {
		if attempts[i].Sub(attempts[i-1]) < c.ReplayInterval {
			t.Fatal("retry exhaustion caused tight loop")
		}
	}
}
func TestOTLPJournalCloseCancelsActiveDestination(t *testing.T) {
	entered := make(chan struct{})
	p := lifecycleOpen(t, controller.OTLPJournalConfig{Directory: t.TempDir()}, controller.OTLPDestination{ID: "a", Send: func(ctx context.Context, _ otlpstate.Envelope) (otlpstate.Outcome, error) {
		close(entered)
		<-ctx.Done()
		return otlpstate.Outcome{}, ctx.Err()
	}})
	if e := p.Admit(context.Background(), lifecycleRequest(t, 1)); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { p.ReplayBatch(context.Background()); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no destination call")
	}
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked on active destination")
	}
}

// This binds the ordinary HTTP handler to the real owner rather than a callback
// that manually supplies a fixed namespace/ID. It is not yet CLI/YAML wiring.
func TestOTLPJournalHTTPAdmissionAndReplay(t *testing.T) {
	for _, signal := range []otlpwire.Signal{otlpwire.Logs, otlpwire.Metrics, otlpwire.Traces} {
		for _, gzip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", signal, gzip), func(t *testing.T) {
				raw, e := os.ReadFile(filepath.Join("../../library/otlpwire/testdata", string(signal)+".json"))
				if e != nil {
					t.Fatal(e)
				}
				var a, b, partial atomic.Int32
				var healthy atomic.Bool
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(payload, raw) {
						t.Error("outgoing payload changed")
					}
					w.Header().Set("Content-Type", otlpwire.JSON)
					switch r.URL.Path {
					case "/a":
						a.Add(1)
					case "/b":
						b.Add(1)
						if !healthy.Load() {
							w.WriteHeader(503)
							return
						}
					case "/c":
						partial.Add(1)
						field := map[otlpwire.Signal]string{otlpwire.Logs: "rejectedLogRecords", otlpwire.Metrics: "rejectedDataPoints", otlpwire.Traces: "rejectedSpans"}[signal]
						fmt.Fprintf(w, `{"partialSuccess":{"%s":"1"}}`, field)
						return
					default:
						t.Error("unexpected path")
					}
					io.WriteString(w, `{}`)
				}))
				defer remote.Close()
				var peers []controller.OTLPDestination
				for _, id := range []string{"a", "b", "c"} {
					ex, err := otlphttp.NewExporter(otlphttp.ExporterConfig{Endpoints: map[otlpwire.Signal]string{signal: remote.URL + "/" + id}, MaxAttempts: 1})
					if err != nil {
						t.Fatal(err)
					}
					defer ex.Close()
					peers = append(peers, controller.OTLPDestination{ID: id, Send: ex.Send})
				}
				cfg := controller.OTLPJournalConfig{Directory: t.TempDir(), Compress: gzip}
				p := lifecycleOpen(t, cfg, peers...)
				ns := p.Namespace()
				h, e := otlphttp.NewReceiver(otlphttp.ReceiverConfig{}, p.Admit)
				if e != nil {
					t.Fatal(e)
				}
				srv := httptest.NewServer(h)
				response, e := http.Post(srv.URL+"/v1/"+string(signal), otlpwire.JSON, bytes.NewReader(raw))
				if e != nil {
					t.Fatal(e)
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				srv.Close()
				if response.StatusCode != 200 || string(body) != "{}" {
					t.Fatal("durable HTTP acceptance failed", response.StatusCode, string(body))
				}
				r, e := lifecycleDrain(t, p)
				if !errors.Is(e, controller.ErrOTLPDeliveryPending) || r.Pending != 1 || r.Released != 0 {
					t.Fatal(r, e)
				}
				if e = p.Close(); e != nil {
					t.Fatal(e)
				}
				healthy.Store(true)
				p = lifecycleOpen(t, cfg, peers...)
				if p.Namespace() != ns {
					t.Fatal("namespace changed")
				}
				r, e = lifecycleDrain(t, p)
				if e != nil || r.Released != 1 || r.Delivered != 0 || r.Quarantined != 1 {
					t.Fatal("quarantine reported delivered", r, e)
				}
				if a.Load() != 1 || b.Load() != 2 || partial.Load() != 1 {
					t.Fatalf("durable peers re-exported: %d %d %d", a.Load(), b.Load(), partial.Load())
				}
				p.Close()
				p = lifecycleOpen(t, cfg, peers...)
				r, e = lifecycleDrain(t, p)
				if e != nil || r.Seen != 0 {
					t.Fatal("released request replayed", r, e)
				}
			})
		}
	}
}
