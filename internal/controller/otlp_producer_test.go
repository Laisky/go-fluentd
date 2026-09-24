//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/controller"
	"gofluentd/internal/otlpstate"
)

func producerStore(t *testing.T, dir string) *otlpstate.Store {
	t.Helper()
	s, e := otlpstate.Open(dir, otlpstate.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func producerNew(t *testing.T, s *otlpstate.Store, ds ...controller.OTLPDestination) *controller.OTLPProducer {
	t.Helper()
	p, e := controller.NewOTLPProducer(s, ds)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func producerEnvelope() otlpstate.Envelope {
	return otlpstate.Envelope{Signal: "metrics", ContentType: "application/json", Items: 2, Payload: []byte(`{"resourceMetrics":[],"future":"18446744073709551615","label":"é🙂"}`)}
}
func producerAccepted() otlpstate.Outcome {
	return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200, ResponseContentType: "application/json", Response: []byte(`{}`)}
}
func producerJournal(t *testing.T, dir string, gzip bool) *journal.Journal {
	t.Helper()
	j, e := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithIsCompress(gzip), journal.WithIsAggresiveGC(false), journal.WithFlushInterval(time.Hour), journal.WithRotateDuration(time.Hour), journal.WithRotateCheckInterval(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(j.Close)
	return j
}
func replayPlan(t *testing.T, j *journal.Journal) controller.OTLPRecord {
	t.Helper()
	if !j.LockLegacy() {
		t.Fatal("no replay lease")
	}
	d := new(journal.Data)
	if e := j.LoadLegacyBuf(d); e != nil {
		t.Fatal(e)
	}
	r, e := controller.OTLPRecordFromJournal(d)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.WriteData(d); e != nil {
		t.Fatal(e)
	}
	if e = j.LoadLegacyBuf(new(journal.Data)); e != io.EOF {
		t.Fatalf("replay cleanup: %v", e)
	}
	return r
}

func TestOTLPProducerMixedOutcomesAndJournalRestart(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(fmt.Sprint(gzip), func(t *testing.T) {
			dir, wal := t.TempDir(), t.TempDir()
			var a, b, c atomic.Int64
			peers := []controller.OTLPDestination{
				{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
					a.Add(1)
					return producerAccepted(), nil
				}},
				{ID: "b", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
					if b.Add(1) == 1 {
						return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
					}
					return producerAccepted(), nil
				}},
				{ID: "c", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
					c.Add(1)
					return otlpstate.Outcome{Kind: otlpstate.Partial, HTTPStatus: 200, RejectedItems: 1, Response: []byte(`{"partialSuccess":{"rejectedDataPoints":"1"}}`)}, nil
				}},
			}
			s := producerStore(t, dir)
			p := producerNew(t, s, peers...)
			r, e := p.Plan("tenant1/generation1", 42, producerEnvelope())
			if e != nil {
				t.Fatal(e)
			}
			d, e := r.JournalData()
			if e != nil {
				t.Fatal(e)
			}
			j := producerJournal(t, wal, gzip)
			if e = j.WriteData(d); e != nil {
				t.Fatal(e)
			}
			if e = j.Sync(); e != nil {
				t.Fatal(e)
			}
			releases := 0
			release := func(context.Context, int64) error { releases++; return nil }
			report, e := p.Process(context.Background(), r, release)
			if !errors.Is(e, controller.ErrOTLPDeliveryPending) || report.Resolved || report.FullyDelivered || report.JournalReleased || releases != 0 {
				t.Fatalf("pending destination released WAL: %+v %v calls=%d", report, e, releases)
			}
			if got := p.Counters(); got.Accepted != 1 || got.Quarantined != 1 || got.Retryable != 1 {
				t.Fatalf("incorrect mixed counters: %+v", got)
			}
			if a.Load() != 1 || b.Load() != 1 || c.Load() != 1 {
				t.Fatal("destinations not processed independently")
			}
			j.Close()
			s.Close()
			// Both durable accepted and terminal receipts must survive while another
			// destination retries. A failed final WAL release must not contact them.
			for round := 0; round < 2; round++ {
				s = producerStore(t, dir)
				p = producerNew(t, s, peers...)
				j = producerJournal(t, wal, gzip)
				saved := replayPlan(t, j)
				if !reflect.DeepEqual(saved, r) {
					t.Fatal("journal changed immutable plan/envelope")
				}
				failure := errors.New("release storage failure")
				report, e = p.Process(context.Background(), saved, func(_ context.Context, id int64) error {
					releases++
					if round == 0 {
						return failure
					}
					if id != 42 {
						t.Fatal("wrong release identity")
					}
					if e := j.WriteId(id); e != nil {
						return e
					}
					return j.Sync()
				})
				if !report.Resolved || report.FullyDelivered || report.JournalReleased != (round == 1) {
					t.Fatalf("quarantine counted as delivery or incomplete release: %+v", report)
				}
				if (round == 0 && !errors.Is(e, failure)) || (round == 1 && e != nil) {
					t.Fatalf("release error: %v", e)
				}
				got := p.Counters()
				if got.Quarantined != 0 || got.Accepted != uint64(1-round) || got.ReplayHits != uint64(2+round) {
					t.Fatalf("replayed receipts inflate counters: %+v", got)
				}
				if a.Load() != 1 || b.Load() != 2 || c.Load() != 1 {
					t.Fatalf("resolved destination resent: %d/%d/%d", a.Load(), b.Load(), c.Load())
				}
				j.Close()
				s.Close()
			}
			j = producerJournal(t, wal, gzip)
			if !j.LockLegacy() {
				t.Fatal("no lease")
			}
			if e = j.LoadLegacyBuf(new(journal.Data)); e != io.EOF {
				t.Fatalf("released plan replayed: %v", e)
			}
		})
	}
}

