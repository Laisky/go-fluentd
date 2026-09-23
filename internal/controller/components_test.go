package controller

import (
	"context"
	"fmt"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
	"gofluentd/library"
	"sync"
	"testing"
	"time"
)

func componentRecv(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("message delivery timed out")
		return nil
	}
}
func componentWait(t *testing.T, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !pred() {
		if time.Now().After(deadline) {
			t.Fatal("state transition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestComponentDefaultChannelCapacities(t *testing.T) {
	a := NewAcceptor(&AcceptorCfg{})
	if cap(a.GetSyncOutChan()) != a.SyncOutChanSize || cap(a.GetAsyncOutChan()) != a.AsyncOutChanSize {
		t.Errorf("acceptor effective channel capacities do not match normalized config: %d/%d", cap(a.GetSyncOutChan()), cap(a.GetAsyncOutChan()))
	}
	d := NewDispatcher(&DispatcherCfg{})
	if cap(d.GetOutChan()) != d.OutChanSize {
		t.Errorf("dispatcher capacity=%d config=%d", cap(d.GetOutChan()), d.OutChanSize)
	}
}

type componentPipeline struct {
	mu     sync.Mutex
	counts map[string]int
	calls  chan string
}

func (p *componentPipeline) Spawn(ctx context.Context, tag string, out chan<- *library.FluentMsg) (chan<- *library.FluentMsg, error) {
	p.mu.Lock()
	p.counts[tag]++
	p.mu.Unlock()
	if p.calls != nil {
		p.calls <- tag
	}
	if tag == "bad" {
		return nil, fmt.Errorf("injected spawn failure")
	}
	return out, nil
}
func TestComponentDispatcherSpawnFailureDoesNotDeadlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := &componentPipeline{counts: map[string]int{}, calls: make(chan string, 3)}
	in := make(chan *library.FluentMsg, 2)
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 1, OutChanSize: 4})
	d.Run(ctx)
	in <- &library.FluentMsg{Tag: "bad"}
	in <- &library.FluentMsg{Tag: "good", ID: 2}
	if got := componentRecv(t, d.GetOutChan()); got.ID != 2 {
		t.Fatalf("unrelated tag did not survive failed pipeline: %+v", got)
	}
	close(in)
}
func TestComponentDispatcherRoutesAndCachesByTag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := &componentPipeline{counts: map[string]int{}}
	in := make(chan *library.FluentMsg, 64)
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 4, OutChanSize: 128})
	d.Run(ctx)
	for i := 0; i < 64; i++ {
		in <- &library.FluentMsg{Tag: fmt.Sprintf("tag-%d", i%3), ID: int64(i)}
	}
	seen := map[int64]bool{}
	for i := 0; i < 64; i++ {
		m := componentRecv(t, d.GetOutChan())
		if seen[m.ID] {
			t.Errorf("duplicate %d", m.ID)
		}
		seen[m.ID] = true
	}
	pipe.mu.Lock()
	defer pipe.mu.Unlock()
	for tag, n := range pipe.counts {
		if n != 1 {
			t.Errorf("tag %s spawned %d times", tag, n)
		}
	}
	close(in)
}

type componentSender struct {
	senders.BaseSender
	name      string
	in        chan *library.FluentMsg
	good, bad chan<- *library.FluentMsg
}

