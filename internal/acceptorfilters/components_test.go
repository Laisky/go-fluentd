package acceptorfilters

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"gofluentd/library"
)

func takeComponent(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("delivery timed out")
		return nil
	}
}
func TestComponentDefaultFilterAdmissionAndAdd(t *testing.T) {
	for _, tag := range []string{"", "bad", "logs"} {
		t.Run(tag, func(t *testing.T) {
			f := NewDefaultFilter(&DefaultFilterCfg{Name: "admission", RemoveEmptyTag: true, RemoveUnsupportTag: true, AcceptTags: []string{"logs"}, AddCfg: library.AddCfg{"logs": {{"id": "%{@id}"}}}})
			f.SetMsgPool(&sync.Pool{})
			m := &library.FluentMsg{Tag: tag, ID: 3, ExtIds: []int64{4}, Message: map[string]interface{}{}}
			got := f.Filter(m)
			if tag == "logs" {
				if got != m || m.Message["id"] != "3" || f.GetName() != "admission" {
					t.Fatalf("valid message changed: %+v", m)
				}
			} else if got != nil || m.ExtIds != nil {
				t.Fatal("discarded message ownership/IDs not reset")
			}
		})
	}
	if err := (&DefaultFilter{DefaultFilterCfg: &DefaultFilterCfg{RemoveUnsupportTag: true}}).valid(); err == nil {
		t.Fatal("unconfigured allowlist must be rejected")
	}
}
func TestComponentSpringReentryAndFirstMatch(t *testing.T) {
	rules := ParseSpringRules("prod", []interface{}{map[interface{}]interface{}{"new_tag": "first.{env}", "regexp": "^INFO"}, map[interface{}]interface{}{"new_tag": "second.{env}", "regexp": ".*"}})
	for _, value := range []interface{}{"INFO hello", []byte("INFO hello"), 42} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			f := NewSpringFilter(&SpringFilterCfg{Name: "spring", Tag: "raw", Rules: rules})
			f.SetMsgPool(&sync.Pool{})
			reentry := make(chan *library.FluentMsg, 1)
			f.SetUpstream(reentry)
			m := &library.FluentMsg{Tag: "raw", ExtIds: []int64{2}, Message: map[string]interface{}{"log": value}}
			if f.Filter(m) != nil {
				t.Fatal("retagged/discarded message must not also flow downstream")
			}
			if _, bad := value.(int); bad {
				if len(reentry) != 0 || m.ExtIds != nil {
					t.Fatal("bad input not discarded")
				}
				return
			}
			if len(reentry) != 1 || <-reentry != m || m.Tag != "first.prod" || m.Message["tag"] != "first.prod" {
				t.Fatalf("retag/reentry wrong: %+v", m)
			}
			if f.Filter(m) != m || f.GetName() != "spring" {
				t.Fatal("retagged message reentered indefinitely")
			}
		})
	}
	f := NewSpringFilter(&SpringFilterCfg{Tag: "raw"})
	m := &library.FluentMsg{Tag: "raw", Message: map[string]interface{}{"log": "no rule"}}
	if f.Filter(m) != m {
		t.Fatal("unmatched log should pass")
	}
}
func TestComponentSparkStringByteParity(t *testing.T) {
	for _, text := range []string{"IGNORE noisy", "INFO log"} {
		for _, value := range []interface{}{text, []byte(text)} {
			t.Run(fmt.Sprintf("%s/%T", text, value), func(t *testing.T) {
				f := NewSparkFilter(&SparkFilterCfg{Name: "spark", Tag: "spark", MsgKey: "log", Identifier: "source", IgnoreRegex: regexp.MustCompile("^IGNORE")})
				f.SetMsgPool(&sync.Pool{})
				m := &library.FluentMsg{Tag: "spark", ExtIds: []int64{1}, Message: map[string]interface{}{"log": value}}
				out := f.Filter(m)
				if text == "IGNORE noisy" {
					if out != nil || m.ExtIds != nil {
						t.Fatal("ignored content must be discarded for both string and bytes")
					}
				} else if out != m || !reflect.DeepEqual(m.Message["source"], []byte("spark")) {
					t.Fatalf("identifier missing for %T", value)
				}
				other := &library.FluentMsg{Tag: "other", Message: map[string]interface{}{"log": value}}
				if f.Filter(other) != other || other.Message["source"] != nil || f.GetName() != "spark" {
					t.Fatal("unsupported tag changed")
				}
			})
		}
	}
}
func TestComponentAcceptorPipelineNormalizedCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := NewAcceptorPipeline(ctx, &AcceptorPipelineCfg{MsgPool: &sync.Pool{}})
	if err != nil {
		t.Fatal(err)
	}
	if cap(p.reEnterChan) != p.ReEnterChanSize {
		t.Fatalf("reentry capacity=%d configuration=%d", cap(p.reEnterChan), p.ReEnterChanSize)
	}
}

