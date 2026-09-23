package postfilters

import (
	"context"
	"fmt"
	"gofluentd/library"
	"reflect"
	"sync"
	"testing"
	"time"
)

func postCall(t *testing.T, f PostFilterItf, m *library.FluentMsg) (out *library.FluentMsg) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("malformed record must not panic: %v", v)
		}
	}()
	return f.Filter(m)
}
func postTake(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("delivery timed out")
		return nil
	}
}
func TestComponentPostDefaultNormalization(t *testing.T) {
	for _, value := range []interface{}{"abcdef", []byte("abcdef"), 42} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			f := NewDefaultFilter(&DefaultFilterCfg{MaxLen: 3})
			m := &library.FluentMsg{Message: map[string]interface{}{"": "remove", "a.b": value, "keep": value}}
			if postCall(t, f, m) != m {
				t.Fatal("message lost")
			}
			if _, ok := m.Message[""]; ok {
				t.Error("empty key resurrected")
			}
			if _, ok := m.Message["a.b"]; ok {
				t.Error("dotted original key resurrected")
			}
			expected := interface{}("abc")
			if _, ok := value.(int); ok {
				expected = 42
			}
			if !reflect.DeepEqual(m.Message["a__b"], expected) || !reflect.DeepEqual(m.Message["keep"], expected) {
				t.Errorf("normalization got=%#v expected=%v", m.Message, expected)
			}
		})
	}
}
func TestComponentFieldsDoesNotMutateCallerSlice(t *testing.T) {
	backing := []string{"keep", "sentinel1", "sentinel2", "sentinel3", "sentinel4", "sentinel5", "sentinel6", "sentinel7", "sentinel8"}
	before := append([]string(nil), backing...)
	getIncludeMap(backing[:1])
	if !reflect.DeepEqual(before, backing) {
		t.Fatal("include map construction mutated caller's backing array")
	}
}
func TestComponentFieldsSelectionAndTemplate(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(fmt.Sprint(include), func(t *testing.T) {
			cfg := &FieldsFilterCfg{Tags: []string{"logs"}, ExcludeFields: []string{"secret"}, NewFieldTemplates: map[string]string{"joined": "${host}/${id}"}}
			if include {
				cfg.IncludeFields = []string{"joined"}
			}
			f := NewFieldsFilter(cfg)
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"host": "node", "id": int64(3), "secret": true, "tag": "logs"}}
			if f.Filter(m) != m || m.Message["joined"] != "node/3" || m.Message["secret"] != nil || m.Message["tag"] != "logs" {
				t.Fatalf("fields selection failed: %v", m.Message)
			}
			if include && len(m.Message) != 2 {
				t.Fatal("include filter leaked unlisted fields")
			}
			other := &library.FluentMsg{Tag: "other", Message: map[string]interface{}{"secret": true}}
			if f.Filter(other) != other || other.Message["secret"] != true {
				t.Fatal("unsupported tag changed")
			}
		})
	}
}
func TestComponentESDispatcherMalformedAndUnknownPreserveJournalTag(t *testing.T) {
	for _, origin := range []interface{}{nil, 42, "", "unknown", []byte("unknown")} {
		t.Run(fmt.Sprintf("%T/%v", origin, origin), func(t *testing.T) {
			f := NewESDispatcherFilter(&ESDispatcherFilterCfg{Tags: []string{"raw"}, TagKey: "origin", ReTagMap: map[string]string{"app": "es"}})
			commit := make(chan *library.FluentMsg, 1)
			f.SetWaitCommitChan(commit)
			m := &library.FluentMsg{Tag: "raw", ID: 1, Message: map[string]interface{}{"origin": origin}}
			if postCall(t, f, m) != nil {
				t.Error("invalid route must be rejected")
			}
			if len(commit) != 1 {
				t.Error("rejected record not finalized")
			}
			if m.Tag != "raw" {
				t.Fatalf("unknown route destroyed journal tag: %q", m.Tag)
			}
		})
	}
}
func TestComponentESDispatcherValidAndBypass(t *testing.T) {
	for _, origin := range []interface{}{"app", []byte("app")} {
		t.Run(fmt.Sprintf("%T", origin), func(t *testing.T) {
			f := NewESDispatcherFilter(&ESDispatcherFilterCfg{Tags: []string{"raw"}, TagKey: "origin", ReTagMap: map[string]string{"app": "es"}})
			m := &library.FluentMsg{Tag: "raw", Message: map[string]interface{}{"origin": origin}}
			if postCall(t, f, m) != m || m.Tag != "es" {
				t.Fatal("valid routing failed")
			}
			m.Tag = "other"
			if postCall(t, f, m) != m || m.Tag != "other" {
				t.Fatal("bypass failed")
			}
		})
	}
	if got := LoadReTagMap("prod", map[string]interface{}{"raw.{env}": "es.{env}"}); got["raw.prod"] != "es.prod" {
		t.Fatal(got)
	}
}
func TestComponentForwardTagLastEnvironment(t *testing.T) {
	for _, origin := range []interface{}{"app.spring.prod", []byte("app.spring.prod")} {
		t.Run(fmt.Sprintf("%T", origin), func(t *testing.T) {
			f := NewForwardTagRewriterFilter(&ForwardTagRewriterFilterCfg{Tag: "forward.app.sit", TagKey: "tag"})
			m := &library.FluentMsg{Tag: "forward.app.sit", Message: map[string]interface{}{"tag": origin}}
			if postCall(t, f, m) != m || m.Tag != "forward.app.prod" {
				t.Fatalf("wrong environment or lost prefix: %q", m.Tag)
			}
		})
	}
}
func TestComponentForwardInvalidDoesNotPanic(t *testing.T) {
	for _, origin := range []interface{}{nil, 1, "nodot", "app.", ".prod"} {
		t.Run(fmt.Sprintf("%v", origin), func(t *testing.T) {
			f := NewForwardTagRewriterFilter(&ForwardTagRewriterFilterCfg{Tag: "forward.sit", TagKey: "tag"})
			commits := make(chan *library.FluentMsg, 1)
			f.SetWaitCommitChan(commits)
			m := &library.FluentMsg{Tag: "forward.sit", Message: map[string]interface{}{"tag": origin}}
			postCall(t, f, m)
			if m.Tag != "forward.sit" {
				t.Fatal("malformed tag changed original journal identity")
			}
		})
	}
}
func TestComponentBigDataValidationAndRowKey(t *testing.T) {
	cases := []struct {
		name    string
		ts, vin interface{}
		valid   bool
	}{{"valid", "2026-09-22T12:00:00.000Z", "VIN", true}, {"missing time", nil, "VIN", false}, {"wrong time", 42, "VIN", false}, {"bad time", "bad", "VIN", false}, {"bad vin", "2026-09-22T12:00:00.000Z", nil, false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewCustomBigDataFilter(&CustomBigDataFilterCfg{Tags: []string{"logs"}})
			commit := make(chan *library.FluentMsg, 1)
			f.SetWaitCommitChan(commit)
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"@timestamp": tc.ts, "vin": tc.vin}}
			out := postCall(t, f, m)
			if tc.valid {
				ts, _ := time.Parse(time.RFC3339, "2026-09-22T12:00:00Z")
				if out != m || m.Message["rowkey"] != fmt.Sprintf("VIN_%d", ts.Unix()) || len(commit) != 0 {
					t.Fatal("valid rowkey failed")
				}
			} else if out != nil || len(commit) != 1 {
				t.Fatal("malformed record was neither delivered nor finalized")
			}
		})
	}
}

