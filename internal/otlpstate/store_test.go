//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/otlpstate"
)

func key() otlpstate.Key {
	return otlpstate.Key{Destination: "collector-a", Journal: "otlp.metrics", RecordID: 42}
}
func envelope() otlpstate.Envelope {
	return otlpstate.Envelope{Signal: "metrics", ContentType: "application/json", Items: 2, Payload: []byte(`{"resourceMetrics":[],"future":"18446744073709551615"}`)}
}
func terminal(kind otlpstate.Kind) otlpstate.Outcome {
	o := otlpstate.Outcome{Kind: kind, HTTPStatus: 400, Diagnostic: "peer rejected é\ncheck schema", ResponseContentType: "application/json", Response: []byte(`{"message":"invalid"}`)}
	if kind == otlpstate.Partial {
		o.HTTPStatus = 200
		o.RejectedItems = 1
		o.Response = []byte(`{"partialSuccess":{"rejectedDataPoints":"1"}}`)
	}
	if kind == otlpstate.Invalid {
		o.HTTPStatus = 200
		o.Response = []byte{0xff, 0x00}
	}
	return o
}
func open(t *testing.T, dir string) *otlpstate.Store {
	t.Helper()
	s, err := otlpstate.Open(dir, otlpstate.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func onlyReceipt(t *testing.T, dir string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("receipts=%v err=%v", paths, err)
	}
	return paths[0]
}

func TestTerminalSurvivesReopenWithoutCallingDestination(t *testing.T) {
	for _, signal := range []string{"logs", "metrics", "traces"} {
		for _, ct := range []string{"application/json", "application/x-protobuf"} {
			for _, kind := range []otlpstate.Kind{otlpstate.Partial, otlpstate.Permanent, otlpstate.Invalid} {
				t.Run(fmt.Sprintf("%s/%s/%s", signal, ct, kind), func(t *testing.T) {
					dir := t.TempDir()
					s := open(t, dir)
					e := envelope()
					e.Signal = signal
					e.ContentType = ct
					if ct == "application/x-protobuf" {
						e.Payload = []byte{0x0a, 0x00, 0xf8, 0x7f, 0x01}
					}
					want := terminal(kind)
					original := bytes.Clone(e.Payload)
					calls := 0
					r, err := s.Do(context.Background(), key(), e, func(_ context.Context, copy otlpstate.Envelope) (otlpstate.Outcome, error) {
						calls++
						copy.Payload[0] ^= 0xff
						return want, errors.New("optional response decode detail")
					})
					if err != nil || !r.Quarantined || r.Replayed || r.Outcome.Kind == otlpstate.Accepted || !reflect.DeepEqual(r.Outcome, want) {
						t.Fatalf("first=%+v %v", r, err)
					}
					if !bytes.Equal(e.Payload, original) {
						t.Fatal("callback changed caller payload")
					}
					saved := bytes.Clone(want.Response)
					r.Outcome.Response[0] ^= 0xff
					path := onlyReceipt(t, dir)
					st, err := os.Stat(path)
					if err != nil || st.Mode().Perm() != 0600 {
						t.Fatalf("unsafe permissions: %v %v", st, err)
					}
					for round := 0; round < 2; round++ {
						if err := s.Close(); err != nil {
							t.Fatal(err)
						}
						s = open(t, dir)
						got, err := s.Do(context.Background(), key(), e, func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
							calls++
							return terminal(kind), nil
						})
						if err != nil || !got.Quarantined || !got.Replayed || !bytes.Equal(got.Outcome.Response, saved) || got.Outcome.Kind != kind {
							t.Fatalf("replay=%+v %v", got, err)
						}
					}
					if calls != 1 {
						t.Fatalf("terminal export retried %d times", calls)
					}
				})
			}
		}
	}
}

