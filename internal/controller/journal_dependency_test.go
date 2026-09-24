package controller

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"

	"gofluentd/library"
)

// These are consumer contracts: real controller writes/receipts and replay use
// the selected journal module, not a mock or a copy of the dependency's code.
func upgradeBackend(t *testing.T, dir string, compressed bool) *journal.Journal {
	t.Helper()
	j, err := journal.NewJournal(
		journal.WithBufDirPath(dir), journal.WithIsCompress(compressed),
		journal.WithBufSizeByte(1<<20), journal.WithIsAggresiveGC(false),
		journal.WithFlushInterval(time.Hour), journal.WithRotateDuration(time.Hour),
		journal.WithRotateCheckInterval(time.Hour), journal.WithCommitIDTTL(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Start(context.Background()); err != nil {
		j.Close()
		t.Fatal(err)
	}
	t.Cleanup(j.Close)
	return j
}

func upgradeController(backend *journal.Journal, groupSize int) *Journal {
	j := &Journal{
		JournalCfg: &JournalCfg{
			GroupCommitMaxMessages: groupSize,
			MsgPool:                &sync.Pool{New: func() interface{} { return new(library.FluentMsg) }},
		},
		outChan:    make(chan *library.FluentMsg, 16),
		legacyLock: utils.NewMutex(),
		tag2JMap:   &sync.Map{},
	}
	j.tag2JMap.Store("source", backend)
	return j
}

func upgradePayload(id int64) map[string]interface{} {
	return map[string]interface{}{
		"event":  fmt.Sprintf("event-%d", id),
		"text":   "hello\x00世界 / café",
		"nested": map[string]interface{}{"ok": true, "value": int64(73)},
	}
}

func TestRegressionJournalUpgradeRejectedPayloadRecovery(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, groupSize := range []int{1, 64} {
			t.Run(fmt.Sprintf("gzip=%v/group=%d", compressed, groupSize), func(t *testing.T) {
				dir := t.TempDir()
				backend := upgradeBackend(t, dir, compressed)
				controller := upgradeController(backend, groupSize)
				// Submit separate batches to distinguish an expected failed batch
				// from a later successful receipt on the same live storage stream.
				for _, attempt := range []struct {
					id      int64
					invalid bool
				}{
					{1, false}, {2, true}, {3, false}, {2, false},
				} {
					receipt := make(chan error, 2)
					payload := upgradePayload(attempt.id)
					if attempt.invalid {
						payload["unsupported"] = make(chan int)
					}
					in := make(chan *library.FluentMsg, 1)
					in <- &library.FluentMsg{ID: attempt.id, Tag: "source", Message: payload, DurableAck: receipt}
					close(in)
					controller.runDataWriter(context.Background(), "source", backend, in, utils.NewCounter())
					if len(receipt) != 1 {
						t.Fatalf("receipt count=%d, want exactly one", len(receipt))
					}
					if err := <-receipt; (err != nil) != attempt.invalid {
						t.Fatalf("ID %d invalid=%v: receipt=%v", attempt.id, attempt.invalid, err)
					}
					if attempt.invalid {
						if len(controller.outChan) != 0 {
							t.Fatal("rejected payload was forwarded")
						}
						continue
					}
					if len(controller.outChan) != 1 {
						t.Fatal("accepted message was not forwarded")
					}
					msg := <-controller.outChan
					if msg.ID != attempt.id || msg.Tag != "source" || msg.JournalTag != "source" || !reflect.DeepEqual(msg.Message, upgradePayload(attempt.id)) {
						t.Fatalf("live message changed: %+v", msg)
					}
				}
				backend.Close()
				// Use the controller recovery path, not the encoder or private
				// caches, to reconcile every successfully synchronized message.
				for restart := 0; restart < 2; restart++ {
					backend = upgradeBackend(t, dir, compressed)
					controller = upgradeController(backend, groupSize)
					high, err := controller.LoadMaxID()
					if err != nil || high != 3 {
						t.Fatalf("restart %d: frontier=%d err=%v", restart, high, err)
					}
					out := make(chan *library.FluentMsg, 8)
					if _, err := controller.ProcessLegacyMsg(out); err != nil {
						t.Fatal(err)
					}
					close(out)
					got := make(map[int64]map[string]interface{})
					for msg := range out {
						if msg.Tag != "source" || msg.JournalTag != "source" {
							t.Fatalf("replay owner changed: %+v", msg)
						}
						if _, duplicate := got[msg.ID]; duplicate {
							t.Fatalf("unexpected duplicate %d in uninterrupted replay", msg.ID)
						}
						got[msg.ID] = msg.Message
					}
					want := map[int64]map[string]interface{}{1: upgradePayload(1), 2: upgradePayload(2), 3: upgradePayload(3)}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("restart %d: accepted payloads changed or lost: got=%v want=%v", restart, got, want)
					}
					backend.Close()
				}
			})
		}
	}
}

func TestRegressionJournalUpgradeACKFrontierAfterCleanup(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("gzip=%v", compressed), func(t *testing.T) {
			dir := t.TempDir()
			backend := upgradeBackend(t, dir, compressed)
			for _, id := range []int64{1000, 1} {
				data := &journal.Data{ID: id, Data: map[string]interface{}{"tag": "source", "message": upgradePayload(id)}}
				if err := backend.WriteData(data); err != nil {
					t.Fatal(err)
				}
				if err := backend.WriteId(id); err != nil {
					t.Fatal(err)
				}
				if err := backend.Sync(); err != nil {
					t.Fatal(err)
				}
				if err := backend.Rotate(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			controller := upgradeController(backend, 1)
			out := make(chan *library.FluentMsg, 4)
			if _, err := controller.ProcessLegacyMsg(out); err != nil {
				t.Fatal(err)
			}
			if len(out) != 0 {
				t.Fatal("completed messages were replayed")
			}
			backend.Close()
			backend = upgradeBackend(t, dir, compressed)
			controller = upgradeController(backend, 1)
			high, err := controller.LoadMaxID()
			if err != nil || high != 1000 {
				t.Fatalf("cleanup lowered accepted identity frontier: high=%d err=%v", high, err)
			}
		})
	}
}
