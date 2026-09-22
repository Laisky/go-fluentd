package tagfilters

import (
	"context"
	"fmt"
	"github.com/cespare/xxhash"
	"github.com/gin-gonic/gin"
	"gofluentd/internal/monitor"
	"gofluentd/library"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"
)

func componentRunParser(cfg *ParserFactCfg, m *library.FluentMsg) ([]*library.FluentMsg, []*library.FluentMsg) {
	cfg.Tags = []string{"logs"}
	f := NewParserFact(cfg)
	in, out, commit := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	f.SetWaitCommitChan(commit)
	in <- m
	close(in)
	f.StartNewParser(context.Background(), out, in)
	var delivered, discarded []*library.FluentMsg
	for len(out) > 0 {
		delivered = append(delivered, <-out)
	}
	for len(commit) > 0 {
		discarded = append(discarded, <-commit)
	}
	return delivered, discarded
}
func TestComponentParserUnsupportedTagIsUntouchedOnce(t *testing.T) {
	m := &library.FluentMsg{Tag: "other", Message: map[string]interface{}{"log": "hello"}}
	out, drop := componentRunParser(&ParserFactCfg{MsgKey: "log", IsRemoveOrigLog: true}, m)
	if len(out) != 1 || len(drop) != 0 || m.Message["log"] != "hello" {
		t.Fatalf("unsupported tag processed or delivered more than once: out=%d drop=%d message=%v", len(out), len(drop), m.Message)
	}
}
func TestComponentParserAddWithoutTimeConversion(t *testing.T) {
	m := &library.FluentMsg{Tag: "logs", ID: 7, Message: map[string]interface{}{}}
	out, drop := componentRunParser(&ParserFactCfg{AddCfg: library.AddCfg{"logs": {{"id": "%{@id}"}}}}, m)
	if len(out) != 1 || len(drop) != 0 || m.Message["id"] != "7" {
		t.Fatalf("add was skipped without TimeKey: %+v", m.Message)
	}
}
func TestComponentParserTimeStringAndBytesAgree(t *testing.T) {
	for _, value := range []interface{}{"2026-09-22 12:30:00", []byte("2026-09-22 12:30:00")} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"ts": value}}
			out, drop := componentRunParser(&ParserFactCfg{TimeKey: "ts", TimeFormat: "2006-01-02 15:04:05 -0700", AppendTimeZone: "+0800", NewTimeFormat: time.RFC3339}, m)
			if len(out) != 1 || len(drop) != 0 || m.Message["@timestamp"] != "2026-09-22T04:30:00Z" {
				t.Fatalf("time parse differs by input representation: out=%d drop=%d msg=%v", len(out), len(drop), m.Message)
			}
		})
	}
}
func TestComponentParserInvalidJSONDoesNotEraseOrPartiallyMutate(t *testing.T) {
	for _, value := range []string{"null", `{"keep":"corrupted",`, "[]", "123"} {
		t.Run(value, func(t *testing.T) {
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"keep": "original", "args": value}}
			out, drop := componentRunParser(&ParserFactCfg{ParseJSONKey: "args"}, m)
			if len(out) != 1 || len(drop) != 0 || m.Message["keep"] != "original" || m.Message["args"] != value {
				t.Fatalf("bad JSON changed original message: %+v", m.Message)
			}
		})
	}
}
func TestComponentParserBehaviorMatrix(t *testing.T) {
	cases := []struct {
		name string
		cfg  ParserFactCfg
		msg  map[string]interface{}
		drop bool
		want map[string]interface{}
	}{
		{"regex", ParserFactCfg{MsgKey: "log", Regexp: regexp.MustCompile(`^(?P<level>INFO) (?P<body>.*)$`), IsRemoveOrigLog: true}, map[string]interface{}{"log": "INFO hello"}, false, map[string]interface{}{"level": []byte("INFO"), "body": []byte("hello")}},
		{"regex mismatch", ParserFactCfg{MsgKey: "log", Regexp: regexp.MustCompile(`^INFO`)}, map[string]interface{}{"log": "bad"}, true, nil},
		{"missing required", ParserFactCfg{MustInclude: "tenant"}, map[string]interface{}{}, true, nil},
		{"invalid time", ParserFactCfg{TimeKey: "ts", TimeFormat: time.RFC3339}, map[string]interface{}{"ts": 42}, true, nil},
		{"bad time text", ParserFactCfg{TimeKey: "ts", TimeFormat: time.RFC3339}, map[string]interface{}{"ts": "bad"}, true, nil},
		{"JSON flatten", ParserFactCfg{ParseJSONKey: "args"}, map[string]interface{}{"args": `{"a":{"b":2}}`}, false, map[string]interface{}{"a__b": float64(2)}},
		{"missing log", ParserFactCfg{MsgKey: "log"}, map[string]interface{}{"untouched": true}, false, map[string]interface{}{"untouched": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &library.FluentMsg{Tag: "logs", ID: 42, Message: tc.msg}
			out, drop := componentRunParser(&tc.cfg, m)
			if tc.drop {
				if len(drop) != 1 || drop[0] != m || len(out) != 0 {
					t.Fatal("discard ownership is incorrect")
				}
			} else if len(out) != 1 || len(drop) != 0 || !reflect.DeepEqual(m.Message, tc.want) {
				t.Fatalf("out=%d drop=%d got=%#v want=%#v", len(out), len(drop), m.Message, tc.want)
			}
		})
	}
}
func componentTake(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("component timed out")
		return nil
	}
}
func componentConcator(t *testing.T, max int) (*ConcatorFactory, *ConcatorCfg, chan *library.FluentMsg) {
	t.Helper()
	cfg := &ConcatorCfg{MsgKey: "log", Identifier: "source", Regexp: regexp.MustCompile(`^HEAD`)}
	f := NewConcatorFact(&ConcatorFactCfg{NFork: 1, MaxLen: max, Plugins: map[string]*ConcatorCfg{"a": cfg, "b": cfg}})
	commits := make(chan *library.FluentMsg, 16)
	f.SetWaitCommitChan(commits)
	f.SetMsgPool(&sync.Pool{})
	return f, cfg, commits
}
func componentLine(tag, text string, id int64) *library.FluentMsg {
	return &library.FluentMsg{Tag: tag, ID: id, Message: map[string]interface{}{"source": "same-source", "log": text}}
}
func TestComponentConcatorTailIsNotAcknowledgedBeforeHead(t *testing.T) {
	f, cfg, commits := componentConcator(t, 10000)
	in, out := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	head, tail := componentLine("a", "HEAD one\n", 1), componentLine("a", " tail\n", 2)
	head.ExtIds = []int64{9}
	tail.ExtIds = []int64{8}
	in <- head
	in <- tail
	in <- componentLine("a", "HEAD two\n", 3)
	close(in)
	f.StartNewConcator(context.Background(), cfg, out, in)
	m := componentTake(t, out)
	if m != head || string(m.Message["log"].([]byte)) != "HEAD one\n tail\n" {
		t.Fatal("concatenation payload/identity wrong")
	}
	if !reflect.DeepEqual(m.ExtIds, []int64{9, 2, 8}) {
		t.Errorf("original acknowledgement IDs lost: %v", m.ExtIds)
	}
	if len(commits) != 0 {
		t.Fatalf("tail was committed before downstream acknowledgement of the head: %d", len(commits))
	}
}
func TestComponentConcatorDrainsClosedInput(t *testing.T) {
	f, cfg, _ := componentConcator(t, 10000)
	in, out := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 2)
	m := componentLine("a", "HEAD last", 1)
	in <- m
	close(in)
	f.StartNewConcator(context.Background(), cfg, out, in)
	if len(out) != 1 || <-out != m {
		t.Fatal("closed input lost the last pending record")
	}
}
func TestComponentConcatorSeparatesWorkerState(t *testing.T) {
	f, cfg, _ := componentConcator(t, 10000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inA, inB, outA, outB := make(chan *library.FluentMsg), make(chan *library.FluentMsg), make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); f.StartNewConcator(ctx, cfg, outA, inA) }()
	go func() { defer wg.Done(); f.StartNewConcator(ctx, cfg, outB, inB) }()
	defer func() { cancel(); wg.Wait() }()
	barrier := func(tag string) *library.FluentMsg {
		return &library.FluentMsg{Tag: tag, ID: 999, Message: map[string]interface{}{"log": "barrier"}}
	}
	inA <- componentLine("a", "HEAD A", 1)
	inA <- barrier("a")
	if componentTake(t, outA).ID != 999 {
		t.Fatal("first worker failed barrier")
	}
	inB <- componentLine("b", "HEAD B", 2)
	inB <- barrier("b")
	if m := componentTake(t, outB); m.ID != 999 {
		t.Fatalf("worker B emitted another worker's pending record: tag=%s id=%d", m.Tag, m.ID)
	}
}
func TestComponentConcatorMaximumLengthAndBypass(t *testing.T) {
	f, cfg, _ := componentConcator(t, 8)
	in, out := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	in <- componentLine("a", "orphan", 0)
	in <- componentLine("a", "HEAD", 1)
	in <- componentLine("a", " tail", 2)
	close(in)
	f.StartNewConcator(context.Background(), cfg, out, in)
	if m := componentTake(t, out); m.ID != 0 {
		t.Fatal("orphan continuation not bypassed")
	}
	if m := componentTake(t, out); m.ID != 1 || string(m.Message["log"].([]byte)) != "HEAD tail" {
		t.Fatal("length flush failed")
	}
}
func TestComponentLoadBalancerStableRouting(t *testing.T) {
	f := &BaseTagFilterFactory{}
	in := make(chan *library.FluentMsg, 5)
	chans := []chan *library.FluentMsg{make(chan *library.FluentMsg, 5), make(chan *library.FluentMsg, 5), make(chan *library.FluentMsg, 5)}
	values := []interface{}{"tenant", []byte("tenant"), nil, 42}
	for i, v := range values {
		in <- &library.FluentMsg{ID: int64(i), Message: map[string]interface{}{"key": v}}
	}
	close(in)
	f.runLB(context.Background(), "key", in, chans)
	for idx, ch := range chans {
		for len(ch) > 0 {
			m := <-ch
			key := ""
			if m.ID < 2 {
				key = "tenant"
			}
			if int(xxhash.Sum64String(key)%3) != idx {
				t.Fatalf("unstable route for %d", m.ID)
			}
		}
	}
}