func (s *componentSender) GetName() string                                 { return s.name }
func (s *componentSender) Spawn(context.Context) chan<- *library.FluentMsg { return s.in }
func (s *componentSender) SetSuccessedChan(ch chan<- *library.FluentMsg)   { s.good = ch }
func (s *componentSender) SetFailedChan(ch chan<- *library.FluentMsg)      { s.bad = ch }
func newComponentSender(name string) *componentSender {
	s := &componentSender{name: name, in: make(chan *library.FluentMsg, 8)}
	s.SetSupportedTags([]string{"logs", "other"})
	return s
}
func TestComponentProducerRequiresAllSenders(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, b := newComponentSender("a"), newComponentSender("b")
			in, commit := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
			p, err := NewProducer(&ProducerCfg{NFork: 1, InChan: in, CommitChan: commit, MsgPool: &sync.Pool{}, DistributeKey: "node"}, a, b)
			if err != nil {
				t.Fatal(err)
			}
			p.Run(ctx)
			m := &library.FluentMsg{Tag: "logs", ID: 42, Message: map[string]interface{}{}}
			in <- m
			if componentRecv(t, a.in) != m || componentRecv(t, b.in) != m {
				t.Fatal("fanout lost identity")
			}
			if m.Message["msgid"] != "node-42" {
				t.Fatalf("distribution id=%v", m.Message["msgid"])
			}
			a.good <- m
			componentWait(t, func() bool { _, ok := p.discardMsgCountMap.Load(m); return ok })
			if len(commit) != 0 {
				t.Fatal("committed before all downstream results")
			}
			if fail {
				b.bad <- m
				componentWait(t, func() bool { _, ok := p.discardMsgCountMap.Load(m); return !ok })
				if len(commit) != 0 {
					t.Fatal("failed fanout was committed")
				}
			} else {
				b.good <- m
				if componentRecv(t, commit) != m {
					t.Fatal("wrong committed instance")
				}
			}
			close(in)
		})
	}
}
func TestComponentProducerReplayInstancesAndUnsupportedPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newComponentSender("a")
	in, commit := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
	p, err := NewProducer(&ProducerCfg{NFork: 1, InChan: in, CommitChan: commit, MsgPool: &sync.Pool{}}, s)
	if err != nil {
		t.Fatal(err)
	}
	p.Run(ctx)
	first := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{}}
	second := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{}}
	in <- first
	in <- second
	x, y := componentRecv(t, s.in), componentRecv(t, s.in)
	s.good <- y
	s.good <- x
	got := map[*library.FluentMsg]bool{componentRecv(t, commit): true, componentRecv(t, commit): true}
	if !got[first] || !got[second] {
		t.Fatal("same ID conflated distinct replay instances")
	}
	for i := 0; i < 2; i++ {
		m := &library.FluentMsg{Tag: "unsupported", Message: map[string]interface{}{}}
		in <- m
		if componentRecv(t, commit) != m {
			t.Fatal("unsupported-tag terminal discard policy changed")
		}
	}
	close(in)
}

// componentReceiver observes the binding contract without opening a transport.
type componentReceiver struct {
	recvs.BaseRecv
	syncOut, asyncOut chan<- *library.FluentMsg
	pool              *sync.Pool
	count             library.CounterIft
}

func (*componentReceiver) GetName() string                                { return "component-receiver" }
func (r *componentReceiver) SetSyncOutChan(ch chan<- *library.FluentMsg)  { r.syncOut = ch }
func (r *componentReceiver) SetAsyncOutChan(ch chan<- *library.FluentMsg) { r.asyncOut = ch }
func (r *componentReceiver) SetMsgPool(p *sync.Pool)                      { r.pool = p }
func (r *componentReceiver) SetCounter(c library.CounterIft)              { r.count = c }
func (r *componentReceiver) Run(ctx context.Context) {
	for _, out := range []chan<- *library.FluentMsg{r.asyncOut, r.syncOut} {
		m := &library.FluentMsg{Tag: "logs", ID: r.count.Count(), Message: map[string]interface{}{}}
		select {
		case out <- m:
		case <-ctx.Done():
			return
		}
	}
}
func TestComponentAcceptorBindsReceiversAndRestartsAboveJournalMaximum(t *testing.T) {
	j, _, _ := regressionReplayJournal(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := &componentReceiver{}, &componentReceiver{}
	pool := &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}
	acceptor := NewAcceptor(&AcceptorCfg{Journal: j, MsgPool: pool, AsyncOutChanSize: 4, SyncOutChanSize: 4}, a, b)
	acceptor.Run(ctx)
	seen := map[int64]bool{}
	for _, ch := range []chan *library.FluentMsg{acceptor.GetAsyncOutChan(), acceptor.GetSyncOutChan()} {
		for i := 0; i < 2; i++ {
			m := componentRecv(t, ch)
			if m.ID <= 42 || seen[m.ID] {
				t.Fatalf("reused persisted/parallel ID %d", m.ID)
			}
			seen[m.ID] = true
		}
	}
	if a.pool != pool || b.pool != pool {
		t.Fatal("receivers were not bound to the shared pool")
	}
}
