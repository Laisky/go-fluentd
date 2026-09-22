package postfilters

import (
	"context"
	"gofluentd/library"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestBehaviorDefaultFieldNormalization(t *testing.T) {
	f := NewDefaultFilter(&DefaultFilterCfg{MaxLen: 4})
	msg := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"": "remove", "a.b": []byte("abcdef"), "plain": []byte("abcdef"), "text": "abcdef", "n": 42}}
	f.Filter(msg)
	want := map[string]interface{}{"a__b": "abcd", "plain": "abcd", "text": "abcd", "n": 42}
	if !reflect.DeepEqual(msg.Message, want) {
		t.Fatalf("normalized fields=%#v; want %#v", msg.Message, want)
	}
	f.Filter(msg)
	if !reflect.DeepEqual(msg.Message, want) {
		t.Fatal("normalization is not idempotent")
	}
}
func TestBehaviorFieldsSelectionAndTemplates(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{true: "include", false: "exclude"}[include], func(t *testing.T) {
			cfg := &FieldsFilterCfg{Tags: []string{"logs"}, NewFieldTemplates: map[string]string{"joined": "${a}/${missing}"}, ExcludeFields: []string{"drop"}}
			if include {
				cfg.IncludeFields = []string{"a", "joined"}
			}
			f := NewFieldsFilter(cfg)
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"a": "v", "drop": 1, "tag": "origin", "level": "info"}}
			f.Filter(m)
			if m.Message["joined"] != "v/" || m.Message["tag"] != "origin" || m.Message["level"] != "info" {
				t.Fatal(m.Message)
			}
			if _, ok := m.Message["drop"]; ok {
				t.Fatal("drop retained")
			}
			other := &library.FluentMsg{Tag: "other", Message: map[string]interface{}{"drop": 1}}
			f.Filter(other)
			if other.Message["drop"] != 1 {
				t.Fatal("unsupported tag modified")
			}
		})
	}
}
func TestBehaviorIncludeConfigNotMutated(t *testing.T) {
	backing := make([]string, 16)
	copy(backing, []string{"keep", "sentinel", "untouched", "still"})
	want := append([]string(nil), backing...)
	include := backing[:1]
	getIncludeMap(include)
	if !reflect.DeepEqual(backing, want) {
		t.Fatalf("caller config overwritten: %v", backing)
	}
}
func TestBehaviorESRoutingRejectsInvalidOriginWithoutChangingJournalTag(t *testing.T) {
	for _, origin := range []interface{}{nil, 3, "", "unknown", []byte("unknown")} {
		t.Run("invalid", func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("malformed record panicked: %v", p)
				}
			}()
			f := NewESDispatcherFilter(&ESDispatcherFilterCfg{TagKey: "tag", Tags: []string{"journal.logs"}, ReTagMap: map[string]string{"app.prod": "es.prod"}})
			commits := make(chan *library.FluentMsg, 2)
			f.SetWaitCommitChan(commits)
			m := &library.FluentMsg{Tag: "journal.logs", ID: 42, Message: map[string]interface{}{"tag": origin}}
			if f.Filter(m) != nil {
				t.Fatal("invalid origin should be rejected")
			}
			if m.Tag != "journal.logs" {
				t.Errorf("lost journal tag: %q", m.Tag)
			}
			if len(commits) != 1 || <-commits != m {
				t.Fatal("rejected record must be committed exactly once")
			}
		})
	}
	f := NewESDispatcherFilter(&ESDispatcherFilterCfg{TagKey: "tag", Tags: []string{"journal.logs"}, ReTagMap: LoadReTagMap("prod", map[string]interface{}{"app.{env}": "es.{env}"})})
	m := &library.FluentMsg{Tag: "journal.logs", Message: map[string]interface{}{"tag": "app.prod"}}
	if f.Filter(m) != m || m.Tag != "es.prod" || m.Message["tag"] != "app.prod" {
		t.Fatal("valid routing")
	}
}
func TestBehaviorForwardTagRewriteUsesFinalEnvironment(t *testing.T) {
	for _, tc := range []struct {
		origin interface{}
		want   string
	}{{"service.component.prod", "forward.component.prod"}, {"service.prod", "forward.component.prod"}, {nil, "forward.component.sit"}, {12, "forward.component.sit"}, {"nosuffix", "forward.component.sit"}, {"service.", "forward.component.sit"}} {
		t.Run("origin", func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("invalid origin panicked: %v", p)
				}
			}()
			f := NewForwardTagRewriterFilter(&ForwardTagRewriterFilterCfg{Tag: "forward.component.sit", TagKey: "tag"})
			m := &library.FluentMsg{Tag: f.Tag, Message: map[string]interface{}{"tag": tc.origin}}
			if f.Filter(m) != m || m.Tag != tc.want {
				t.Errorf("origin=%v: tag=%q want=%q", tc.origin, m.Tag, tc.want)
			}
		})
	}
}
func TestBehaviorBigDataValidationAndDisposition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ts, vin interface{}
		valid   bool
	}{{"valid", "2020-01-01T00:00:00.000Z", "VIN", true}, {"bad time", "invalid", "VIN", false}, {"missing time", nil, "VIN", false}, {"missing vin", "2020-01-01T00:00:00.000Z", nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("invalid data panicked: %v", p)
				}
			}()
			f := NewCustomBigDataFilter(&CustomBigDataFilterCfg{Tags: []string{"logs"}})
			commits := make(chan *library.FluentMsg, 1)
			f.SetWaitCommitChan(commits)
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"@timestamp": tc.ts, "vin": tc.vin}}
			out := f.Filter(m)
			if tc.valid {
				if out != m || m.Message["rowkey"] != "VIN_1577836800" || len(commits) != 0 {
					t.Fatal(m)
				}
			} else if out != nil || len(commits) != 1 {
				t.Fatal("rejection must have exactly one terminal disposition")
			}
		})
	}
}

