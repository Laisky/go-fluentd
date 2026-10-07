package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gofluentd/library"
)

func testTagBudget(t *testing.T, n int) *TagBudget {
	t.Helper()
	b, e := NewTagBudget(n)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestLegacyTagBudgetLimitsAndGrammar(t *testing.T) {
	for _, n := range []int{-1, MaximumLegacyMaxTags + 1} {
		if _, e := NewTagBudget(n); e == nil {
			t.Errorf("accepted invalid limit %d", n)
		}
	}
	b := testTagBudget(t, 0)
	_, limit, _ := b.Snapshot()
	if limit != 64 {
		t.Fatalf("default=%d", limit)
	}
	for _, tag := range []string{"", ".", "..", "../other", "nested/tag", "a\x00b", strings.Repeat("x", 256)} {
		if !errors.Is(b.Admit(tag), ErrLegacyTagInvalid) {
			t.Errorf("accepted invalid %q", tag)
		}
	}
	for _, tag := range []string{"logs.prod", "a:b", `a\b`, "logs with spaces", "日志", strings.Repeat("x", 255)} {
		if e := b.Admit(tag); e != nil {
			t.Errorf("rejected valid %q: %v", tag, e)
		}
	}
	reserved, _, rejects := b.Snapshot()
	if reserved != 6 || rejects != 7 {
		t.Fatalf("counters %d/%d", reserved, rejects)
	}
}

func TestLegacyTagBudgetConcurrentSourcesAndAtomicRetained(t *testing.T) {
	b := testTagBudget(t, 3)
	var wg sync.WaitGroup
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = b.Admit(fmt.Sprintf("tag-%d", i)) }(i)
	}
	wg.Wait()
	n, limit, rejected := b.Snapshot()
	if n != 3 || limit != 3 || rejected != 77 {
		t.Fatalf("overbooked %d/%d rejected=%d", n, limit, rejected)
	}
	for tag := range b.tags {
		if e := b.Admit(tag); e != nil {
			t.Fatal(e)
		}
	}
	c := testTagBudget(t, 2)
	if e := c.admitRetained([]string{"a", "a", "b"}); e != nil {
		t.Fatal(e)
	}
	d := testTagBudget(t, 2)
	if !errors.Is(d.admitRetained([]string{"a", "b", "c"}), ErrLegacyTagLimit) {
		t.Fatal("retained excess accepted")
	}
	if n, _, _ := d.Snapshot(); n != 0 {
		t.Fatal("partially reserved rejected inventory")
	}
	if !errors.Is(d.admitRetained([]string{"a", "../escape"}), ErrLegacyTagInvalid) {
		t.Fatal("retained invalid accepted")
	}
	if n, _, _ := d.Snapshot(); n != 0 {
		t.Fatal("partially reserved invalid inventory")
	}
}

func budgetJournal(t *testing.T, b *TagBudget, root string) (*Journal, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	j := NewJournal(ctx, &JournalCfg{TagBudget: b, BufDirPath: root, BufSizeBytes: 8192, JournalOutChanLen: 8, CommitIDChanLen: 8, ChildJournalDataInchanLen: 8, ChildJournalIDInchanLen: 8, CommittedIDTTL: time.Minute, MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}})
	t.Cleanup(func() { cancel(); <-j.directoriesClosed })
	return j, ctx
}

func TestLegacyTagBudgetRefusalKeepsExistingTrafficAndReceipt(t *testing.T) {
	b := testTagBudget(t, 1)
	j, ctx := budgetJournal(t, b, filepath.Join(t.TempDir(), "root"))
	if e := j.createJournalRunner(ctx, "kept"); e != nil {
		t.Fatal(e)
	}
	dump, skip := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	out := j.DumpMsgFlow(ctx, j.MsgPool, dump, skip)
	for _, ch := range []chan *library.FluentMsg{dump, skip} {
		ack := make(chan error, 1)
		ch <- &library.FluentMsg{Tag: "excess", ID: 19, JournalTag: "retained-owner", ExtIds: []int64{17, 18}, Message: map[string]interface{}{"text": "x"}, DurableAck: ack}
		select {
		case e := <-ack:
			if !errors.Is(e, ErrLegacyTagLimit) {
				t.Fatalf("refused receipt=%v", e)
			}
		case <-time.After(time.Second):
			t.Fatal("no error receipt")
		}
	}
	ack := make(chan error, 1)
	dump <- &library.FluentMsg{Tag: "kept", ID: 20, Message: map[string]interface{}{"text": "ok"}, DurableAck: ack}
	select {
	case e := <-ack:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("known tag blocked")
	}
	m := componentRecv(t, out)
	if m.Tag != "kept" || m.ID != 20 {
		t.Fatalf("refused tag leaked to output: %+v", m)
	}
	if _, e := os.Stat(filepath.Join(j.BufDirPath, "excess")); !os.IsNotExist(e) {
		t.Fatal("refusal created filesystem state")
	}
	if len(j.commitChan) != 0 {
		t.Fatal("refusal acknowledged work")
	}
	close(dump)
	close(skip)
}