func TestOTLPProducerFullAcceptanceAndReleaseRetry(t *testing.T) {
	s := producerStore(t, t.TempDir())
	var calls atomic.Int64
	p := producerNew(t, s, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		return producerAccepted(), nil
	}})
	r, e := p.Plan("wal", 1, producerEnvelope())
	if e != nil {
		t.Fatal(e)
	}
	failure := errors.New("local ACK not synced")
	for round := 0; round < 2; round++ {
		report, e := p.Process(context.Background(), r, func(context.Context, int64) error {
			if round == 0 {
				return failure
			}
			return nil
		})
		if !report.Resolved || !report.FullyDelivered || report.JournalReleased != (round == 1) {
			t.Fatalf("full result: %+v", report)
		}
		if round == 0 && !errors.Is(e, failure) || round == 1 && e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 1 || p.Counters().Accepted != 1 || p.Counters().ReplayHits != 1 {
		t.Fatalf("release retry resent accepted request: calls=%d counters=%+v", calls.Load(), p.Counters())
	}
}

func TestOTLPProducerAdmissionPlanAndValidation(t *testing.T) {
	s := producerStore(t, t.TempDir())
	calls := 0
	send := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls++
		return producerAccepted(), nil
	}
	ds := []controller.OTLPDestination{{ID: "b", Send: send}, {ID: "a", Send: send}}
	p := producerNew(t, s, ds...)
	env := producerEnvelope()
	r, e := p.Plan("wal", 0, env)
	if e != nil {
		t.Fatal(e)
	}
	env.Payload[0] = '!'
	ds[0].ID = "replaced"
	if !bytes.Equal(r.Envelope.Payload, producerEnvelope().Payload) || !reflect.DeepEqual(r.Required, []string{"a", "b"}) {
		t.Fatal("plan aliases caller configuration")
	}
	// Missing a previously required destination is NOT a new one-peer plan.
	missing := producerNew(t, s, controller.OTLPDestination{ID: "a", Send: send})
	if _, e := missing.Process(context.Background(), r, func(context.Context, int64) error { t.Error("released missing destination"); return nil }); e == nil || calls != 0 {
		t.Fatal("configuration removal silently dropped obligation")
	}
	// A newly configured peer must not receive a previously admitted record.
	larger := producerNew(t, s, controller.OTLPDestination{ID: "a", Send: send}, controller.OTLPDestination{ID: "b", Send: send}, controller.OTLPDestination{ID: "c", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("retroactively added destination")
		return producerAccepted(), nil
	}})
	report, e := larger.Process(context.Background(), r, func(context.Context, int64) error { return nil })
	if e != nil || !report.FullyDelivered || !report.JournalReleased || calls != 2 {
		t.Fatalf("frozen plan: %+v %v", report, e)
	}
	for _, bad := range []controller.OTLPRecord{{Namespace: "wal", Required: nil}, {Namespace: "wal", Required: []string{"a", "a"}}, {Namespace: "wal", Required: []string{"a", "missing"}, Envelope: producerEnvelope()}, {Namespace: "wal", ID: -1, Required: []string{"a"}, Envelope: producerEnvelope()}, {Namespace: "wal", Required: []string{"a"}, Envelope: otlpstate.Envelope{Signal: "bad"}}} {
		if _, e := p.Process(context.Background(), bad, func(context.Context, int64) error { t.Error("invalid plan released"); return nil }); e == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	if calls != 2 {
		t.Fatal("validation touched network")
	}
	if _, e = p.Process(nil, r, func(context.Context, int64) error { return nil }); e == nil {
		t.Fatal("nil context accepted")
	}
	if _, e = p.Process(context.Background(), r, nil); e == nil {
		t.Fatal("nil release accepted")
	}
	if _, e = controller.NewOTLPProducer(nil, ds); e == nil {
		t.Fatal("nil store accepted")
	}
	if _, e = controller.NewOTLPProducer(s, nil); e == nil {
		t.Fatal("empty destinations accepted")
	}
	for _, bad := range [][]controller.OTLPDestination{{{ID: "a"}}, {{ID: "a", Send: send}, {ID: "a", Send: send}}, {{ID: "\n", Send: send}}} {
		if _, e = controller.NewOTLPProducer(s, bad); e == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestOTLPProducerFailureNeverReleasesOrCountsDelivery(t *testing.T) {
	for _, kind := range []otlpstate.Kind{otlpstate.Accepted, otlpstate.Partial} {
		t.Run(string(kind), func(t *testing.T) {
			dir := t.TempDir()
			s := producerStore(t, dir)
			calls := 0
			p := producerNew(t, s, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				calls++
				// Fail persistence AFTER obtaining a peer result. Failure before
				// lookup would correctly prevent sending altogether, not model
				// the post-response uncertainty boundary tested here.
				if e := os.Rename(dir, dir+"-saved"); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { os.RemoveAll(dir + "-saved") })
				if e := os.WriteFile(dir, []byte("blocked"), 0600); e != nil {
					t.Fatal(e)
				}
				if kind == otlpstate.Partial {
					return otlpstate.Outcome{Kind: kind, HTTPStatus: 200, RejectedItems: 1}, nil
				}
				return producerAccepted(), nil
			}})
			r, e := p.Plan("wal", 3, producerEnvelope())
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 2; i++ {
				report, e := p.Process(context.Background(), r, func(context.Context, int64) error { t.Error("released without durable disposition"); return nil })
				if !errors.Is(e, otlpstate.ErrUncertain) || report.Resolved || report.JournalReleased || report.FullyDelivered {
					t.Fatalf("storage failure acknowledged: %+v %v", report, e)
				}
			}
			if got := p.Counters(); got.Accepted != 0 || got.Quarantined != 0 || got.Blocked != 2 || calls != 1 {
				t.Fatalf("storage failure counted or resent: %+v calls=%d", got, calls)
			}
		})
	}
}

