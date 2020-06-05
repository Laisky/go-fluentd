package libs

import (
	"reflect"
	"testing"
)

func Test_replaceByKey(t *testing.T) {
	type args struct {
		msg *FluentMsg
		v   string
	}
	msg := &FluentMsg{
		Message: map[string]interface{}{
			"a":     "aaaa",
			"b":     "bbbb",
			"int":   123,
			"float": 1.21,
		},
		Id:  123,
		Tag: "test",
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		// TODO: Add test cases.
		{"t0", args{msg, "a"}, "a"},
		{"t1", args{msg, "%{a}"}, "aaaa"},
		{"t2", args{msg, "a%{a}"}, "aaaaa"},
		{"t3", args{msg, "a%{a}a"}, "aaaaaa"},
		{"t4", args{msg, "v%{a}v"}, "vaaaav"},
		{"t5", args{msg, "v%%{a}v"}, "v%aaaav"},
		{"t6", args{msg, "v%{{a}v"}, "v%{{a}v"},
		{"t7", args{msg, "v%{a}}v"}, "vaaaa}v"},
		{"t8", args{msg, "%{a}%{a}"}, "aaaaaaaa"},
		{"t9", args{msg, "%{a}%{b}%{a}"}, "aaaabbbbaaaa"},
		{"t10", args{msg, "%{int}"}, "123"},
		{"t11", args{msg, "%{float}"}, "1.21"},
		{"t11", args{msg, "%{@tag}"}, "test"},
		{"t11", args{msg, "%{@id}"}, "123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReplaceStrByMsg(tt.args.msg, tt.args.v); got != tt.want {
				t.Errorf("ReplaceStrByMsg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadValFromMap(t *testing.T) {
	type args struct {
		m   interface{}
		key string
	}
	tests := []struct {
		name string
		args args
		want interface{}
	}{
		// TODO: Add test cases.
		{"0", args{map[string]string{"a": "b"}, "a"}, "b"},
		{"1", args{map[string]string{"a": "b", "b": "a"}, "a"}, "b"},
		{"2", args{map[string]string{"a": "b", "b": "a"}, "b"}, "a"},
		{"3", args{map[string]interface{}{"a": "b"}, "a"}, "b"},
		{"4", args{map[string]interface{}{"a": "b", "b": "a"}, "a"}, "b"},
		{"5", args{map[string]interface{}{"a": "b", "b": "a"}, "b"}, "a"},
		{"5", args{map[string]interface{}{"a": "b", "b": map[string]string{"a": "b"}}, "b"}, map[string]string{"a": "b"}},
		{"5", args{map[string]interface{}{"a": "b", "b": map[string]string{"a": "b"}}, "b.a"}, "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetValFromMap(tt.args.m, tt.args.key); !reflect.DeepEqual(got, tt.want) {
				// if got := GetValFromMap(tt.args.m, tt.args.key); !interfaceStringEqual(got, tt.want) {
				t.Errorf("GetValFromMap() = %v, want %v", got, tt.want)
			}
		})
	}
}
