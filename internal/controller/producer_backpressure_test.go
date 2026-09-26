package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"gofluentd/library"
)

func TestProducerEventBackpressureDoesNotAbandonLiveDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newComponentSender("blocked-event")
	s.in = make(chan *library.FluentMsg, 1)
	s.in <- &library.FluentMsg{ID: 0}
	in, commit := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
	p, err := NewProducer(&ProducerCfg{NFork: 1, InChan: in, CommitChan: commit, MsgPool: &sync.Pool{}}, s)
	if err != nil {
		t.Fatal(err)
	}
	p.Run(ctx)
	in <- &library.FluentMsg{ID: 1, Tag: "logs", SourceFormat: "ndjson", Message: map[string]interface{}{"value": "unchanged"}}
	componentWait(t, func() bool { return p.counter.Get() == 1 })
	// The downstream's already-full queue remains blocked before it resumes.
	time.Sleep(50 * time.Millisecond)
	<-s.in
	select {
	case got := <-s.in:
		if got.ID != 1 || got.Message["value"] != "unchanged" {
			t.Fatal("live event changed")
		}
		s.good <- got
		if componentRecv(t, commit) != got {
			t.Fatal("wrong event acknowledgement")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("healthy event queue saturation abandoned delivery until WAL replay")
	}
	close(in)
}