type componentFilter struct {
	BaseFilter
	apply func(*library.FluentMsg) *library.FluentMsg
}

func (f *componentFilter) Filter(m *library.FluentMsg) *library.FluentMsg { return f.apply(m) }
func TestComponentAcceptorPipelineOrderAndReentry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &componentFilter{apply: func(m *library.FluentMsg) *library.FluentMsg {
		m.Message["order"] = fmt.Sprint(m.Message["order"]) + "a"
		return m
	}}
	last := &componentFilter{apply: func(m *library.FluentMsg) *library.FluentMsg {
		m.Message["order"] = fmt.Sprint(m.Message["order"]) + "b"
		return m
	}}
	p, err := NewAcceptorPipeline(ctx, &AcceptorPipelineCfg{NFork: 1, OutChanSize: 16, ReEnterChanSize: 4, MsgPool: &sync.Pool{}}, first, last)
	if err != nil {
		t.Fatal(err)
	}
	async, syncIn := make(chan *library.FluentMsg, 2), make(chan *library.FluentMsg, 2)
	out, skip := p.Wrap(ctx, async, syncIn)
	for _, input := range []chan *library.FluentMsg{async, syncIn, p.reEnterChan} {
		m := &library.FluentMsg{Message: map[string]interface{}{"order": ""}}
		input <- m
		if got := takeComponent(t, out); got != m || got.Message["order"] != "ab" {
			t.Fatal("pipeline order/identity violated")
		}
	}
	if len(skip) != 0 {
		t.Fatal("normal delivery bypassed journal")
	}
	close(async)
	close(syncIn)
}
func TestComponentAcceptorPipelineShortCircuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 1)
	never := &componentFilter{apply: func(m *library.FluentMsg) *library.FluentMsg { t.Error("filter after discard was called"); return m }}
	stop := &componentFilter{apply: func(m *library.FluentMsg) *library.FluentMsg { entered <- struct{}{}; return nil }}
	p, err := NewAcceptorPipeline(ctx, &AcceptorPipelineCfg{NFork: 1, OutChanSize: 4, MsgPool: &sync.Pool{}}, stop, never)
	if err != nil {
		t.Fatal(err)
	}
	async, syncIn := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg)
	out, skip := p.Wrap(ctx, async, syncIn)
	async <- &library.FluentMsg{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("filter not invoked")
	}
	if len(out)+len(skip) != 0 {
		t.Fatal("nil filter output propagated")
	}
	close(async)
	close(syncIn)
}
func TestComponentAcceptorPipelineOverloadBypassPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := NewAcceptorPipeline(ctx, &AcceptorPipelineCfg{NFork: 1, OutChanSize: 1, MsgPool: &sync.Pool{}})
	if err != nil {
		t.Fatal(err)
	}
	async, syncIn := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg)
	out, skip := p.Wrap(ctx, async, syncIn)
	a, b := &library.FluentMsg{ID: 1}, &library.FluentMsg{ID: 2}
	async <- a
	async <- b
	if got := takeComponent(t, skip); got != b {
		t.Fatal("overflow did not use documented skip-dump path")
	}
	if takeComponent(t, out) != a {
		t.Fatal("first message lost")
	}
	close(async)
	close(syncIn)
}
