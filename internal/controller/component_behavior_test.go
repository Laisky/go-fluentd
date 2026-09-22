package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/senders"
	"gofluentd/internal/tagfilters"
	"gofluentd/library"
)

type behaviorPipeline struct {
	mu        sync.Mutex
	calls     map[string]int
	failFirst bool
}

func (p *behaviorPipeline) Spawn(_ context.Context, tag string, out chan<- *library.FluentMsg) (chan<- *library.FluentMsg, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[tag]++
	if p.failFirst {
		p.failFirst = false
		return nil, errors.New("injected spawn failure")
	}
	return out, nil
}
func behaviorReceive(t *testing.T, c <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-c:
		return m
	case <-time.After(time.Second):
		t.Fatal("message did not arrive")
		return nil
	}
}
func TestBehaviorDispatcherDefaultsMatchBuffers(t *testing.T) {
	d := NewDispatcher(&DispatcherCfg{})
	if cap(d.GetOutChan()) != d.OutChanSize {
		t.Fatalf("declared capacity %d, actual %d", d.OutChanSize, cap(d.GetOutChan()))
	}
	a := NewAcceptor(&AcceptorCfg{})
	if cap(a.GetSyncOutChan()) != a.SyncOutChanSize || cap(a.GetAsyncOutChan()) != a.AsyncOutChanSize {
		t.Fatal("acceptor defaults not applied before allocation")
	}
}
func TestBehaviorDispatcherRecoversAfterSpawnFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg, 3)
	p := &behaviorPipeline{calls: map[string]int{}, failFirst: true}
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: p, NFork: 1, OutChanSize: 4})
	d.Run(ctx)
	in <- &library.FluentMsg{Tag: "bad"}
	want := &library.FluentMsg{Tag: "good"}
	in <- want
	if got := behaviorReceive(t, d.GetOutChan()); got != want {
		t.Fatal("spawn failure damaged later routing")
	}
}
func TestBehaviorDispatcherReusesOnePipelinePerTag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg, 40)
	p := &behaviorPipeline{calls: map[string]int{}}
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: p, NFork: 4, OutChanSize: 40})
	d.Run(ctx)
	for i := 0; i < 40; i++ {
		in <- &library.FluentMsg{Tag: fmt.Sprint(i % 2), ID: int64(i)}
	}
	seen := map[int64]bool{}
	for i := 0; i < 40; i++ {
		m := behaviorReceive(t, d.GetOutChan())
		if seen[m.ID] {
			t.Fatal("duplicate dispatch")
		}
		seen[m.ID] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls["0"] != 1 || p.calls["1"] != 1 {
		t.Fatal(p.calls)
	}
}

type behaviorSender struct {
	senders.BaseSender
	name   string
	in     chan *library.FluentMsg
	spawns atomic.Int32
}

