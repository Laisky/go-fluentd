package controller

import (
	"context"
	"errors"
	"testing"

	"gofluentd/library"
)

// The opt-in policy follows the measured rollout decision: applications that
// omit the new option keep one successful storage barrier per durable record.
func TestJournalWriterDefaultKeepsPerRecordAcceptance(t *testing.T) {
	j, ctx, _ := testWriter(t)
	j.GroupCommitMaxMessages = 0
	in := make(chan *library.FluentMsg, 17)
	receipts := make([]chan error, cap(in))
	for i := range receipts {
		msg, receipt := writerMessage(int64(i+1), true)
		in <- msg
		receipts[i] = receipt
	}
	close(in)
	store := &writerStore{}
	waitWriter(t, writerRun(j, ctx, store, in))
	for _, receipt := range receipts {
		if err := writerReceipt(t, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if store.syncCalls != len(receipts) {
		t.Fatalf("default changed the per-record barrier policy: %d Sync calls for %d accepted records", store.syncCalls, len(receipts))
	}
}

// Cancelling the worker cannot revoke or invent the result of a kernel Sync
// already in progress. No receipt may escape until that storage call returns.
func TestJournalGroupCancellationDuringSyncKeepsActualResult(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "successful-barrier"
		if fail {
			name = "failed-barrier"
		}
		t.Run(name, func(t *testing.T) {
			j, ctx, cancel := testWriter(t)
			j.GroupCommitMaxMessages = 4
			in := make(chan *library.FluentMsg, 4)
			receipts := make([]chan error, 4)
			for i := range receipts {
				msg, receipt := writerMessage(int64(i+1), true)
				in <- msg
				receipts[i] = receipt
			}
			close(in)
			entered, release := make(chan struct{}), make(chan struct{})
			want := errors.New("sync refused after cancellation")
			store := &writerStore{syncHook: func([]int64) error {
				close(entered)
				<-release
				if fail {
					return want
				}
				return nil
			}}
			done := writerRun(j, ctx, store, in)
			waitWriter(t, entered)
			cancel()
			for _, receipt := range receipts {
				if len(receipt) != 0 {
					t.Error("cancellation completed a receipt before Sync returned")
				}
			}
			close(release)
			waitWriter(t, done)
			for i, receipt := range receipts {
				err := writerReceipt(t, receipt)
				if fail && !errors.Is(err, want) || !fail && err != nil {
					t.Fatalf("receipt %d replaced the actual storage result: %v", i, err)
				}
				if store.durable[int64(i+1)] == fail {
					t.Fatal("receipt does not match durable storage")
				}
				if errors.Is(err, context.Canceled) {
					t.Fatal("cancellation obscured a completed storage result")
				}
			}
			if fail && len(j.outChan) != 0 || !fail && len(j.outChan) != 4 {
				t.Fatal("publication does not match the storage result")
			}
		})
	}
}
