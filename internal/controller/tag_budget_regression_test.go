package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gofluentd/library"
)

// Baseline-compatible reproductions deliberately use small journals and queues.
// Sixty-five identities are enough to falsify the default lifetime ceiling; no
// memory-exhaustion or disk-fill load is necessary.
func TestRegressionLegacyTagJournalDefaultBound(t *testing.T) {
	j, ctx := securityJournal(t, filepath.Join(t.TempDir(), "journal"), false)
	for i := 0; i < 64; i++ {
		if err := j.createJournalRunner(ctx, fmt.Sprintf("tag-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.createJournalRunner(ctx, "excess"); err == nil {
		t.Error("TAG_BUDGET_REGRESSION: 65th journal identity accepted")
	}
	if _, err := os.Stat(filepath.Join(j.BufDirPath, "excess")); !os.IsNotExist(err) {
		t.Errorf("TAG_BUDGET_REGRESSION: excess identity allocated a directory: %v", err)
	}
	if err := j.createJournalRunner(ctx, "tag-00"); err != nil {
		t.Errorf("known identity stopped at saturation: %v", err)
	}
}

func TestRegressionLegacyTagDispatcherDefaultBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := &componentPipeline{counts: map[string]int{}}
	in := make(chan *library.FluentMsg, 66)
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 1, OutChanSize: 128})
	d.Run(ctx)
	for i := 0; i < 65; i++ {
		in <- &library.FluentMsg{ID: int64(i), Tag: fmt.Sprintf("tag-%02d", i)}
	}
	in <- &library.FluentMsg{ID: 100, Tag: "tag-00"}
	for {
		m := componentRecv(t, d.GetOutChan())
		if m.ID == 64 {
			t.Error("TAG_BUDGET_REGRESSION: 65th dispatcher identity forwarded")
		}
		if m.ID == 100 {
			break
		}
	}
	pipe.mu.Lock()
	defer pipe.mu.Unlock()
	if len(pipe.counts) > 64 {
		t.Errorf("TAG_BUDGET_REGRESSION: registered %d dispatcher pipelines", len(pipe.counts))
	}
	close(in)
}

func TestRegressionLegacyTagProducerNegativeCacheDefaultBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, commits := make(chan *library.FluentMsg, 66), make(chan *library.FluentMsg, 128)
	p, err := NewProducer(&ProducerCfg{InChan: in, CommitChan: commits, NFork: 1, MsgPool: &sync.Pool{}})
	if err != nil {
		t.Fatal(err)
	}
	p.Run(ctx)
	for i := 0; i < 65; i++ {
		in <- &library.FluentMsg{ID: int64(i), Tag: fmt.Sprintf("tag-%02d", i), Message: map[string]interface{}{}}
	}
	in <- &library.FluentMsg{ID: 100, Tag: "tag-00", Message: map[string]interface{}{}}
	for {
		m := componentRecv(t, commits)
		if m.ID == 64 {
			t.Error("TAG_BUDGET_REGRESSION: excess routing identity falsely acknowledged")
		}
		if m.ID == 100 {
			break
		}
	}
	count := 0
	p.unSupportedTags.Range(func(_, _ interface{}) bool { count++; return true })
	if count > 64 {
		t.Errorf("TAG_BUDGET_REGRESSION: negative cache grew to %d identities", count)
	}
	close(in)
}
