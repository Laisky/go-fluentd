//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func acceptedStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func acceptedKey() Key { return Key{Destination: "a", Journal: "tenant-generation", RecordID: 7} }
func acceptedEnvelope() Envelope {
	return Envelope{Signal: "metrics", ContentType: "application/json", Items: 2, Payload: []byte(`{"future":"18446744073709551615"}`)}
}
func acceptedOutcome() Outcome {
	return Outcome{Kind: Accepted, HTTPStatus: 200, ResponseContentType: "application/json", Response: []byte(`{}`)}
}

func TestAcceptedReceiptSurvivesReopen(t *testing.T) {
	for _, signal := range []string{"logs", "metrics", "traces"} {
		for _, ct := range []string{"application/json", "application/x-protobuf"} {
			t.Run(signal+"/"+ct, func(t *testing.T) {
				dir := t.TempDir()
				s := acceptedStore(t, dir)
				e := acceptedEnvelope()
				e.Signal, e.ContentType = signal, ct
				if ct == "application/x-protobuf" {
					e.Payload = []byte{0x0a, 0, 0xf8, 0x7f, 1}
				}
				want := bytes.Clone(e.Payload)
				calls := 0
				send := func(_ context.Context, got Envelope) (Outcome, error) {
					calls++
					got.Payload[0] ^= 0xff
					o := acceptedOutcome()
					o.ResponseContentType = ct
					if ct == "application/x-protobuf" {
						o.Response = nil
					}
					return o, nil
				}
				for round := 0; round < 3; round++ {
					r, err := s.DoDelivery(context.Background(), acceptedKey(), e, send)
					if err != nil || !r.Durable || r.Quarantined || r.Outcome.Kind != Accepted || r.Replayed != (round > 0) {
						t.Fatalf("receipt round %d: %+v %v", round, r, err)
					}
					if !bytes.Equal(e.Payload, want) {
						t.Fatal("caller bytes changed")
					}
					if len(r.Outcome.Response) > 0 {
						r.Outcome.Response[0] = '!'
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s = acceptedStore(t, dir)
				}
				if calls != 1 {
					t.Fatalf("accepted destination re-exported: %d", calls)
				}
				e.Payload = append(e.Payload, 'x')
				if _, err := s.DoDelivery(context.Background(), acceptedKey(), e, send); !errors.Is(err, ErrConflict) {
					t.Fatalf("identity conflict accepted: %v", err)
				}
			})
		}
	}
}

func TestAcceptedReceiptConcurrencyAndLegacyPolicy(t *testing.T) {
	s := acceptedStore(t, t.TempDir())
	var calls atomic.Int64
	send := func(context.Context, Envelope) (Outcome, error) { calls.Add(1); return acceptedOutcome(), nil }
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	var fresh atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.DoDelivery(context.Background(), acceptedKey(), acceptedEnvelope(), send)
			if e != nil || !r.Durable || r.Quarantined {
				errs <- errors.New("non-durable acceptance")
			} else if !r.Replayed {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if calls.Load() != 1 || fresh.Load() != 1 {
		t.Fatalf("same identity sent %d times, fresh %d", calls.Load(), fresh.Load())
	}
	k := acceptedKey()
	k.RecordID++
	for i := 0; i < 2; i++ {
		r, e := s.Do(context.Background(), k, acceptedEnvelope(), send)
		if e != nil || r.Durable || r.Replayed || r.Quarantined {
			t.Fatalf("legacy Do policy changed: %+v %v", r, e)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("legacy accepted-only policy changed")
	}
}

func TestAcceptedReceiptWaitsForAllBarriers(t *testing.T) {
	for _, which := range []string{"file", "link", "directory"} {
		t.Run(which, func(t *testing.T) {
			s := acceptedStore(t, t.TempDir())
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			block := func() { close(entered); <-release }
			switch which {
			case "file":
				s.syncRecord = func(f *os.File) error { block(); return f.Sync() }
			case "link":
				s.linkRecord = func(a, b string) error { block(); return os.Link(a, b) }
			case "directory":
				s.syncDirectory = func(f *os.File) error { block(); return f.Sync() }
			}
			done := make(chan error, 1)
			go func() {
				r, e := s.DoDelivery(context.Background(), acceptedKey(), acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) { return acceptedOutcome(), nil })
				if e == nil && (!r.Durable || r.Quarantined) {
					e = errors.New("false acceptance")
				}
				done <- e
			}()
			select {
			case <-entered:
			case e := <-done:
				t.Fatalf("acceptance bypassed %s barrier: %v", which, e)
			case <-time.After(3 * time.Second):
				t.Fatal("barrier not reached")
			}
			select {
			case e := <-done:
				t.Fatalf("acceptance returned before %s barrier: %v", which, e)
			default:
			}
			once.Do(func() { close(release) })
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestAcceptedPersistenceFailureBlocksResend(t *testing.T) {
	for _, which := range []string{"file", "link", "directory"} {
		t.Run(which, func(t *testing.T) {
			dir := t.TempDir()
			s := acceptedStore(t, dir)
			switch which {
			case "file":
				s.syncRecord = func(*os.File) error { return syscall.EIO }
			case "link":
				s.linkRecord = func(string, string) error { return syscall.EIO }
			case "directory":
				s.syncDirectory = func(*os.File) error { return syscall.EIO }
			}
			calls := 0
			send := func(context.Context, Envelope) (Outcome, error) { calls++; return acceptedOutcome(), nil }
			for i := 0; i < 2; i++ {
				r, e := s.DoDelivery(context.Background(), acceptedKey(), acceptedEnvelope(), send)
				if !errors.Is(e, ErrUncertain) || r.Durable || r.Quarantined {
					t.Fatalf("persistence failure acknowledged: %+v %v", r, e)
				}
			}
			s.Close()
			s = acceptedStore(t, dir)
			r, e := s.DoDelivery(context.Background(), acceptedKey(), acceptedEnvelope(), send)
			if which == "directory" {
				if e != nil || !r.Durable || !r.Replayed || r.Quarantined {
					t.Fatalf("surviving acceptance not recovered: %+v %v", r, e)
				}
			} else if !errors.Is(e, ErrUncertain) {
				t.Fatalf("uncertain acceptance resent: %+v %v", r, e)
			}
			if calls != 1 {
				t.Fatalf("failed persistence caused resend: %d", calls)
			}
		})
	}
}

func TestAcceptedResponsePersistsAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	s := acceptedStore(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, e := s.DoDelivery(ctx, acceptedKey(), acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) { cancel(); return acceptedOutcome(), nil })
	if e != nil || !r.Durable || r.Quarantined {
		t.Fatalf("lost response on cancellation: %+v %v", r, e)
	}
	s.Close()
	s = acceptedStore(t, dir)
	r, e = s.DoDelivery(context.Background(), acceptedKey(), acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) {
		t.Error("accepted response resent")
		return acceptedOutcome(), nil
	})
	if e != nil || !r.Replayed || !r.Durable {
		t.Fatalf("missing acceptance: %+v %v", r, e)
	}
}
