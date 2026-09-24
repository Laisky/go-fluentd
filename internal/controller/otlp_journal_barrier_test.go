//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

func journalBarrierRequest(t *testing.T) *otlpwire.Request {
	t.Helper()
	r, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewBufferString(`{"resourceLogs":[],"future":"retained"}`), otlpwire.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func journalBarrierOwner(t *testing.T, dir string, send func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error)) *OTLPJournal {
	t.Helper()
	p, e := OpenOTLPJournal(context.Background(), OTLPJournalConfig{Directory: dir}, []OTLPDestination{{ID: "a", Send: send}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func barrierAccepted(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
	return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
}

func TestOTLPJournalAdmissionWaitsForSync(t *testing.T) {
	dir := t.TempDir()
	p := journalBarrierOwner(t, dir, barrierAccepted)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	real := p.syncWAL
	p.syncWAL = func() error { close(entered); <-release; return real() }
	done := make(chan error, 1)
	req := journalBarrierRequest(t)
	go func() { done <- p.Admit(context.Background(), req) }()
	select {
	case <-entered:
	case e := <-done:
		t.Fatalf("admission completed before Sync barrier: %v", e)
	case <-time.After(3 * time.Second):
		t.Fatal("no admission progress")
	}
	select {
	case e := <-done:
		t.Fatalf("admission escaped blocked Sync: %v", e)
	default:
	}
	// A second caller must be able to cancel while the first owns disk admission.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := p.Admit(ctx, req); !errors.Is(e, context.Canceled) {
		t.Fatal("queued cancellation ignored", e)
	}
	release <- struct{}{}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
	q := journalBarrierOwner(t, dir, barrierAccepted)
	r, e := q.ReplayBatch(context.Background())
	if e != nil || r.Delivered != 1 {
		t.Fatal("synchronized admission missing after restart", r, e)
	}
}
func TestOTLPJournalReplayCopyWaitsForSync(t *testing.T) {
	dir := t.TempDir()
	p := journalBarrierOwner(t, dir, barrierAccepted)
	if e := p.Admit(context.Background(), journalBarrierRequest(t)); e != nil {
		t.Fatal(e)
	}
	p.Close()
	called := make(chan struct{}, 1)
	q := journalBarrierOwner(t, dir, func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		called <- struct{}{}
		return barrierAccepted(ctx, e)
	})
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	real := q.syncWAL
	q.syncWAL = func() error {
		select {
		case <-entered:
		default:
			close(entered)
			<-release
		}
		return real()
	}
	done := make(chan error, 1)
	go func() { _, e := q.ReplayBatch(context.Background()); done <- e }()
	select {
	case <-entered:
	case <-called:
		t.Fatal("network send preceded durable replay copy")
	case e := <-done:
		t.Fatal("replay skipped copy barrier", e)
	case <-time.After(3 * time.Second):
		t.Fatal("no replay progress")
	}
	select {
	case <-called:
		t.Fatal("network send escaped copy Sync")
	default:
	}
	release <- struct{}{}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestOTLPJournalStorageFailureStopsFurtherAdmission(t *testing.T) {
	for _, operation := range []string{"append", "sync"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			p := journalBarrierOwner(t, dir, barrierAccepted)
			failure := errors.New("injected disk failure after append")
			calls := 0
			realWrite := p.writeWAL
			realSync := p.syncWAL
			if operation == "append" {
				p.writeWAL = func(d *journal.Data) error {
					calls++
					if e := realWrite(d); e != nil {
						return e
					}
					return failure
				}
			} else {
				p.syncWAL = func() error {
					calls++
					if e := realSync(); e != nil {
						return e
					}
					return failure
				}
			}
			if e := p.Admit(context.Background(), journalBarrierRequest(t)); !errors.Is(e, failure) || !errors.Is(e, ErrOTLPJournalFault) {
				t.Fatal("storage failure acknowledged", e)
			}
			if e := p.Admit(context.Background(), journalBarrierRequest(t)); !errors.Is(e, ErrOTLPJournalFault) || calls != 1 {
				t.Fatal("faulted journal kept admitting", e, calls)
			}
			if _, e := p.ReplayBatch(context.Background()); !errors.Is(e, ErrOTLPJournalFault) {
				t.Fatal("faulted journal kept exporting", e)
			}
			p.Close()
			q := journalBarrierOwner(t, dir, barrierAccepted)
			r, e := q.ReplayBatch(context.Background())
			if e != nil || r.Delivered != 1 {
				t.Fatal("unknown-outcome complete record was lost", r, e)
			}
		})
	}
}
func TestOTLPJournalReplayCopyFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	p := journalBarrierOwner(t, dir, barrierAccepted)
	p.Admit(context.Background(), journalBarrierRequest(t))
	p.Close()
	calls := 0
	q := journalBarrierOwner(t, dir, func(c context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls++
		return barrierAccepted(c, e)
	})
	failure := errors.New("replacement write failed")
	q.writeWAL = func(*journal.Data) error { return failure }
	if _, e := q.ReplayBatch(context.Background()); !errors.Is(e, failure) || calls != 0 {
		t.Fatal("failed replay copy was exported", e, calls)
	}
	q.Close()
	q = journalBarrierOwner(t, dir, barrierAccepted)
	r, e := q.ReplayBatch(context.Background())
	if e != nil || r.Delivered != 1 {
		t.Fatal("failed replacement deleted sole durable copy", r, e)
	}
}
