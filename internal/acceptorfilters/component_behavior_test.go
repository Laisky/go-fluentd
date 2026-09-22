package acceptorfilters

import (
	"context"
	"gofluentd/library"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestBehaviorDefaultAcceptance(t *testing.T) {
	for _, tc := range []struct {
		tag    string
		accept bool
	}{{"logs", true}, {"", false}, {"other", false}} {
		t.Run(tc.tag, func(t *testing.T) {
			f := NewDefaultFilter(&DefaultFilterCfg{Name: "default", RemoveEmptyTag: true, RemoveUnsupportTag: true, AcceptTags: []string{"logs"}, AddCfg: library.AddCfg{"logs": {{"source": "%{@tag}"}}}})
			f.SetMsgPool(&sync.Pool{})
			m := &library.FluentMsg{Tag: tc.tag, Message: map[string]interface{}{}, ExtIds: []int64{99}}
			got := f.Filter(m)
			if tc.accept {
				if got != m || m.Message["source"] != "logs" {
					t.Fatal(m)
				}
			} else if got != nil || m.ExtIds != nil {
				t.Fatal("discard did not clear concatenation IDs")
			}
			if f.GetName() != "default" {
				t.Fatal("name")
			}
		})
	}
}
func TestBehaviorSpringRetagAndReentry(t *testing.T) {
	rules := ParseSpringRules("prod", []interface{}{map[interface{}]interface{}{"new_tag": "parsed.{env}", "regexp": "^INFO"}, map[interface{}]interface{}{"new_tag": "later.{env}", "regexp": ".*"}})
	f := NewSpringFilter(&SpringFilterCfg{Name: "spring", Tag: "raw", Rules: rules})
	up := make(chan *library.FluentMsg, 2)
	f.SetUpstream(up)
	f.SetMsgPool(&sync.Pool{})
	m := &library.FluentMsg{Tag: "raw", Message: map[string]interface{}{"log": "INFO first"}}
	if f.Filter(m) != nil || len(up) != 1 || <-up != m || m.Tag != "parsed.prod" || m.Message["tag"] != m.Tag {
		t.Fatal("first matching rule must transfer ownership once")
	}
	if f.Filter(m) != m || len(up) != 0 {
		t.Fatal("reentry should not loop after retag")
	}
	invalid := &library.FluentMsg{Tag: "raw", Message: map[string]interface{}{"log": 3}}
	if f.Filter(invalid) != nil {
		t.Fatal("invalid log accepted")
	}
}
func TestBehaviorSparkFilter(t *testing.T) {
	f := NewSparkFilter(&SparkFilterCfg{Name: "spark", Tag: "spark", MsgKey: "log", Identifier: "container", IgnoreRegex: regexp.MustCompile("^IGNORE")})
	f.SetMsgPool(&sync.Pool{})
	m := &library.FluentMsg{Tag: "spark", Message: map[string]interface{}{"log": []byte("KEEP")}}
	if f.Filter(m) != m || !reflect.DeepEqual(m.Message["container"], []byte("spark")) {
		t.Fatal(m)
	}
	m = &library.FluentMsg{Tag: "spark", Message: map[string]interface{}{"log": []byte("IGNORE old")}}
	if f.Filter(m) != nil {
		t.Fatal("ignored message accepted")
	}
	m = &library.FluentMsg{Tag: "other", Message: map[string]interface{}{}}
	if f.Filter(m) != m {
		t.Fatal("unsupported tag dropped")
	}
}

type behaviorFilter struct {
	BaseFilter
	fn func(*library.FluentMsg) *library.FluentMsg
}

func (f *behaviorFilter) Filter(m *library.FluentMsg) *library.FluentMsg { return f.fn(m) }
func TestBehaviorAcceptorPipelineOrderSyncAsyncAndReentry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	async, synchronous := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	retag := &behaviorFilter{}
	retag.fn = func(m *library.FluentMsg) *library.FluentMsg {
		if m.Tag == "raw" {
			m.Tag = "ready"
			retag.upstreamChan <- m
			return nil
		}
		return m
	}
	add := &behaviorFilter{fn: func(m *library.FluentMsg) *library.FluentMsg { m.Message["processed"] = m.Tag; return m }}
	p, err := NewAcceptorPipeline(ctx, &AcceptorPipelineCfg{NFork: 1, ReEnterChanSize: 8, OutChanSize: 8, MsgPool: &sync.Pool{}}, retag, add)
	if err != nil {
		t.Fatal(err)
	}
	out, skip := p.Wrap(ctx, async, synchronous)
	async <- &library.FluentMsg{ID: 1, Tag: "raw", Message: map[string]interface{}{}}
	synchronous <- &library.FluentMsg{ID: 2, Tag: "ready", Message: map[string]interface{}{}}
	seen := map[int64]bool{}
	for i := 0; i < 2; i++ {
		select {
		case m := <-out:
			if m.Tag != "ready" || m.Message["processed"] != "ready" || seen[m.ID] {
				t.Fatal(m)
			}
			seen[m.ID] = true
		case <-time.After(time.Second):
			t.Fatal("pipeline stalled")
		}
	}
	if len(skip) != 0 {
		t.Fatal("normal traffic bypassed journal")
	}
	close(async)
	close(synchronous)
}
func TestBehaviorAcceptorPipelineDefaults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := NewAcceptorPipeline(ctx, &AcceptorPipelineCfg{})
	if err != nil {
		t.Fatal(err)
	}
	if cap(p.reEnterChan) != p.ReEnterChanSize {
		t.Fatalf("capacity=%d config=%d", cap(p.reEnterChan), p.ReEnterChanSize)
	}
}
