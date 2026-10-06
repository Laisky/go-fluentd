package recvs

import (
	"context"
	"gofluentd/internal/concatstate"
	"gofluentd/internal/tagfilters"
	"gofluentd/library"
	"regexp"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestReceiverAndTagFilterSharePendingBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		budget, _ := concatstate.NewBudget(1, 4096)
		r := newComponentFluent(t)
		r.ConcatBudget = budget
		r.ConcatorWait = time.Hour
		r.concatTagCfg = map[string]*concatCfg{"a": {headRegexp: regexp.MustCompile("^HEAD"), msgKey: "log", identifierKey: "source"}}
		cfg := &tagfilters.ConcatorCfg{MsgKey: "log", Identifier: "source", Regexp: regexp.MustCompile("^HEAD")}
		f := tagfilters.NewConcatorFact(&tagfilters.ConcatorFactCfg{NFork: 1, MaxLen: 10000, ConcatBudget: budget})
		f.SetMsgPool(&sync.Pool{})
		commits := make(chan *library.FluentMsg, 4)
		f.SetWaitCommitChan(commits)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rin, fin, rout, fout := make(chan *library.FluentMsg), make(chan *library.FluentMsg), make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
		r.SetAsyncOutChan(rout)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r.runConcator(ctx, 0, rin) }()
		go func() { defer wg.Done(); f.StartNewConcator(ctx, cfg, fout, fin) }()
		h := &library.FluentMsg{Tag: "a", Message: map[string]interface{}{"source": []byte("s"), "log": "HEAD receiver"}}
		rin <- h
		synctest.Wait()
		if budget.Snapshot().Entries != 1 {
			t.Fatal("receiver missing shared reservation")
		}
		other := &library.FluentMsg{Tag: "a", JournalTag: "original", ID: 41, ExtIds: []int64{40}, Message: map[string]interface{}{"source": "post", "log": "HEAD post"}}
		fin <- other
		synctest.Wait()
		if len(fout) != 1 || <-fout != other || len(commits) != 0 || other.ID != 41 || other.ExtIds[0] != 40 || budget.Snapshot().Entries != 1 {
			t.Fatal("post-journal bypass violated global budget/ACK ownership")
		}
		close(rin)
		synctest.Wait()
		if len(rout) != 1 || <-rout != h {
			t.Fatal("receiver lost head")
		}
		fin <- other
		synctest.Wait()
		if budget.Snapshot().Entries != 1 || len(fout) != 0 {
			t.Fatal("reservation not reusable after handoff")
		}
		close(fin)
		wg.Wait()
		if len(fout) != 1 || <-fout != other || len(commits) != 0 {
			t.Fatal("replay/head ownership lost")
		}
		if n := budget.Snapshot(); n.Entries != 0 || n.Bytes != 0 {
			t.Fatal("global budget leaked", n)
		}
	})
}
