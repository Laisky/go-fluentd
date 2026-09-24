//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Deterministic I/O-seam tests supplement public/process tests. The real files
// and record protocol remain in use; only an individual durability step blocks
// or reports EIO. No global hooks, maps or fabricated recovered state are used.
func TestDurabilityBarriersBeforeQuarantineReceipt(t *testing.T) {
	for _, which := range []string{"file-sync", "link", "directory-sync"} {
		t.Run(which, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			entered := make(chan struct{})
			release := make(chan struct{})
			var barrierRan atomic.Bool
			block := func() error { barrierRan.Store(true); close(entered); <-release; return nil }
			switch which {
			case "file-sync":
				s.syncRecord = func(f *os.File) error {
					if err := block(); err != nil {
						return err
					}
					return f.Sync()
				}
			case "link":
				s.linkRecord = func(a, b string) error {
					if err := block(); err != nil {
						return err
					}
					return os.Link(a, b)
				}
			case "directory-sync":
				s.syncDirectory = func(f *os.File) error {
					if err := block(); err != nil {
						return err
					}
					return f.Sync()
				}
			}
			done := make(chan error, 1)
			go func() {
				r, e := s.Do(context.Background(), Key{"peer", "journal", 1}, Envelope{"metrics", "application/json", 1, []byte("{}")}, func(context.Context, Envelope) (Outcome, error) {
					return Outcome{Kind: Partial, HTTPStatus: 200, RejectedItems: 1}, nil
				})
				if e == nil && !r.Quarantined {
					e = errors.New("missing quarantine")
				}
				done <- e
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("returned before %s: %v", which, err)
			case <-time.After(3 * time.Second):
				t.Fatal("barrier not reached")
			}
			select {
			case err := <-done:
				t.Fatalf("returned while %s blocked: %v", which, err)
			default:
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !barrierRan.Load() {
				t.Fatal("barrier bypassed")
			}
		})
	}
}

func TestPersistenceFailureNeverAcknowledgesOrReexports(t *testing.T) {
	for _, which := range []string{"file-sync", "link", "directory-sync"} {
		t.Run(which, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			switch which {
			case "file-sync":
				s.syncRecord = func(*os.File) error { return syscall.EIO }
			case "link":
				s.linkRecord = func(string, string) error { return syscall.EIO }
			case "directory-sync":
				s.syncDirectory = func(*os.File) error { return syscall.EIO }
			}
			calls := 0
			k := Key{"peer", "journal", 1}
			e := Envelope{"logs", "application/json", 1, []byte("{}")}
			send := func(context.Context, Envelope) (Outcome, error) {
				calls++
				return Outcome{Kind: Permanent, HTTPStatus: 400}, nil
			}
			for i := 0; i < 2; i++ {
				r, err := s.Do(context.Background(), k, e, send)
				if !errors.Is(err, ErrUncertain) || r.Quarantined {
					t.Fatalf("false durable result: %+v %v", r, err)
				}
			}
			if calls != 1 {
				t.Fatal("terminal response retried")
			}
			s.Close()
			s, err = Open(dir, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r, err := s.Do(context.Background(), k, e, send)
			if which == "directory-sync" {
				if err != nil || !r.Quarantined || !r.Replayed {
					t.Fatalf("published record not recovered after fresh directory Sync: %+v %v", r, err)
				}
			} else if !errors.Is(err, ErrUncertain) {
				t.Fatalf("orphan pending record treated as retryable: %+v %v", r, err)
			}
			if calls != 1 {
				t.Fatal("unresolved persistence retried after reopen")
			}
		})
	}
}