func TestDestinationsAndNonterminalOutcomesStayIndependent(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	counts := map[string]int{}
	for round := 0; round < 2; round++ {
		if round > 0 {
			s.Close()
			s = open(t, dir)
		}
		for _, name := range []string{"partial", "retry", "accepted"} {
			k := key()
			k.Destination = name
			r, err := s.Do(context.Background(), k, envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				counts[name]++
				if name == "partial" {
					return terminal(otlpstate.Partial), nil
				}
				if name == "retry" {
					return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
				}
				return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if r.Quarantined != (name == "partial") || (r.Replayed != (round == 1 && name == "partial")) {
				t.Fatalf("incorrect classification: %+v", r)
			}
		}
	}
	if counts["partial"] != 1 || counts["retry"] != 2 || counts["accepted"] != 2 {
		t.Fatal(counts)
	}
	onlyReceipt(t, dir)
}

func TestIdentityConflictNeverSendsOrOverwrites(t *testing.T) {
	for _, change := range []string{"payload", "signal", "content-type", "items"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			s := open(t, dir)
			e := envelope()
			_, err := s.Do(context.Background(), key(), e, func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				return terminal(otlpstate.Permanent), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			path := onlyReceipt(t, dir)
			before, _ := os.ReadFile(path)
			switch change {
			case "payload":
				e.Payload = []byte("different")
			case "signal":
				e.Signal = "logs"
			case "content-type":
				e.ContentType = "application/x-protobuf"
			case "items":
				e.Items++
			}
			_, err = s.Do(context.Background(), key(), e, func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				t.Error("conflicting identity exported")
				return terminal(otlpstate.Permanent), nil
			})
			after, _ := os.ReadFile(path)
			if !errors.Is(err, otlpstate.ErrConflict) || !bytes.Equal(before, after) {
				t.Fatalf("conflict lost: %v", err)
			}
		})
	}
	// Journal and destination names may contain separators without key collisions.
	s := open(t, t.TempDir())
	k1 := key()
	k1.Destination = "a/b"
	k1.Journal = "c"
	k2 := key()
	k2.Destination = "a"
	k2.Journal = "b/c"
	for _, k := range []otlpstate.Key{k1, k2} {
		r, e := s.Do(context.Background(), k, envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
			return terminal(otlpstate.Permanent), nil
		})
		if e != nil || r.Replayed {
			t.Fatalf("key collision: %+v %v", r, e)
		}
	}
}

func TestConcurrentReplaysCallDestinationOnce(t *testing.T) {
	s := open(t, t.TempDir())
	var calls atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, e := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				calls.Add(1)
				return terminal(otlpstate.Partial), nil
			})
			if e != nil || !r.Quarantined {
				t.Errorf("concurrent receipt: %+v %v", r, e)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestCancellationAndClose(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.Do(ctx, key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
			close(entered)
			<-release
			cancel()
			return terminal(otlpstate.Partial), nil
		})
		done <- err
	}()
	<-entered
	blockedCtx, blockedCancel := context.WithCancel(context.Background())
	blockedCancel()
	if _, err := s.Do(blockedCtx, key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("canceled send")
		return terminal(otlpstate.Partial), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	closing := make(chan error, 1)
	go func() { closing <- s.Close() }()
	select {
	case <-closing:
		t.Fatal("Close released ownership while send running")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("known terminal outcome lost on cancellation: %v", err)
	}
	if err := <-closing; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("closed send")
		return terminal(otlpstate.Partial), nil
	}); !errors.Is(err, otlpstate.ErrClosed) {
		t.Fatal(err)
	}
	s = open(t, dir)
	r, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("canceled terminal retried")
		return terminal(otlpstate.Partial), nil
	})
	if err != nil || !r.Replayed {
		t.Fatalf("reopen=%+v %v", r, err)
	}
}

func TestCorruptionAndStorageErrorsAreNotCacheMisses(t *testing.T) {
	for _, damage := range []string{"truncate", "checksum", "directory", "symlink", "oversize"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			s := open(t, dir)
			if _, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				return terminal(otlpstate.Permanent), nil
			}); err != nil {
				t.Fatal(err)
			}
			path := onlyReceipt(t, dir)
			s.Close()
			before, _ := os.ReadFile(path)
			switch damage {
			case "truncate":
				os.WriteFile(path, before[:len(before)/2], 0600)
			case "checksum":
				before[12] ^= 1
				os.WriteFile(path, before, 0600)
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0700)
			case "symlink":
				os.Remove(path)
				os.Symlink("/dev/null", path)
			case "oversize":
				os.Truncate(path, 20<<20)
			}
			s = open(t, dir)
			_, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				t.Error("corrupt receipt treated as absent")
				return terminal(otlpstate.Permanent), nil
			})
			if !errors.Is(err, otlpstate.ErrCorrupt) {
				t.Fatalf("expected corruption: %v", err)
			}
		})
	}
	t.Run("directory disappears after peer response", func(t *testing.T) {
		dir := t.TempDir()
		s := open(t, dir)
		calls := 0
		f := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
			calls++
			if err := os.Rename(dir, dir+".offline"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir + ".offline") })
			return terminal(otlpstate.Partial), nil
		}
		r, err := s.Do(context.Background(), key(), envelope(), f)
		if !errors.Is(err, otlpstate.ErrUncertain) || r.Quarantined {
			t.Fatalf("false receipt: %+v %v", r, err)
		}
		if _, err := s.Do(context.Background(), key(), envelope(), f); !errors.Is(err, otlpstate.ErrUncertain) {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatal("known response retried after storage error")
		}
	})
}

