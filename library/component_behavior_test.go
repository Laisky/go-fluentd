package library

import (
	"bytes"
	"github.com/tinylib/msgp/msgp"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBehaviorMessageTemplates(t *testing.T) {
	msg := &FluentMsg{Tag: "app.prod", ID: 42, Message: map[string]interface{}{"name": "AbC", "raw": []byte("AbC"), "count": int64(7), "nested": map[string]interface{}{"value": "deep"}}}
	cases := []struct{ in, want string }{
		{"%{@tag}-%{@id}", "app.prod-42"}, {"%{nested.value}/%{missing}", "deep/"},
		{"%{raw}", "AbC"}, {"%{@lower:raw}-%{@upper:raw}", "abc-ABC"},
		{"%{@lower:name}:%{name}:%{@upper:name}:%{name}", "abc:AbC:ABC:AbC"},
		{"%{count}/%{count}", "7/7"}, {"literal", "literal"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := ReplaceStrByMsg(msg, tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if !reflect.DeepEqual(msg.Message["raw"], []byte("AbC")) {
		t.Fatal("template mutated source bytes")
	}
}
func TestBehaviorTemplateMissingValuesDoNotReusePreviousValue(t *testing.T) {
	data := map[string]interface{}{"a": "first", "b": []byte("second"), "i": int64(42), "f": 1.25}
	for _, tc := range []struct{ in, want string }{
		{"${a}/${missing}/${b}/${nil}", "first//second/"}, {"${i}-${f}", "42-1.25"}, {"${missing}", ""},
	} {
		if got := TemplateWithMap(tc.in, data); got != tc.want {
			t.Errorf("%q = %q; want %q", tc.in, got, tc.want)
		}
	}
	re := regexp.MustCompile(`\[\[([^]]+)\]\]`)
	if got := TemplateWithMapAndRegexp(re, "[[a]]/[[b]]", data); got != "first/second" {
		t.Errorf("custom regexp replacement=%q", got)
	}
}
func TestBehaviorAddOrderDeletionAndEnvironment(t *testing.T) {
	cfg := ParseAddCfg("prod", map[string]interface{}{"app.{env}": []interface{}{map[string]interface{}{"first": "%{@tag}"}, map[string]interface{}{"second": "%{first}:ok"}, map[string]interface{}{"drop": nil, "number": 3, "bytes": []byte("%{@id}")}}})
	msg := &FluentMsg{Tag: "app.prod", ID: 9, Message: map[string]interface{}{"drop": "old", "keep": "yes"}}
	ProcessAdd(cfg, msg)
	want := map[string]interface{}{"first": "app.prod", "second": "app.prod:ok", "number": 3, "bytes": "9", "keep": "yes"}
	if !reflect.DeepEqual(msg.Message, want) {
		t.Fatalf("got %#v want %#v", msg.Message, want)
	}
	msg.Tag = "other"
	ProcessAdd(cfg, msg)
	if !reflect.DeepEqual(msg.Message, want) {
		t.Fatal("unsupported tag modified")
	}
	if len(ParseAddCfg("prod", nil)) != 0 {
		t.Fatal("nil config")
	}
}
func TestBehaviorMapAndRegexUtilities(t *testing.T) {
	m := map[string]interface{}{"a": map[string]interface{}{"b": "v"}, "arr": []interface{}{1, "x"}, "keep": 3}
	if GetValFromMap(m, "a.b") != "v" || GetValFromMap(m, "a.missing") != nil || GetValFromMap(m, "keep.x") != nil || GetValFromMap(nil, "a") != nil {
		t.Fatal("nested lookup")
	}
	FlattenMap(m, "__")
	want := map[string]interface{}{"a__b": "v", "arr": []interface{}{1, "x"}, "keep": 3}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("flatten=%#v", m)
	}
	FlattenMap(m, "__")
	if !reflect.DeepEqual(m, want) {
		t.Fatal("flatten not idempotent")
	}
	r := regexp.MustCompile(`^(?P<level>\w+) (?P<body>.*)$`)
	out := map[string]interface{}{"keep": 1}
	if err := RegexNamedSubMatch(r, []byte("INFO hello  "), out); err != nil {
		t.Fatal(err)
	}
	if string(out["body"].([]byte)) != "hello" || string(out["level"].([]byte)) != "INFO" || out["keep"] != 1 {
		t.Fatal(out)
	}
	if RegexNamedSubMatch(r, []byte("bad"), out) == nil {
		t.Fatal("unmatched regex accepted")
	}
	tags := []string{"a.{env}", "b"}
	if got := LoadTagsReplaceEnv("prod", tags); !reflect.DeepEqual(got, []string{"a.prod", "b"}) || tags[0] != "a.{env}" {
		t.Fatal(got)
	}
	if LoadTagReplaceEnv("x", "{env}.{env}") != "x.x" || LoadTagsAppendEnv("x", []string{"a"})[0] != "a.x" {
		t.Fatal("environment expansion")
	}
	if LoadTagsMapAppendEnv("x", map[string]interface{}{"a": 7})["a.x"] != 7 {
		t.Fatal("map expansion")
	}
}
func TestBehaviorFluentWireRoundTripAndWriteFailure(t *testing.T) {
	msgs := []*FluentMsg{{Tag: "logs", Message: map[string]interface{}{"value": "first"}}, {Tag: "logs", Message: map[string]interface{}{"value": "second"}}}
	for _, batch := range []bool{false, true} {
		var b bytes.Buffer
		e := NewFluentEncoder(&b)
		if batch {
			if err := e.EncodeBatch("logs", msgs); err != nil {
				t.Fatal(err)
			}
		} else {
			for _, m := range msgs {
				if err := e.Encode(m); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := e.Flush(); err != nil {
			t.Fatal(err)
		}
		r := msgp.NewReader(&b)
		seen := []string{}
		for b.Len() > 0 || r.Buffered() > 0 {
			var frame FluentBatchMsg
			if err := frame.DecodeMsg(r); err != nil {
				t.Fatal(err)
			}
			if frame[0] != "logs" {
				t.Fatal(frame)
			}
			for _, entry := range frame[1].([]interface{}) {
				seen = append(seen, entry.([]interface{})[1].(map[string]interface{})["value"].(string))
			}
		}
		if !reflect.DeepEqual(seen, []string{"first", "second"}) {
			t.Fatal(seen)
		}
	}
	e := NewFluentEncoder(componentFailWriter{})
	if err := e.Encode(msgs[0]); err != nil {
		t.Fatal(err)
	}
	if e.Flush() == nil {
		t.Fatal("lost flush error")
	}
}

type componentFailWriter struct{}

func (componentFailWriter) Write([]byte) (int, error) { return 0, bytes.ErrTooLarge }
func TestBehaviorTimerBoundaryAndReset(t *testing.T) {
	cfg := NewTimerConfig(time.Millisecond, 8*time.Millisecond, time.Millisecond, 5*time.Second, 0, 2)
	tm := NewTimer(cfg)
	now := time.Now()
	tm.Reset(now)
	if tm.Tick(now.Add(5 * time.Second)) {
		t.Fatal("strict timeout boundary changed")
	}
	if !tm.Tick(now.Add(5*time.Second + time.Nanosecond)) {
		t.Fatal("timeout did not fire")
	}
	tm.Reset(now)
	if cfg.nWaits != 0 || cfg.waitTs != time.Millisecond {
		t.Fatal("reset")
	}
}
func FuzzBehaviorFluentMessageRoundTrip(f *testing.F) {
	f.Add("app", "payload", int64(42))
	f.Add("", "", int64(0))
	f.Add("中文", "line\nline", int64(-1))
	f.Fuzz(func(t *testing.T, tag, payload string, id int64) {
		if len(tag)+len(payload) > 65536 {
			return
		}
		m := &FluentMsg{Tag: tag, ID: id, ExtIds: []int64{3, 8}, Message: map[string]interface{}{"message": payload}}
		b, err := m.MarshalMsg(nil)
		if err != nil {
			t.Fatal(err)
		}
		var decoded FluentMsg
		rest, err := decoded.UnmarshalMsg(b)
		if err != nil || len(rest) != 0 || !reflect.DeepEqual(*m, decoded) {
			t.Fatalf("round trip failed: %v", err)
		}
	})
}
func FuzzBehaviorTemplateLiteralSafety(f *testing.F) {
	f.Add("plain", "value")
	f.Fuzz(func(t *testing.T, literal, value string) {
		if strings.Contains(literal, "%{") {
			return
		}
		m := &FluentMsg{Message: map[string]interface{}{"a": value}}
		if got := ReplaceStrByMsg(m, literal); got != literal {
			t.Fatal("literal changed")
		}
	})
}