type componentFactory struct {
	BaseTagFilterFactory
	name string
	skip bool
}

func (f *componentFactory) GetName() string                { return f.name }
func (f *componentFactory) IsTagSupported(tag string) bool { return !f.skip && tag != "bypass" }
func (f *componentFactory) Spawn(ctx context.Context, tag string, out chan<- *library.FluentMsg) chan<- *library.FluentMsg {
	in := make(chan *library.FluentMsg, 64)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-in:
				if !ok {
					return
				}
				m.Message["order"] = fmt.Sprint(m.Message["order"]) + f.name
				select {
				case out <- m:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return in
}
func TestComponentTagPipelineOrderAndBypass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTagPipeline(ctx, &TagPipelineCfg{InternalChanSize: 64}, &componentFactory{name: "a"}, &componentFactory{name: "x", skip: true}, &componentFactory{name: "b"})
	out := make(chan *library.FluentMsg, 4)
	in, err := p.Spawn(ctx, "logs", out)
	if err != nil {
		t.Fatal(err)
	}
	in <- &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"order": ""}}
	if m := componentTake(t, out); m.Message["order"] != "ab" {
		t.Fatalf("filter order=%v", m.Message)
	}
	bypass, err := p.Spawn(ctx, "bypass", out)
	if err != nil || bypass != out {
		t.Fatal("no-filter path must return downstream channel")
	}
}
func TestComponentTagPipelineConcurrentMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTagPipeline(ctx, &TagPipelineCfg{InternalChanSize: 64}, &componentFactory{name: "a"})
	engine := gin.New()
	monitor.BindHTTP(engine)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				p.Spawn(ctx, fmt.Sprintf("tag-%d-%d", n, i), make(chan *library.FluentMsg, 1))
				r := httptest.NewRecorder()
				engine.ServeHTTP(r, httptest.NewRequest("GET", "/monitor", nil))
				if r.Code != 200 {
					t.Errorf("monitor status=%d", r.Code)
				}
			}
		}(n)
	}
	wg.Wait()
}