func TestOTLPProducerConcurrentReplayCountsOnce(t *testing.T) {
	s := producerStore(t, t.TempDir())
	var calls atomic.Int64
	p := producerNew(t, s, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		return producerAccepted(), nil
	}})
	r, e := p.Plan("wal", 9, producerEnvelope())
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report, e := p.Process(context.Background(), r, func(context.Context, int64) error { return nil })
			if e != nil || !report.FullyDelivered || !report.JournalReleased {
				t.Errorf("concurrent pass: %+v %v", report, e)
			}
		}()
	}
	wg.Wait()
	if got := p.Counters(); got.Accepted != 1 || got.ReplayHits != 31 || calls.Load() != 1 {
		t.Fatalf("concurrent replay inflated outcomes: %+v calls=%d", got, calls.Load())
	}
}

func TestOTLPProducerWrapperRejectsMalformedIdentity(t *testing.T) {
	for _, d := range []*journal.Data{nil, {ID: 1, Data: map[string]interface{}{}}, {ID: 1, Data: map[string]interface{}{"otlp_delivery": "bad"}}, {ID: 1, Data: map[string]interface{}{"otlp_delivery": []byte(`{"version":99}`)}}, {ID: 1, Data: map[string]interface{}{"otlp_delivery": []byte(`{broken`)}}} {
		if _, e := controller.OTLPRecordFromJournal(d); e == nil {
			t.Fatal("malformed wrapper accepted")
		}
	}
	s := producerStore(t, t.TempDir())
	p := producerNew(t, s, controller.OTLPDestination{ID: "a", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) { return producerAccepted(), nil }})
	r, e := p.Plan("wal", 1, producerEnvelope())
	if e != nil {
		t.Fatal(e)
	}
	d, e := r.JournalData()
	if e != nil {
		t.Fatal(e)
	}
	d.ID = 2
	if _, e = controller.OTLPRecordFromJournal(d); e == nil {
		t.Fatal("outer/inner identity collision accepted")
	}
}
