package library

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func TestComponentAddAdjacentModifiersAndBytes(t *testing.T) {
	m := &FluentMsg{Tag: "logs", ID: 7, Message: map[string]interface{}{"name": []byte("MiXeD"), "nested": map[string]interface{}{"id": 3}, "nested.id": "literal"}}
	for _, tc := range []struct{ tpl, want string }{{"%{name}", "MiXeD"}, {"%{@upper:name}/%{@lower:name}/%{name}", "MIXED/mixed/MiXeD"}, {"%{nested.id}", "literal"}, {"%{@tag}-%{@id}", "logs-7"}, {"%{missing}", ""}} {
		t.Run(tc.tpl, func(t *testing.T) {
			if got := ReplaceStrByMsg(m, tc.tpl); got != tc.want {
				t.Fatalf("template=%q got=%q want=%q", tc.tpl, got, tc.want)
			}
		})
	}
}
func TestComponentTemplateMissingValueDoesNotReusePrevious(t *testing.T) {
	for _, tpl := range []string{"${a}/${missing}", "${a}/${unsupported}"} {
		if got := TemplateWithMap(tpl, map[string]interface{}{"a": "first", "unsupported": make(chan int)}); got != "first/" {
			t.Errorf("template=%s got=%q, previous placeholder leaked", tpl, got)
		}
	}
}
func TestComponentTemplateCustomRegexp(t *testing.T) {
	if got := TemplateWithMapAndRegexp(regexp.MustCompile(`\{\{([^}]+)\}\}`), "{{a}}/{{a}}", map[string]interface{}{"a": "ok"}); got != "ok/ok" {
		t.Fatalf("custom match was not replaced: %q", got)
	}
}
func TestComponentAddConfigurationAndNestedValues(t *testing.T) {
	cfg := ParseAddCfg("prod", map[string]interface{}{"app.{env}": []interface{}{map[string]interface{}{"a": "%{deep.value}"}, map[string]interface{}{"b": "%{a}"}, map[string]interface{}{"remove": nil, "n": 3, "byteTpl": []byte("%{@id}")}}})
	m := &FluentMsg{Tag: "app.prod", ID: 9, Message: map[string]interface{}{"deep": map[string]interface{}{"value": "found"}, "remove": true}}
	ProcessAdd(cfg, m)
	if m.Message["a"] != "found" || m.Message["b"] != "found" || m.Message["remove"] != nil || m.Message["n"] != 3 || m.Message["byteTpl"] != "9" {
		t.Fatal(m.Message)
	}
	for _, key := range []string{"missing", "deep.missing", "deep.value.too.deep"} {
		if got := GetValFromMap(m.Message, key); got != nil {
			t.Errorf("missing path %s=%v", key, got)
		}
	}
	if len(ParseAddCfg("prod", nil)) != 0 {
		t.Fatal("nil configuration should be empty")
	}
}
func TestComponentUtilityContracts(t *testing.T) {
	if got := LoadTagsReplaceEnv("prod", []string{"a.{env}", "b"}); !reflect.DeepEqual(got, []string{"a.prod", "b"}) {
		t.Fatal(got)
	}
	if LoadTagReplaceEnv("x", "{env}.{env}") != "x.x" {
		t.Fatal("replace all")
	}
	if !reflect.DeepEqual(LoadTagsAppendEnv("x", []string{"a"}), []string{"a.x"}) {
		t.Fatal("append")
	}
	if LoadTagsMapAppendEnv("x", map[string]interface{}{"a": 1})["a.x"] != 1 {
		t.Fatal("map append")
	}
	for _, n := range []int{-5, 0, 5} {
		if AbsInt(n) != 5 && n != 0 {
			t.Fatal(n)
		}
	}
	data := map[string]interface{}{"nested": map[string]interface{}{"level": map[string]interface{}{"x": 3}}, "array": []int{1, 2}, "nil": nil}
	FlattenMap(data, "__")
	if data["nested__level__x"] != 3 || data["nested"] != nil || !reflect.DeepEqual(data["array"], []int{1, 2}) {
		t.Fatal(data)
	}
	for _, v := range []interface{}{"str", []byte("str"), int(3), int64(4), float64(1.5)} {
		if got := TemplateWithMap("${x}", map[string]interface{}{"x": v}); got == "" {
			t.Fatalf("supported type %T empty", v)
		}
	}
}
func TestComponentTimerBackoffAndReset(t *testing.T) {
	cfg := NewTimerConfig(time.Millisecond, 8*time.Millisecond, time.Millisecond, 10*time.Millisecond, 0, 2)
	timer := NewTimer(cfg)
	now := time.Now()
	timer.Reset(now)
	for i, want := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond, 8 * time.Millisecond, 8 * time.Millisecond} {
		timer.Sleep()
		if cfg.waitTs != want {
			t.Fatalf("step %d wait=%s want=%s", i, cfg.waitTs, want)
		}
	}
	if timer.Tick(now.Add(10 * time.Millisecond)) {
		t.Fatal("strict timeout boundary changed")
	}
	if !timer.Tick(now.Add(11*time.Millisecond)) || cfg.waitTs != time.Millisecond || cfg.nWaits != 0 {
		t.Fatal(fmt.Sprint(cfg))
	}
}
func TestComponentJournalProvenanceIsNotSerialized(t *testing.T) {
	m := &FluentMsg{Tag: "route", ID: 7, JournalTag: "source-private", Message: map[string]interface{}{"message": "hello"}}
	data, err := m.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded FluentMsg
	if _, err = decoded.UnmarshalMsg(data); err != nil {
		t.Fatal(err)
	}
	if decoded.JournalTag != "" || decoded.Tag != "route" || decoded.ID != 7 {
		t.Fatalf("wire provenance leak or payload change: %+v", decoded)
	}
}
