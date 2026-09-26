package controller

import (
	"gofluentd/library"
	"testing"
	"time"
)

func TestJournalWriterReliableBackpressurePreservesLiveDelivery(t *testing.T) {
	j, ctx, _ := testWriter(t)
	j.outChan = make(chan *library.FluentMsg, 1)
	j.outChan <- &library.FluentMsg{ID: 0}
	in := make(chan *library.FluentMsg, 1)
	msg, receipt := writerMessage(1, true)
	in <- msg
	close(in)
	store := &writerStore{}
	done := writerRun(j, ctx, store, in)
	if err := writerReceipt(t, receipt); err != nil {
		t.Fatal(err)
	}
	<-j.outChan // the downstream resumes after a full live queue
	select {
	case received := <-j.outChan:
		if received.ID != 1 || received.Message["payload"] != "payload-1" {
			t.Fatal("backpressured record changed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("durably accepted record abandoned until periodic replay")
	}
	waitWriter(t, done)
	if !store.durable[1] {
		t.Fatal("live delivery escaped durable barrier")
	}
}

func TestJournalWriterReliableBackpressureIsCancelable(t *testing.T) {
	j, ctx, cancel := testWriter(t)
	j.outChan = make(chan *library.FluentMsg)
	in := make(chan *library.FluentMsg, 1)
	msg, receipt := writerMessage(1, true)
	in <- msg
	close(in)
	store := &writerStore{}
	done := writerRun(j, ctx, store, in)
	if err := writerReceipt(t, receipt); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitWriter(t, done)
	if !store.durable[1] {
		t.Fatal("canceled live delivery lost durable ownership")
	}
}

func TestJournalWriterBestEffortBackpressureStillReturns(t *testing.T) {
	j, ctx, _ := testWriter(t)
	j.outChan = make(chan *library.FluentMsg)
	in := make(chan *library.FluentMsg, 1)
	msg, _ := writerMessage(1, false)
	in <- msg
	close(in)
	store := &writerStore{}
	waitWriter(t, writerRun(j, ctx, store, in))
	if store.syncCalls != 0 {
		t.Fatal("best-effort contract changed")
	}
}
