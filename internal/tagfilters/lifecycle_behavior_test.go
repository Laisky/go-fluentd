package tagfilters

import (
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"gofluentd/internal/monitor"
	"gofluentd/library"
)

func TestBehaviorConcatorTimeoutAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cfg := &ConcatorCfg{Identifier: "container", MsgKey: "log", Regexp: regexp.MustCompile("^HEAD")}
		cf := NewConcatorFact(&ConcatorFactCfg{NFork: 1, MaxLen: 1000})
		cf.SetMsgPool(&sync.Pool{})
		in, out := make(chan *library.FluentMsg, 2), make(chan *library.FluentMsg, 2)
		done := make(chan struct{})
		go func() { defer close(done); cf.StartNewConcator(ctx, cfg, out, in) }()
		m := behaviorConcatMsg(1, "HEAD idle")
		in <- m
		synctest.Wait()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if len(out) != 0 {
			t.Fatal("flushed before idle timeout")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if len(out) != 1 || <-out != m {
			t.Fatal("idle record not flushed")
		}
		in <- behaviorConcatMsg(2, "HEAD cancelled")
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("cancelled concator did not stop")
		}
	})
}
func TestBehaviorTagPipelineOrderAndBypass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := make(chan *library.FluentMsg, 4)
	out := make(chan *library.FluentMsg, 4)
	first := NewParserFact(&ParserFactCfg{Name: "first", Tags: []string{"logs"}, NFork: 1, AddCfg: library.AddCfg{"logs": {{"step": "one"}}}})
	second := NewParserFact(&ParserFactCfg{Name: "second", Tags: []string{"logs"}, NFork: 1, AddCfg: library.AddCfg{"logs": {{"step": "%{step}-two"}}}})
	p := NewTagPipeline(ctx, &TagPipelineCfg{InternalChanSize: 8, MsgPool: &sync.Pool{}, WaitCommitChan: commit}, first, second)
	in, err := p.Spawn(ctx, "logs", out)
	if err != nil {
		t.Fatal(err)
	}
	m := behaviorConcatMsg(1, "x")
	in <- m
	select {
	case got := <-out:
		if got != m || got.Message["step"] != "one-two" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("pipeline order stalled")
	}
	bypass, err := p.Spawn(ctx, "other", out)
	if err != nil || bypass != out {
		t.Fatal("unsupported tag must bypass")
	}
}
func TestBehaviorParserCancellationWhenOutputBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := NewParserFact(&ParserFactCfg{Tags: []string{"logs"}})
	in := make(chan *library.FluentMsg)
	out := make(chan *library.FluentMsg)
	done := make(chan struct{})
	go func() { defer close(done); p.StartNewParser(ctx, out, in) }()
	in <- &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{}}
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("parser ignores cancellation on blocked output")
		<-out
		<-done
	}
}
func TestBehaviorLoadBalancerClosesOwnedWorkerInputs(t *testing.T) {
	f := &BaseTagFilterFactory{}
	in := make(chan *library.FluentMsg)
	close(in)
	workers := []chan *library.FluentMsg{make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)}
	f.runLB(context.Background(), "key", in, workers)
	for _, c := range workers {
		select {
		case _, ok := <-c:
			if ok {
				t.Fatal("unexpected data")
			}
		default:
			t.Error("worker input left open after upstream close")
		}
	}
}
func TestBehaviorTagPipelineMetricsConcurrentWithSpawn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tags := []string{}
	for i := 0; i < 50; i++ {
		tags = append(tags, fmt.Sprint(i))
	}
	f := NewParserFact(&ParserFactCfg{Name: "p", Tags: tags, NFork: 1})
	p := NewTagPipeline(ctx, &TagPipelineCfg{InternalChanSize: 1}, f)
	srv := gin.New()
	monitor.BindHTTP(srv)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, tag := range tags {
			if _, err := p.Spawn(ctx, tag, make(chan *library.FluentMsg, 1)); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
			if w.Code != 200 {
				t.Errorf("monitor status %d", w.Code)
			}
		}
	}()
	wg.Wait()
}

func TestBehaviorConcatorFactoryConfigurationAndSpawn(t *testing.T) {
	configs := LoadConcatorTagConfigs("prod", map[string]interface{}{"logs": map[string]interface{}{"msg_key": "log", "identifier": "container", "regex": "^START"}})
	cf := NewConcatorFact(&ConcatorFactCfg{NFork: 2, MaxLen: 10000, LBKey: "container", Plugins: configs})
	if !cf.IsTagSupported("logs.prod") || cf.IsTagSupported("logs.test") || cf.GetName() != "concator" {
		t.Fatal("factory configuration")
	}
	cf.SetDefaultIntervalChanSize(8)
	cf.SetMsgPool(&sync.Pool{New: func() interface{} { return &library.FluentMsg{} }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *library.FluentMsg, 8)
	in := cf.Spawn(ctx, "logs.prod", out)
	head := &library.FluentMsg{Tag: "logs.prod", ID: 10, Message: map[string]interface{}{"log": "START a", "container": "x"}}
	tail := &library.FluentMsg{Tag: "logs.prod", ID: 11, Message: map[string]interface{}{"log": " tail", "container": "x"}}
	in <- head
	in <- tail
	close(in)
	select {
	case got := <-out:
		if got != head || string(got.Message["log"].([]byte)) != "START a tail" || len(got.ExtIds) != 1 || got.ExtIds[0] != 11 {
			t.Fatalf("factory lost/merged incorrectly: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("factory did not drain")
	}
}
