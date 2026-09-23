package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
)

// Store and barriers are observed independently of the worker's success path.
// A sync hook may block or fail, but only a completed successful Sync updates
// the durable ledger. Messages and receipts are never read through private maps.
type writerStore struct {
	records   []int64
	payloads  map[int64]string
	durable   map[int64]bool
	syncCalls int
	syncHook  func([]int64) error
	writeHook func(int64) error
}

func (s *writerStore) WriteData(d *journal.Data) error {
	if s.writeHook != nil {
		if err := s.writeHook(d.ID); err != nil {
			return err
		}
	}
	if s.payloads == nil {
		s.payloads = map[int64]string{}
	}
	s.records = append(s.records, d.ID)
	s.payloads[d.ID] = d.Data["message"].(map[string]interface{})["payload"].(string)
	return nil
}
func (s *writerStore) Sync() error {
	s.syncCalls++
	ids := append([]int64(nil), s.records...)
	if s.syncHook != nil {
		if err := s.syncHook(ids); err != nil {
			return err
		}
	}
	if s.durable == nil {
		s.durable = map[int64]bool{}
	}
	for _, id := range ids {
		s.durable[id] = true
	}
	return nil
}
func writerMessage(id int64, durable bool) (*library.FluentMsg, chan error) {
	m := &library.FluentMsg{ID: id, Tag: "source", Message: map[string]interface{}{"payload": fmt.Sprintf("payload-%d", id)}}
	var receipt chan error
	if durable {
		receipt = make(chan error, 2)
		m.DurableAck = receipt
	}
	return m, receipt
}
func testWriter(t *testing.T) (*Journal, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	j := &Journal{JournalCfg: &JournalCfg{MsgPool: &sync.Pool{}}, outChan: make(chan *library.FluentMsg, 256)}
	return j, ctx, cancel
}
func writerRun(j *Journal, ctx context.Context, s journalDataWriter, in <-chan *library.FluentMsg) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); j.runDataWriter(ctx, "source", s, in, utils.NewCounter()) }()
	return done
}
func waitWriter(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish")
	}
}
func writerReceipt(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("receipt not completed")
		return nil
	}
}

