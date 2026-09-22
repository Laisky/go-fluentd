package controller

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"github.com/gin-gonic/gin"
	"gofluentd/internal/monitor"
	"gofluentd/library"
)

func TestBehaviorJournalMaintenanceCancellation(t *testing.T) {
	j := &Journal{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { j.runJournalMaintenance(ctx, time.Hour, func() { called <- struct{}{} }); close(done) }()
	<-called
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("journal maintenance ignores cancellation while sleeping")
	}
}
func TestBehaviorJournalBypassCancellation(t *testing.T) {
	j := &Journal{outChan: make(chan *library.FluentMsg)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg)
	done := make(chan struct{})
	go func() { j.runSkipDump(ctx, in); close(done) }()
	in <- &library.FluentMsg{Tag: "logs"}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("journal bypass blocked after cancellation")
	}
}
func TestBehaviorJournalDumpAndBypassRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}
	j := NewJournal(ctx, &JournalCfg{BufDirPath: t.TempDir(), BufSizeBytes: 8192, JournalOutChanLen: 8, MsgPool: pool, GCIntervalSec: time.Hour})
	dump, bypass := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
	out := j.DumpMsgFlow(ctx, pool, dump, bypass)
	if out != j.GetOutChan() {
		t.Fatal("flow returned the wrong output")
	}
	persisted := &library.FluentMsg{Tag: "logs", ID: 101, Message: map[string]interface{}{"value": "persisted"}}
	transient := &library.FluentMsg{Tag: "skip", ID: 102, Message: map[string]interface{}{"value": "bypass"}}
	dump <- persisted
	bypass <- transient
	seen := map[*library.FluentMsg]bool{}
	for i := 0; i < 2; i++ {
		seen[behaviorReceive(t, out)] = true
	}
	if !seen[persisted] || !seen[transient] {
		t.Fatal("routing lost identity")
	}
	backendI, ok := j.tag2JMap.Load("logs")
	if !ok {
		t.Fatal("durable input not journaled")
	}
	backend := backendI.(*journal.Journal)
	defer backend.Close()
	if _, ok := j.tag2JMap.Load("skip"); ok {
		t.Fatal("bypass created journal")
	}
	if err := backend.Sync(); err != nil {
		t.Fatal(err)
	}
	if max, err := j.LoadMaxID(); err != nil || max != 0 {
		t.Fatalf("uncommitted max=%d err=%v", max, err)
	}
	// Exercise all currently registered component metric callbacks against a
	// real request; tests must not race concurrent channel/metric creation.
	engine := gin.New()
	monitor.BindHTTP(engine)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
	if w.Code != 200 {
		t.Fatalf("metrics status %d", w.Code)
	}
	close(dump)
	close(bypass)
	cancel()
}
