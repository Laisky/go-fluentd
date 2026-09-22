package controller

import (
	"context"
	"errors"
	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func regressionReplayJournal(t *testing.T, committed bool) (*Journal, *journal.Journal, string) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	backend, err := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithBufSizeByte(8192), journal.WithRotateDuration(time.Hour), journal.WithIsAggresiveGC(false))
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	data := &journal.Data{ID: 42, Data: map[string]interface{}{"tag": "logs", "message": map[string]interface{}{"payload": "must-survive"}}}
	if err = backend.WriteData(data); err != nil {
		t.Fatal(err)
	}
	if committed {
		if err = backend.WriteId(42); err != nil {
			t.Fatal(err)
		}
	}
	// The legacy loader deliberately retains the latest closed segment.
	for i := 0; i < 2; i++ {
		if err = backend.Rotate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	j := &Journal{JournalCfg: &JournalCfg{MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}}, legacyLock: utils.NewMutex(), tag2JMap: &sync.Map{}}
	j.tag2JMap.Store("logs", backend)
	return j, backend, dir
}
func regressionDiskHasMessage(t *testing.T, dir string) bool {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.buf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		fp, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := journal.NewDataDecoder(fp, false)
		if err != nil {
			fp.Close()
			t.Fatal(err)
		}
		for {
			var data journal.Data
			err = dec.Read(&data)
			if err == io.EOF {
				break
			}
			if err != nil {
				fp.Close()
				t.Fatal(err)
			}
			if data.ID == 42 {
				fp.Close()
				return true
			}
		}
		fp.Close()
	}
	return false
}
func TestRegressionReplayPreservesDiskCopyBeforeQueueConsumption(t *testing.T) {
	j, _, dir := regressionReplayJournal(t, false)
	if !regressionDiskHasMessage(t, dir) {
		t.Fatal("invalid fixture: missing initial durable record")
	}
	queue := make(chan *library.FluentMsg, 8) // Deliberately no writer/consumer.
	max, err := j.ProcessLegacyMsg(queue)
	if err != nil {
		t.Fatal(err)
	}
	if max != 42 || len(queue) != 1 {
		t.Fatalf("replay did not exercise the record: max=%d queued=%d", max, len(queue))
	}
	if !regressionDiskHasMessage(t, dir) {
		t.Error("replay deleted the only disk copy while the replacement was only queued in memory")
	}
}
func TestRegressionReplaySkipsAcknowledgedRecord(t *testing.T) {
	j, _, _ := regressionReplayJournal(t, true)
	queue := make(chan *library.FluentMsg, 8)
	if _, err := j.ProcessLegacyMsg(queue); err != nil {
		t.Fatal(err)
	}
	if len(queue) != 0 {
		t.Error("acknowledged record was replayed")
	}
}
func TestRegressionReplayCancellation(t *testing.T) {
	j, _, _ := regressionReplayJournal(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := j.processLegacyMsg(ctx, make(chan *library.FluentMsg)); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error=%v, want cancellation", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("replay ignores cancellation while output is blocked")
	}
}
func TestRegressionCommittedIDRetry(t *testing.T) {
	sentinel := errors.New("disk unavailable")
	for _, tc := range []struct {
		name                string
		failures, wantCalls int
		wantErr             bool
	}{{"success", 0, 1, false}, {"transient", 1, 2, false}, {"permanent", 10, 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := writeCommittedID(func(id int64) error {
				calls++
				if id != 42 {
					t.Errorf("ID=%d", id)
				}
				if calls <= tc.failures {
					return sentinel
				}
				return nil
			}, 42)
			if calls != tc.wantCalls || (err != nil) != tc.wantErr {
				t.Errorf("calls=%d err=%v; want calls=%d wantErr=%v", calls, err, tc.wantCalls, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, sentinel) {
				t.Error("original write error was lost")
			}
		})
	}
}

type regressionReplayBackend struct {
	loadError   error
	malformed   bool
	unlockCalls int
	writes      int
}

func (*regressionReplayBackend) LockLegacy() bool     { return true }
func (b *regressionReplayBackend) UnLockLegacy() bool { b.unlockCalls++; return true }
func (b *regressionReplayBackend) LoadLegacyBuf(data *journal.Data) error {
	if b.malformed {
		data.ID = 42
		data.Data = map[string]interface{}{"tag": 123, "message": map[string]interface{}{"payload": "keep"}}
		return nil
	}
	// Model the documented backend handoff: EOF/error already released
	// our lock, and another owner may acquire it before this returns.
	return b.loadError
}
func (b *regressionReplayBackend) WriteData(*journal.Data) error { b.writes++; return nil }
func regressionControllerForBackend(b legacyJournal) *Journal {
	j := &Journal{JournalCfg: &JournalCfg{MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}}, legacyLock: utils.NewMutex(), tag2JMap: &sync.Map{}}
	j.tag2JMap.Store("logs", b)
	return j
}
func TestRegressionReplayDoesNotReleaseAnotherOwner(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF} {
		b := &regressionReplayBackend{loadError: err}
		regressionControllerForBackend(b).ProcessLegacyMsg(make(chan *library.FluentMsg, 1))
		if b.unlockCalls != 0 {
			t.Errorf("released another owner's lock %d times after %v", b.unlockCalls, err)
		}
	}
}
func TestRegressionReplayMalformedRecordPreserved(t *testing.T) {
	b := &regressionReplayBackend{malformed: true}
	_, err := regressionControllerForBackend(b).ProcessLegacyMsg(make(chan *library.FluentMsg, 1))
	if err == nil {
		t.Error("malformed record accepted")
	}
	if b.writes != 1 {
		t.Errorf("malformed record lost its replacement copy: writes=%d", b.writes)
	}
	if b.unlockCalls != 1 {
		t.Errorf("owned lock not released exactly once: %d", b.unlockCalls)
	}
}