func TestJournalWriterReceiptsFollowSuccessfulBarrier(t *testing.T) {
	j, ctx, _ := testWriter(t)
	in := make(chan *library.FluentMsg, 137)
	receipts := make([]chan error, 138)
	for id := int64(1); id <= 137; id++ {
		m, r := writerMessage(id, true)
		in <- m
		receipts[id] = r
	}
	close(in)
	// Executed on the storage side before each barrier completes. It checks
	// every newly written record, not a particular batch size or queue layout.
	prior := map[int64]bool{}
	s := &writerStore{syncHook: func(ids []int64) error {
		for _, id := range ids {
			if !prior[id] && len(receipts[id]) != 0 {
				t.Errorf("receipt %d preceded its barrier", id)
			}
			prior[id] = true
		}
		return nil
	}}
	waitWriter(t, writerRun(j, ctx, s, in))
	if len(j.outChan) != 137 {
		t.Fatalf("delivered %d messages, want 137", len(j.outChan))
	}
	for id := 1; id <= 137; id++ {
		if err := writerReceipt(t, receipts[id]); err != nil {
			t.Fatal(err)
		}
		if !s.durable[int64(id)] || s.payloads[int64(id)] != fmt.Sprintf("payload-%d", id) {
			t.Fatalf("accepted record absent or corrupted: %d", id)
		}
		if len(receipts[id]) != 0 {
			t.Fatalf("duplicate completion for %d", id)
		}
		m := <-j.outChan
		if m.ID != int64(id) || m.JournalTag != "source" {
			t.Fatal("identity/order/provenance changed")
		}
	}
}
func TestJournalWriterBlockedBarrierDoesNotAcceptOrForward(t *testing.T) {
	j, ctx, _ := testWriter(t)
	in := make(chan *library.FluentMsg, 1)
	m, r := writerMessage(1, true)
	in <- m
	close(in)
	entered, release := make(chan struct{}), make(chan struct{})
	s := &writerStore{syncHook: func([]int64) error { close(entered); <-release; return nil }}
	done := writerRun(j, ctx, s, in)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Sync never entered")
	}
	if len(r) != 0 || len(j.outChan) != 0 {
		t.Error("uncommitted group exposed success")
	}
	close(release)
	waitWriter(t, done)
	if err := writerReceipt(t, r); err != nil {
		t.Fatal(err)
	}
}
func TestJournalWriterStorageErrorsNeverSucceed(t *testing.T) {
	for _, stage := range []string{"write", "sync"} {
		t.Run(stage, func(t *testing.T) {
			j, ctx, _ := testWriter(t)
			in := make(chan *library.FluentMsg, 1)
			m, r := writerMessage(1, true)
			in <- m
			close(in)
			want := errors.New("storage refused")
			s := &writerStore{}
			if stage == "write" {
				s.writeHook = func(int64) error { return want }
			} else {
				s.syncHook = func([]int64) error { return want }
			}
			waitWriter(t, writerRun(j, ctx, s, in))
			if err := writerReceipt(t, r); !errors.Is(err, want) {
				t.Fatalf("storage error=%v", err)
			}
			if len(j.outChan) != 0 || s.durable[1] {
				t.Fatal("failed record was accepted or published")
			}
			if stage == "write" && s.syncCalls != 0 {
				t.Fatal("attempted to sync a failed append")
			}
		})
	}
}
func TestJournalWriterBestEffortDoesNotRequestSync(t *testing.T) {
	j, ctx, _ := testWriter(t)
	in := make(chan *library.FluentMsg, 3)
	for id := int64(1); id <= 3; id++ {
		m, _ := writerMessage(id, false)
		in <- m
	}
	close(in)
	s := &writerStore{}
	waitWriter(t, writerRun(j, ctx, s, in))
	if s.syncCalls != 0 || len(j.outChan) != 3 {
		t.Fatal("best-effort policy changed")
	}
}
func TestJournalWriterLateArrivalNeedsItsOwnBarrier(t *testing.T) {
	j, ctx, _ := testWriter(t)
	in := make(chan *library.FluentMsg, 4)
	first, firstReceipt := writerMessage(1, true)
	in <- first
	entered := make(chan []int64)
	release := make(chan struct{})
	s := &writerStore{syncHook: func(ids []int64) error { entered <- ids; <-release; return nil }}
	done := writerRun(j, ctx, s, in)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first barrier absent")
	}
	later, r := writerMessage(2, true)
	in <- later
	close(in)
	release <- struct{}{}
	if err := writerReceipt(t, firstReceipt); err != nil {
		t.Fatal(err)
	}
	select {
	case ids := <-entered:
		if !reflect.DeepEqual(ids, []int64{1, 2}) {
			t.Errorf("second barrier misses record: %v", ids)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late arrival never synchronized")
	}
	if len(r) != 0 {
		t.Error("late arrival used an earlier barrier")
	}
	release <- struct{}{}
	waitWriter(t, done)
	if err := writerReceipt(t, r); err != nil {
		t.Fatal(err)
	}
}
func TestJournalWriterCancellationWhileIdle(t *testing.T) {
	j, ctx, cancel := testWriter(t)
	s := &writerStore{}
	in := make(chan *library.FluentMsg)
	done := writerRun(j, ctx, s, in)
	cancel()
	waitWriter(t, done)
	if len(s.records) != 0 || s.syncCalls != 0 {
		t.Fatal("idle cancellation wrote data")
	}
}
func TestJournalWriterReopenHasEveryAcceptedRecord(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			j, ctx, _ := testWriter(t)
			dir := t.TempDir()
			jj, err := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithIsCompress(compressed), journal.WithBufSizeByte(1<<20))
			if err != nil {
				t.Fatal(err)
			}
			if err = jj.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer jj.Close()
			in := make(chan *library.FluentMsg, 137)
			receipts := make([]chan error, 137)
			for i := range receipts {
				m, r := writerMessage(int64(i+1), true)
				m.Message["payload"] = strings.Repeat("x", 2048) + fmt.Sprint(i)
				in <- m
				receipts[i] = r
			}
			close(in)
			waitWriter(t, writerRun(j, ctx, jj, in))
			for _, r := range receipts {
				if err := writerReceipt(t, r); err != nil {
					t.Fatal(err)
				}
			}
			// Open the actual files while the writer remains alive. No extra Sync,
			// Close, or rotation is allowed to manufacture the promised persistence.
			names, err := filepath.Glob(filepath.Join(dir, "*.buf*"))
			if err != nil {
				t.Fatal(err)
			}
			seen := map[int64]string{}
			for _, name := range names {
				fp, err := os.Open(name)
				if err != nil {
					t.Fatal(err)
				}
				dec, err := journal.NewDataDecoder(fp, compressed)
				if err != nil {
					fp.Close()
					t.Fatal(err)
				}
				for {
					d := &journal.Data{}
					err = dec.Read(d)
					if err == io.EOF {
						break
					}
					if err != nil {
						fp.Close()
						t.Fatal(err)
					}
					seen[d.ID] = d.Data["message"].(map[string]interface{})["payload"].(string)
				}
				fp.Close()
			}
			if len(seen) != 137 {
				t.Fatalf("durable files contain %d of 137 accepted records (files=%v)", len(seen), names)
			}
			for i := 0; i < 137; i++ {
				if seen[int64(i+1)] != strings.Repeat("x", 2048)+fmt.Sprint(i) {
					t.Fatalf("payload corrupted for %d", i+1)
				}
			}
		})
	}
}