type postFilter struct {
	BaseFilter
	fn func(*library.FluentMsg) *library.FluentMsg
}

func (f *postFilter) Filter(m *library.FluentMsg) *library.FluentMsg { return f.fn(m) }
func TestComponentPostPipelineDefaultCapacity(t *testing.T) {
	p := NewPostPipeline(&PostPipelineCfg{})
	if cap(p.reEnterChan) != p.ReEnterChanSize {
		t.Fatalf("reentry buffer=%d config=%d", cap(p.reEnterChan), p.ReEnterChanSize)
	}
}
func TestComponentPostPipelineOrderingAndShortCircuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commit := make(chan *library.FluentMsg, 4)
	f1 := &postFilter{fn: func(m *library.FluentMsg) *library.FluentMsg { m.Message["order"] = "a"; return m }}
	f2 := &postFilter{fn: func(m *library.FluentMsg) *library.FluentMsg {
		if m.ID == 2 {
			commit <- m
			return nil
		}
		m.Message["order"] = fmt.Sprint(m.Message["order"]) + "b"
		return m
	}}
	p := NewPostPipeline(&PostPipelineCfg{NFork: 1, OutChanSize: 8, ReEnterChanSize: 8, MsgPool: &sync.Pool{}, WaitCommitChan: commit}, f1, f2)
	in := make(chan *library.FluentMsg, 4)
	out := p.Wrap(ctx, in)
	for _, ch := range []chan *library.FluentMsg{in, p.reEnterChan} {
		m := &library.FluentMsg{Message: map[string]interface{}{}}
		ch <- m
		if got := postTake(t, out); got != m || got.Message["order"] != "ab" {
			t.Fatal("ordering failed")
		}
	}
	m := &library.FluentMsg{ID: 2, Message: map[string]interface{}{}}
	in <- m
	if postTake(t, commit) != m || len(out) != 0 {
		t.Fatal("short-circuit failed")
	}
	close(in)
}
