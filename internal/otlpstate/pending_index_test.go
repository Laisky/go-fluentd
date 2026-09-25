//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gofluentd/internal/otlpstate"
)

func TestPendingReceiptInventoryPreservesRecoveryContract(t *testing.T) {
	for _, suffix := range []string{"old-random-12345", ""} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			s := open(t, dir)
			send := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				return terminal(otlpstate.Partial), nil
			}
			if _, err := s.Do(context.Background(), key(), envelope(), send); err != nil {
				t.Fatal(err)
			}
			original := onlyReceipt(t, dir)
			s.Close()
			name := strings.TrimSuffix(filepath.Base(original), ".json")
			pending := filepath.Join(dir, ".pending-"+name+"-"+suffix)
			if err := os.Rename(original, pending); err != nil {
				t.Fatal(err)
			}
			// Simulate a large retained history without relying on its internal decoder.
			for i := 0; i < 600; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("retained-%04d.json", i)), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for restart := 0; restart < 3; restart++ {
				s = open(t, dir)
				calls := 0
				_, err := s.Do(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
					calls++
					return terminal(otlpstate.Partial), nil
				})
				if !errors.Is(err, otlpstate.ErrUncertain) || calls != 0 {
					t.Fatalf("uncertain receipt sent: %v calls=%d", err, calls)
				}
				other := key()
				other.RecordID += int64(restart) + 1
				if _, err = s.Do(context.Background(), other, envelope(), send); err != nil {
					t.Fatal("unrelated destination blocked", err)
				}
				s.Close()
			}
		})
	}
}
func TestCommittedReceiptWinsOverRedundantTemporaryLink(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, e := s.DoDelivery(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	}); e != nil {
		t.Fatal(e)
	}
	original := onlyReceipt(t, dir)
	s.Close()
	pending := filepath.Join(dir, ".pending-"+strings.TrimSuffix(filepath.Base(original), ".json")+"-redundant")
	if e := os.Link(original, pending); e != nil {
		t.Fatal(e)
	}
	s = open(t, dir)
	got, e := s.DoDelivery(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("durable acceptance resent")
		return otlpstate.Outcome{}, nil
	})
	if e != nil || !got.Durable || !got.Replayed {
		t.Fatal(got, e)
	}
}
func TestPendingInventoryRefusesReplacedDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "state")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s := open(t, dir)
	if e := os.Rename(dir, dir+"-preserved"); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	_, e := s.DoDelivery(context.Background(), key(), envelope(), func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		t.Error("sent using stale directory inventory")
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	})
	if !errors.Is(e, otlpstate.ErrUncertain) {
		t.Fatal(e)
	}
}