func TestJournalGroupBoundedAndFinalPartialGroup(t *testing.T) {
	for _, limit := range []int{1, 7, 64} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			j, ctx, _ := testWriter(t)
			j.GroupCommitMaxMessages = limit
			in := make(chan *library.FluentMsg, 137)
			rs := make([]chan error, 137)
			for i := range rs {
				m, r := writerMessage(int64(i+1), true)
				in <- m
				rs[i] = r
			}
			close(in)
			last := 0
			s := &writerStore{syncHook: func(ids []int64) error {
				n := len(ids) - last
				if n < 1 || n > limit {
					t.Errorf("unbounded/empty group: %d, limit=%d", n, limit)
				}
				last = len(ids)
				return nil
			}}
			waitWriter(t, writerRun(j, ctx, s, in))
			if s.syncCalls != (137+limit-1)/limit {
				t.Fatalf("ready backlog required %d Sync calls, expected %d", s.syncCalls, (137+limit-1)/limit)
			}
			for _, r := range rs {
				if err := writerReceipt(t, r); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestJournalGroupSharedFailureAndSubsequentSuccess(t *testing.T) {
	for _, stage := range []string{"write", "sync"} {
		t.Run(stage, func(t *testing.T) {
			j, ctx, _ := testWriter(t)
			j.GroupCommitMaxMessages = 7
			in := make(chan *library.FluentMsg, 14)
			rs := make([]chan error, 14)
			for i := range rs {
				m, r := writerMessage(int64(i+1), true)
				in <- m
				rs[i] = r
			}
			close(in)
			want := errors.New("injected group refusal")
			s := &writerStore{}
			if stage == "write" {
				s.writeHook = func(id int64) error {
					if id == 4 {
						return want
					}
					return nil
				}
			} else {
				s.syncHook = func(ids []int64) error {
					if len(ids) == 7 {
						return want
					}
					return nil
				}
			}
			waitWriter(t, writerRun(j, ctx, s, in))
			for i, r := range rs {
				err := writerReceipt(t, r)
				if i < 7 && !errors.Is(err, want) {
					t.Fatalf("group member %d falsely accepted: %v", i+1, err)
				}
				if i >= 7 && err != nil {
					t.Fatalf("next successful group rejected: %v", err)
				}
			}
			if len(j.outChan) != 7 {
				t.Fatalf("live output count=%d, want only next successful group", len(j.outChan))
			}
			for len(j.outChan) > 0 {
				if m := <-j.outChan; m.ID <= 7 {
					t.Fatalf("failed group leaked live record %d", m.ID)
				}
			}
		})
	}
}
func TestJournalGroupCancellationAfterPartialAppend(t *testing.T) {
	j, ctx, cancel := testWriter(t)
	j.GroupCommitMaxMessages = 7
	in := make(chan *library.FluentMsg, 7)
	rs := make([]chan error, 7)
	for i := range rs {
		m, r := writerMessage(int64(i+1), true)
		in <- m
		rs[i] = r
	}
	close(in)
	entered, release := make(chan struct{}), make(chan struct{})
	s := &writerStore{writeHook: func(id int64) error {
		if id == 2 {
			close(entered)
			<-release
		}
		return nil
	}}
	done := writerRun(j, ctx, s, in)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("second write not reached")
	}
	cancel()
	close(release)
	waitWriter(t, done)
	for _, r := range rs {
		if err := writerReceipt(t, r); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled group result=%v", err)
		}
	}
	if s.syncCalls != 0 || len(j.outChan) != 0 {
		t.Fatal("cancelled incomplete group accepted")
	}
}
func TestJournalGroupMixedReliabilityAndSeparateTags(t *testing.T) {
	j, ctx, _ := testWriter(t)
	j.GroupCommitMaxMessages = 7
	in := make(chan *library.FluentMsg, 3)
	a, ra := writerMessage(1, true)
	b, _ := writerMessage(2, false)
	c, rc := writerMessage(3, true)
	in <- a
	in <- b
	in <- c
	close(in)
	s := &writerStore{}
	waitWriter(t, writerRun(j, ctx, s, in))
	if s.syncCalls != 1 || len(j.outChan) != 3 {
		t.Fatal("mixed group lost data or its barrier")
	}
	if writerReceipt(t, ra) != nil || writerReceipt(t, rc) != nil {
		t.Fatal("reliable receipts failed")
	}
	// A blocked tag must not impose a global batching lock on another tag.
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := &writerStore{syncHook: func([]int64) error { close(entered); <-release; return nil }}
	input := make(chan *library.FluentMsg, 1)
	m, r := writerMessage(4, true)
	input <- m
	close(input)
	done := writerRun(j, ctx, blocked, input)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("blocked tag not at barrier")
	}
	second := &writerStore{}
	otherIn := make(chan *library.FluentMsg, 1)
	m2, r2 := writerMessage(5, true)
	otherIn <- m2
	close(otherIn)
	otherDone := make(chan struct{})
	go func() { defer close(otherDone); j.runDataWriter(ctx, "other", second, otherIn, utils.NewCounter()) }()
	waitWriter(t, otherDone)
	if writerReceipt(t, r2) != nil || len(r) != 0 {
		t.Error("independent tag blocked or first tag accepted prematurely")
	}
	close(release)
	waitWriter(t, done)
}
func TestJournalGroupConfigBounds(t *testing.T) {
	for _, limit := range []int{-1, 1025} {
		j, _, _ := testWriter(t)
		j.GroupCommitMaxMessages = limit
		if j.valid() == nil {
			t.Fatalf("unsafe group size %d accepted", limit)
		}
	}
	j, _, _ := testWriter(t)
	j.BufDirPath = t.TempDir()
	if err := j.valid(); err != nil {
		t.Fatal(err)
	}
	if j.GroupCommitMaxMessages != 64 {
		t.Fatal("wrong default group bound")
	}
}