func TestLegacyTagBudgetCloseDoesNotRecycleIdentity(t *testing.T) {
	b := testTagBudget(t, 1)
	j, ctx := budgetJournal(t, b, filepath.Join(t.TempDir(), "root"))
	if e := j.createJournalRunner(ctx, "first"); e != nil {
		t.Fatal(e)
	}
	if e := j.CloseTag("first"); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(j.createJournalRunner(ctx, "second"), ErrLegacyTagLimit) {
		t.Fatal("CloseTag bypassed lifetime ceiling")
	}
	if e := j.createJournalRunner(ctx, "first"); e != nil {
		t.Fatal(e)
	}
}

func TestLegacyTagBudgetStartupInventoryBeforeAnyOpen(t *testing.T) {
	for _, kind := range []string{"directories", "files"} {
		t.Run(kind, func(t *testing.T) {
			root := privateJournalTestDir(t)
			for i := 0; i < 3; i++ {
				p := filepath.Join(root, fmt.Sprint(i))
				if kind == "directories" {
					if e := os.Mkdir(p, 0700); e != nil {
						t.Fatal(e)
					}
				} else if e := os.WriteFile(p, []byte("preserved"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				j := NewJournal(ctx, &JournalCfg{TagBudget: testTagBudget(t, 2), BufDirPath: root, MsgPool: &sync.Pool{}})
				cancel()
				<-j.directoriesClosed
			}()
			if !panicked {
				t.Error("oversized startup inventory was opened")
			}
			entries, e := os.ReadDir(root)
			if e != nil || len(entries) != 3 {
				t.Fatalf("inventory changed: %v", e)
			}
			for _, entry := range entries {
				p := filepath.Join(root, entry.Name())
				if kind == "directories" {
					children, e := os.ReadDir(p)
					if e != nil || len(children) != 0 {
						t.Errorf("partial backend allocation in %s: %v", p, children)
					}
				} else {
					data, e := os.ReadFile(p)
					if e != nil || string(data) != "preserved" {
						t.Error("evidence changed")
					}
				}
			}
		})
	}
}

func TestLegacyTagBudgetSharedAcrossStagesAndRewrittenTags(t *testing.T) {
	b := testTagBudget(t, 2)
	j, ctx := budgetJournal(t, b, filepath.Join(t.TempDir(), "root"))
	if e := j.createJournalRunner(ctx, "source"); e != nil {
		t.Fatal(e)
	}
	in, commits := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	p, e := NewProducer(&ProducerCfg{TagBudget: b, InChan: in, CommitChan: commits, NFork: 1, MsgPool: &sync.Pool{}})
	if e != nil {
		t.Fatal(e)
	}
	p.Run(ctx)
	// A post-filter rewrite consumes the same process budget, even though no
	// child journal exists for that destination routing identity.
	in <- &library.FluentMsg{ID: 1, Tag: "rewritten", JournalTag: "source", Message: map[string]interface{}{}}
	componentRecv(t, commits)
	ack := make(chan error, 1)
	refused := &library.FluentMsg{ID: 2, Tag: "third", JournalTag: "source", ExtIds: []int64{3}, Message: map[string]interface{}{}, DurableAck: ack}
	in <- refused
	select {
	case e := <-ack:
		if !errors.Is(e, ErrLegacyTagLimit) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("rewritten tag not refused")
	}
	if len(commits) != 0 {
		t.Fatal("refused rewritten tag falsely ACKed")
	}
	if refused.ID != 2 || refused.JournalTag != "source" || len(refused.ExtIds) != 1 {
		t.Fatal("lost retained ownership")
	}
	pipe := &componentPipeline{counts: map[string]int{}}
	din := make(chan *library.FluentMsg, 4)
	d := NewDispatcher(&DispatcherCfg{TagBudget: b, InChan: din, TagPipeline: pipe, NFork: 1, OutChanSize: 4})
	d.Run(ctx)
	din <- &library.FluentMsg{Tag: "third", ID: 3}
	din <- &library.FluentMsg{Tag: "source", ID: 4}
	if got := componentRecv(t, d.GetOutChan()); got.ID != 4 {
		t.Fatal("dispatcher bypassed shared budget")
	}
	if n, _, _ := b.Snapshot(); n != 2 {
		t.Fatal("shared registry changed")
	}
	close(in)
	close(din)
}