func TestValidationAndDirectoryOwnership(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if other, err := otlpstate.Open(dir, otlpstate.DefaultLimits()); err == nil {
		other.Close()
		t.Fatal("second owner admitted")
	}
	lockPath := filepath.Join(dir, ".otlp-disposition.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, dir)
	after, _ := os.Stat(lockPath)
	if !os.SameFile(before, after) {
		t.Fatal("lock inode changed")
	}
	tests := []struct {
		name string
		k    otlpstate.Key
		e    otlpstate.Envelope
	}{
		{"empty destination", otlpstate.Key{Journal: "a"}, envelope()},
		{"negative id", otlpstate.Key{Destination: "a", Journal: "b", RecordID: -1}, envelope()},
		{"signal", key(), otlpstate.Envelope{Signal: "profiles", ContentType: "application/json"}},
		{"content type", key(), otlpstate.Envelope{Signal: "logs", ContentType: "text/plain"}},
		{"oversize", key(), otlpstate.Envelope{Signal: "logs", ContentType: "application/json", Payload: make([]byte, (4<<20)+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Do(context.Background(), tt.k, tt.e, func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				t.Error("invalid request sent")
				return terminal(otlpstate.Permanent), nil
			})
			if err == nil {
				t.Fatal("accepted invalid arguments")
			}
		})
	}
	if _, err := s.Do(nil, key(), envelope(), nil); err == nil {
		t.Fatal("nil arguments")
	}
	for _, o := range []otlpstate.Outcome{{Kind: otlpstate.Partial, HTTPStatus: 200}, {Kind: otlpstate.Partial, HTTPStatus: 200, RejectedItems: 3}, {Kind: otlpstate.Accepted, HTTPStatus: 204}, {Kind: "unknown"}} {
		ss := open(t, t.TempDir())
		calls := 0
		send := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) { calls++; return o, nil }
		if r, err := ss.Do(context.Background(), key(), envelope(), send); err == nil || r.Quarantined {
			t.Fatalf("bad outcome accepted: %+v %v", r, err)
		}
		ss.Do(context.Background(), key(), envelope(), send)
		if calls != 1 {
			t.Fatal("invalid classifier response retried")
		}
	}
}

func TestTransportFailuresRemainRetryable(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	networkErr := errors.New("connection closed before response")
	if r, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{}, networkErr
	}); !errors.Is(err, networkErr) || r.Quarantined {
		t.Fatalf("transport error: %+v %v", r, err)
	}
	r, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	})
	if err != nil || r.Quarantined || r.Replayed || r.Outcome.Kind != otlpstate.Accepted {
		t.Fatalf("healthy retry: %+v %v", r, err)
	}
	receipts, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(receipts) != 0 {
		t.Fatal("acceptance incorrectly stored as rejection")
	}
}

func TestEmptyProtobufBytesAreOneIdentity(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	e := otlpstate.Envelope{Signal: "logs", ContentType: "application/x-protobuf", Payload: []byte{}}
	send := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return terminal(otlpstate.Permanent), nil
	}
	if _, err := s.Do(context.Background(), key(), e, send); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, dir)
	e.Payload = nil
	r, err := s.Do(context.Background(), key(), e, func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("empty request repeated")
		return terminal(otlpstate.Permanent), nil
	})
	if err != nil || !r.Replayed {
		t.Fatalf("nil and empty bytes have the same wire identity: %+v %v", r, err)
	}
	if s, err := otlpstate.Open("", otlpstate.DefaultLimits()); err == nil {
		s.Close()
		t.Fatal("empty directory accepted")
	}
}