// Rotation may occur between group appends. It must sync the sealed predecessor
// before the final group Sync covers the new active file. This uses actual
// journal public APIs and reads every accepted record without an extra flush.
type rotatingGroupStore struct {
	*journal.Journal
	ctx              context.Context
	syncs, rotations int
}

func (s *rotatingGroupStore) WriteData(d *journal.Data) error {
	if err := s.Journal.WriteData(d); err != nil {
		return err
	}
	if d.ID == 3 {
		s.rotations++
		return s.Journal.Rotate(s.ctx)
	}
	return nil
}
func (s *rotatingGroupStore) Sync() error { s.syncs++; return s.Journal.Sync() }

func TestJournalGroupFinalBarrierCoversRotatedPredecessor(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			j, ctx, _ := testWriter(t)
			dir := t.TempDir()
			jj, err := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithIsCompress(compressed), journal.WithIsAggresiveGC(false), journal.WithFlushInterval(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if err = jj.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer jj.Close()
			s := &rotatingGroupStore{Journal: jj, ctx: ctx}
			in := make(chan *library.FluentMsg, 17)
			receipts := make([]chan error, 17)
			for i := range receipts {
				m, r := writerMessage(int64(i+1), true)
				in <- m
				receipts[i] = r
			}
			close(in)
			waitWriter(t, writerRun(j, ctx, s, in))
			for _, r := range receipts {
				if err := writerReceipt(t, r); err != nil {
					t.Fatal(err)
				}
			}
			if s.syncs != 1 || s.rotations != 1 {
				t.Fatalf("barriers=%d rotations=%d", s.syncs, s.rotations)
			}
			names, err := filepath.Glob(filepath.Join(dir, "*.buf*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(names) < 2 {
				t.Fatal("rotation did not produce separate files")
			}
			seen := map[int64]bool{}
			for _, name := range names {
				fp, err := os.Open(name)
				if err != nil {
					t.Fatal(err)
				}
				dec, err := journal.NewDataDecoder(fp, compressed)
				if err != nil {
					fp.Close()
					t.Fatal(err)
				}
				for {
					d := &journal.Data{}
					err = dec.Read(d)
					if err == io.EOF {
						break
					}
					if err != nil {
						fp.Close()
						t.Fatal(err)
					}
					if seen[d.ID] || d.ID < 1 || d.ID > 17 || d.Data["message"].(map[string]interface{})["payload"] != fmt.Sprintf("payload-%d", d.ID) {
						fp.Close()
						t.Fatal("rotated data corrupted")
					}
					seen[d.ID] = true
				}
				fp.Close()
			}
			if len(seen) != 17 {
				t.Fatalf("only %d records durable after rotated group acceptance", len(seen))
			}
		})
	}
}
func TestJournalGroupLaterSyncFailureDoesNotRevokeEarlierSuccess(t *testing.T) {
	j, ctx, _ := testWriter(t)
	j.GroupCommitMaxMessages = 7
	in := make(chan *library.FluentMsg, 14)
	rs := make([]chan error, 14)
	for i := range rs {
		m, r := writerMessage(int64(i+1), true)
		in <- m
		rs[i] = r
	}
	close(in)
	want := errors.New("second group failed")
	s := &writerStore{syncHook: func(ids []int64) error {
		if len(ids) > 7 {
			return want
		}
		return nil
	}}
	waitWriter(t, writerRun(j, ctx, s, in))
	for i, r := range rs {
		err := writerReceipt(t, r)
		if i < 7 && err != nil {
			t.Fatal(err)
		}
		if i >= 7 && !errors.Is(err, want) {
			t.Fatalf("late failed barrier falsely accepted %d", i+1)
		}
	}
	if len(j.outChan) != 7 {
		t.Fatalf("output=%d", len(j.outChan))
	}
	for len(j.outChan) > 0 {
		if (<-j.outChan).ID > 7 {
			t.Fatal("failed group escaped")
		}
	}
}