func (s *behaviorSender) GetName() string { return s.name }
func (s *behaviorSender) Spawn(context.Context) chan<- *library.FluentMsg {
	s.spawns.Add(1)
	return s.in
}
func behaviorProducer(t *testing.T, ss ...senders.SenderItf) (*Producer, chan *library.FluentMsg, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	commits := make(chan *library.FluentMsg, 32)
	p, err := NewProducer(&ProducerCfg{InChan: make(chan *library.FluentMsg, 32), CommitChan: commits, MsgPool: &sync.Pool{}, NFork: 1, DiscardChanSize: 32, DistributeKey: "instance"}, ss...)
	if err != nil {
		t.Fatal(err)
	}
	return p, commits, ctx, cancel
}
func TestBehaviorProducerFanoutWaitsForEverySenderAndSeparatesReplayCopies(t *testing.T) {
	a, b := &behaviorSender{name: "a", in: make(chan *library.FluentMsg, 8)}, &behaviorSender{name: "b", in: make(chan *library.FluentMsg, 8)}
	a.SetSupportedTags([]string{"logs"})
	b.SetSupportedTags([]string{"logs"})
	p, commits, ctx, cancel := behaviorProducer(t, a, b)
	defer cancel()
	p.Run(ctx)
	first := &library.FluentMsg{Tag: "logs", ID: 42, Message: map[string]interface{}{}}
	replay := &library.FluentMsg{Tag: "logs", ID: 42, Message: map[string]interface{}{}}
	for _, m := range []*library.FluentMsg{first, replay} {
		p.InChan <- m
		if behaviorReceive(t, a.in) != m || behaviorReceive(t, b.in) != m {
			t.Fatal("fanout identity")
		}
	}
	// The same ID in two live instances must never combine their completion counts.
	p.successedChan <- first
	p.successedChan <- replay
	sentinel := &library.FluentMsg{Tag: "barrier", ID: 99}
	p.tag2NSender.Store("barrier", 1)
	p.successedChan <- sentinel
	if behaviorReceive(t, commits) != sentinel {
		t.Fatal("premature commit before second sender")
	}
	p.successedChan <- replay
	p.successedChan <- first
	if behaviorReceive(t, commits) != replay || behaviorReceive(t, commits) != first {
		t.Fatal("completion order/identity")
	}
	if first.Message["msgid"] != "instance-42" || a.spawns.Load() != 1 || b.spawns.Load() != 1 {
		t.Fatal("msgid or sender caching")
	}
}
func TestBehaviorProducerFailedDeliveryNeverCommits(t *testing.T) {
	p, commits, ctx, cancel := behaviorProducer(t)
	defer cancel()
	p.tag2NSender.Store("logs", 2)
	done := make(chan struct{})
	go func() { defer close(done); p.runMsgCollector(ctx, p.tag2NSender, p.successedChan) }()
	m := &library.FluentMsg{Tag: "logs", ID: 1}
	p.failedChan <- m
	// Wait until the failure has actually entered the pending table before success.
	deadline := time.After(time.Second)
	for {
		if _, ok := p.discardMsgCountMap.Load(m); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("failure not observed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	p.successedChan <- m
	sentinel := &library.FluentMsg{Tag: "barrier"}
	p.tag2NSender.Store("barrier", 1)
	p.successedChan <- sentinel
	if behaviorReceive(t, commits) != sentinel {
		t.Fatal("failed record committed")
	}
	cancel()
	<-done
}
func TestBehaviorProducerCollectorCancellationWhileCommitBlocked(t *testing.T) {
	p, _, ctx, cancel := behaviorProducer(t)
	blocked := make(chan *library.FluentMsg)
	p.CommitChan = blocked
	p.tag2NSender.Store("logs", 1)
	done := make(chan struct{})
	go func() { defer close(done); p.runMsgCollector(ctx, p.tag2NSender, p.successedChan) }()
	p.successedChan <- &library.FluentMsg{Tag: "logs"}
	// A barrier that shares the collector's input ensures the first record is handled.
	deadline := time.After(time.Second)
	for len(p.successedChan) > 0 {
		select {
		case <-deadline:
			t.Fatal("collector did not read")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("collector ignores cancellation while commit blocked")
		go func() { <-blocked }()
		<-done
	}
}
func TestBehaviorProducerFullQueuePolicies(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		t.Run(fmt.Sprint(lossy), func(t *testing.T) {
			s := &behaviorSender{name: "blocked", in: make(chan *library.FluentMsg)}
			s.SetSupportedTags([]string{"logs"})
			s.IsDiscardWhenBlocked = lossy
			p, commits, ctx, cancel := behaviorProducer(t, s)
			defer cancel()
			p.Run(ctx)
			m := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{}}
			p.InChan <- m
			// Unsupported tags explicitly follow the existing discard/commit policy and form a barrier.
			barrier := &library.FluentMsg{Tag: "unsupported", ID: 2, Message: map[string]interface{}{}}
			p.InChan <- barrier
			got := behaviorReceive(t, commits)
			if lossy {
				if got != m || behaviorReceive(t, commits) != barrier {
					t.Fatal("lossy queue disposition")
				}
			} else if got != barrier {
				t.Fatal("reliable blocked message committed")
			}
		})
	}
}
func TestBehaviorEndToEndPipelineDispatchFanoutCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := &sync.Pool{}
	commits := make(chan *library.FluentMsg, 8)
	parser := tagfilters.NewParserFact(&tagfilters.ParserFactCfg{Tags: []string{"logs"}, NFork: 1, ParseJSONKey: "args", MustInclude: "payload"})
	tp := tagfilters.NewTagPipeline(ctx, &tagfilters.TagPipelineCfg{InternalChanSize: 8, MsgPool: pool, WaitCommitChan: commits}, parser)
	source := make(chan *library.FluentMsg, 8)
	d := NewDispatcher(&DispatcherCfg{InChan: source, TagPipeline: tp, NFork: 1, OutChanSize: 8})
	s := senders.NewStdoutSender(&senders.StdoutSenderCfg{Name: "sink", Tags: []string{"logs"}, IsCommit: true, NFork: 1, InChanSize: 8})
	p, err := NewProducer(&ProducerCfg{InChan: d.GetOutChan(), MsgPool: pool, CommitChan: commits, NFork: 1, DiscardChanSize: 8, DistributeKey: "e2e"}, s)
	if err != nil {
		t.Fatal(err)
	}
	p.Run(ctx)
	d.Run(ctx)
	m := &library.FluentMsg{Tag: "logs", ID: 17, Message: map[string]interface{}{"args": `{"payload":"verified","nested":{"n":1}}`}}
	source <- m
	got := behaviorReceive(t, commits)
	if got != m || got.Message["payload"] != "verified" || got.Message["nested__n"] != float64(1) || got.Message["msgid"] != "e2e-17" {
		t.Fatalf("pipeline result=%+v", got)
	}
}