type behaviorPostFilter struct {
	BaseFilter
	fn func(*library.FluentMsg) *library.FluentMsg
}

func (f *behaviorPostFilter) Filter(m *library.FluentMsg) *library.FluentMsg { return f.fn(m) }
func TestBehaviorPostPipelineOrderingAndDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg, 4)
	commits := make(chan *library.FluentMsg, 4)
	first := &behaviorPostFilter{fn: func(m *library.FluentMsg) *library.FluentMsg { m.Message["order"] = "first"; return m }}
	second := &behaviorPostFilter{}
	second.fn = func(m *library.FluentMsg) *library.FluentMsg {
		if m.ID == 2 {
			second.DiscardMsg(m)
			return nil
		}
		m.Message["order"] = m.Message["order"].(string) + "/second"
		return m
	}
	p := NewPostPipeline(&PostPipelineCfg{NFork: 1, OutChanSize: 4, ReEnterChanSize: 4, MsgPool: &sync.Pool{}, WaitCommitChan: commits}, first, second)
	out := p.Wrap(ctx, in)
	a := &library.FluentMsg{ID: 1, Message: map[string]interface{}{}}
	b := &library.FluentMsg{ID: 2, Message: map[string]interface{}{}}
	in <- a
	in <- b
	select {
	case m := <-out:
		if m != a || m.Message["order"] != "first/second" {
			t.Fatal(m)
		}
	case <-time.After(time.Second):
		t.Fatal("pipeline stalled")
	}
	select {
	case m := <-commits:
		if m != b {
			t.Fatal(m)
		}
	case <-time.After(time.Second):
		t.Fatal("discard lost")
	}
	close(in)
}
func TestBehaviorPostPipelineDefaults(t *testing.T) {
	p := NewPostPipeline(&PostPipelineCfg{})
	if cap(p.reEnterChan) != p.ReEnterChanSize || p.NFork <= 0 {
		t.Fatalf("defaults don't match channels: capacity=%d config=%d", cap(p.reEnterChan), p.ReEnterChanSize)
	}
}
