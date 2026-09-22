package tagfilters

import (
	"context"
	"gofluentd/library"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"
)

func behaviorParse(t *testing.T, cfg *ParserFactCfg, m *library.FluentMsg) ([]*library.FluentMsg, []*library.FluentMsg) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewParserFact(cfg)
	in, out, commit := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	p.SetWaitCommitChan(commit)
	in <- m
	close(in)
	done := make(chan struct{})
	go func() { defer close(done); p.StartNewParser(ctx, out, in) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parser stuck")
	}
	outputs, commits := []*library.FluentMsg{}, []*library.FluentMsg{}
	for len(out) > 0 {
		outputs = append(outputs, <-out)
	}
	for len(commit) > 0 {
		commits = append(commits, <-commit)
	}
	return outputs, commits
}
func TestBehaviorParserUnsupportedTagIsForwardedOnceUnchanged(t *testing.T) {
	m := &library.FluentMsg{Tag: "other", Message: map[string]interface{}{"log": "keep"}}
	out, commits := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, MsgKey: "log", IsRemoveOrigLog: true}, m)
	if len(out) != 1 || out[0] != m || len(commits) != 0 || m.Message["log"] != "keep" {
		t.Fatalf("unsupported tag changed or duplicated: outputs=%d commits=%d message=%v", len(out), len(commits), m.Message)
	}
}
func TestBehaviorParserRegexAndRequiredField(t *testing.T) {
	for _, good := range []bool{true, false} {
		m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"log": "INFO hello"}}
		if !good {
			m.Message["log"] = "invalid"
		}
		out, commits := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, MsgKey: "log", Regexp: regexp.MustCompile(`^(?P<level>INFO) (?P<body>.*)$`), MustInclude: "level", IsRemoveOrigLog: true}, m)
		if good {
			if len(out) != 1 || len(commits) != 0 || string(m.Message["body"].([]byte)) != "hello" {
				t.Fatal("regex parse")
			}
			if _, ok := m.Message["log"]; ok {
				t.Fatal("raw log not removed")
			}
		} else if len(out) != 0 || len(commits) != 1 || commits[0] != m {
			t.Fatal("bad log disposition")
		}
	}
	m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{}}
	out, commits := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, MustInclude: "required"}, m)
	if len(out) != 0 || len(commits) != 1 {
		t.Fatal("required key")
	}
}
func TestBehaviorParserAddWithoutTimeParsing(t *testing.T) {
	m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{}}
	out, _ := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, AddCfg: library.AddCfg{"logs": {{"added": "yes"}}}}, m)
	if len(out) != 1 || m.Message["added"] != "yes" {
		t.Fatal("add configuration ignored without time_key")
	}
}
func TestBehaviorParserJSONIsAtomic(t *testing.T) {
	for _, raw := range []string{`{"app":"changed",`, `null`, `[1,2]`} {
		t.Run(raw, func(t *testing.T) {
			m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"app": "original", "args": raw}}
			want := map[string]interface{}{"app": "original", "args": raw}
			out, commits := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, ParseJSONKey: "args"}, m)
			if len(out) != 1 || len(commits) != 0 || !reflect.DeepEqual(m.Message, want) {
				t.Fatalf("invalid JSON partially changed record: %v", m.Message)
			}
		})
	}
	for _, raw := range []interface{}{`{"app":"new","nested":{"v":2}}`, []byte(`{"app":"new","nested":{"v":2}}`)} {
		m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"keep": "yes", "args": raw}}
		out, _ := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, ParseJSONKey: "args"}, m)
		if len(out) != 1 || m.Message["app"] != "new" || m.Message["keep"] != "yes" || m.Message["nested__v"] != float64(2) {
			t.Fatal(m.Message)
		}
		if _, ok := m.Message["args"]; ok {
			t.Fatal("raw JSON retained after successful parse")
		}
	}
}
func TestBehaviorParserTimeStringAndBytesEquivalent(t *testing.T) {
	for _, ts := range []interface{}{"2020-01-01 08:00:00", []byte("2020-01-01 08:00:00")} {
		m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"time": ts}}
		out, commits := behaviorParse(t, &ParserFactCfg{Tags: []string{"logs"}, TimeKey: "time", TimeFormat: "2006-01-02 15:04:05 -0700", AppendTimeZone: "+0800", NewTimeFormat: time.RFC3339}, m)
		if len(out) != 1 || len(commits) != 0 || m.Message["@timestamp"] != "2020-01-01T00:00:00Z" {
			t.Errorf("timestamp %T: outputs=%d commits=%d msg=%v", ts, len(out), len(commits), m.Message)
		}
	}
}
func behaviorConcat(t *testing.T, cf *ConcatorFactory, cfg *ConcatorCfg, msgs ...*library.FluentMsg) ([]*library.FluentMsg, []*library.FluentMsg) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, out, commits := make(chan *library.FluentMsg, len(msgs)), make(chan *library.FluentMsg, len(msgs)+1), make(chan *library.FluentMsg, len(msgs)+1)
	cf.SetWaitCommitChan(commits)
	cf.SetMsgPool(&sync.Pool{})
	for _, m := range msgs {
		in <- m
	}
	close(in)
	done := make(chan struct{})
	go func() { defer close(done); cf.StartNewConcator(ctx, cfg, out, in) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concator did not stop")
	}
	outputs, acks := []*library.FluentMsg{}, []*library.FluentMsg{}
	for len(out) > 0 {
		outputs = append(outputs, <-out)
	}
	for len(commits) > 0 {
		acks = append(acks, <-commits)
	}
	return outputs, acks
}
func behaviorConcatMsg(id int64, text string) *library.FluentMsg {
	return &library.FluentMsg{Tag: "logs", ID: id, Message: map[string]interface{}{"container": "same", "log": text}}
}
func TestBehaviorConcatorPreservesTailIDsUntilHeadAcknowledgement(t *testing.T) {
	cfg := &ConcatorCfg{Identifier: "container", MsgKey: "log", Regexp: regexp.MustCompile("^HEAD")}
	cf := NewConcatorFact(&ConcatorFactCfg{NFork: 1, MaxLen: 1000, Plugins: map[string]*ConcatorCfg{"logs": cfg}})
	head, tail, next := behaviorConcatMsg(1, "HEAD"), behaviorConcatMsg(2, " tail"), behaviorConcatMsg(4, "HEAD next")
	tail.ExtIds = []int64{3}
	out, commits := behaviorConcat(t, cf, cfg, head, tail, next)
	if len(commits) != 0 {
		t.Errorf("tail acknowledged before merged head reached a sender: %d ACKs", len(commits))
	}
	if len(out) != 2 {
		t.Fatalf("close lost pending head: got %d messages want 2", len(out))
	}
	if out[0] != head || string(head.Message["log"].([]byte)) != "HEAD tail" || !reflect.DeepEqual(head.ExtIds, []int64{2, 3}) {
		t.Errorf("merged data/IDs=%v", head)
	}
}
func TestBehaviorConcatorPassThroughAndMaxLength(t *testing.T) {
	cfg := &ConcatorCfg{Identifier: "container", MsgKey: "log", Regexp: regexp.MustCompile("^HEAD")}
	cf := NewConcatorFact(&ConcatorFactCfg{NFork: 1, MaxLen: 5})
	a, b, c := behaviorConcatMsg(1, "orphan"), behaviorConcatMsg(2, "HEAD"), behaviorConcatMsg(3, " tail")
	out, _ := behaviorConcat(t, cf, cfg, a, b, c)
	if len(out) != 2 || out[0] != a || out[1] != b || string(b.Message["log"].([]byte)) != "HEAD tail" {
		t.Fatal(out)
	}
}
func TestBehaviorConcatorWorkersDoNotSharePendingMessages(t *testing.T) {
	cfg := &ConcatorCfg{Identifier: "container", MsgKey: "log", Regexp: regexp.MustCompile("^HEAD")}
	cf := NewConcatorFact(&ConcatorFactCfg{NFork: 1, MaxLen: 1000})
	a := behaviorConcatMsg(1, "HEAD A")
	first, _ := behaviorConcat(t, cf, cfg, a)
	b := behaviorConcatMsg(2, "tail B")
	b.Tag = "other"
	second, _ := behaviorConcat(t, cf, cfg, b)
	if len(first) != 1 || first[0] != a || len(second) != 1 || second[0] != b {
		t.Fatal("pending state leaked between workers/tags")
	}
}
func TestBehaviorLoadBalancerStableAffinity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &BaseTagFilterFactory{}
	in := make(chan *library.FluentMsg, 8)
	workers := []chan *library.FluentMsg{make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)}
	for _, key := range []interface{}{"same", []byte("same"), nil, 23} {
		in <- &library.FluentMsg{Message: map[string]interface{}{"key": key}}
	}
	close(in)
	f.runLB(ctx, "key", in, workers)
	count := 0
	for _, c := range workers {
		keys := []interface{}{}
		for len(c) > 0 {
			keys = append(keys, (<-c).Message["key"])
			count++
		}
		hasString, hasBytes := false, false
		for _, key := range keys {
			if key == nil {
				continue
			}
			switch key.(type) {
			case string:
				hasString = true
			case []byte:
				hasBytes = true
			}
		}
		if hasString != hasBytes {
			t.Fatal("string and byte affinity differ")
		}
	}
	if count != 4 {
		t.Fatalf("lost routed messages: %d", count)
	}
}
