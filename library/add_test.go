package library

import (
	"reflect"
	"testing"
)

func Test_replaceByKey(t *testing.T) {
	type args struct {
		msg *FluentMsg
		v   string
	}
	msgOrig := &FluentMsg{
		Message: map[string]interface{}{
			"a":     "aaaa",
			"b":     "bbbb",
			"LA":    "AAAA",
			"LB":    "BBBB",
			"int":   123,
			"float": 1.21,
			"in": map[string]interface{}{
				"ia":  "iaaaa",
				"ib":  "ibbbb",
				"iLA": "iAAAA",
				"iLB": "iBBBB",
			},
		},
		ID:  123,
		Tag: "test",
	}
	msgArg := &FluentMsg{
		Message: map[string]interface{}{
			"a":     "aaaa",
			"b":     "bbbb",
			"LA":    "AAAA",
			"LB":    "BBBB",
			"int":   123,
			"float": 1.21,
			"in": map[string]interface{}{
				"ia":  "iaaaa",
				"ib":  "ibbbb",
				"iLA": "iAAAA",
				"iLB": "iBBBB",
			},
		},
		ID:  123,
		Tag: "test",
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{"0", args{msgArg, "a"}, "a"},
		{"1", args{msgArg, "%{a}"}, "aaaa"},
		{"2", args{msgArg, "%{in}"}, "map[iLA:iAAAA iLB:iBBBB ia:iaaaa ib:ibbbb]"},
		{"3", args{msgArg, "%{ii}"}, ""},
		{"4", args{msgArg, "%{in.ia}"}, "iaaaa"},
		{"5", args{msgArg, "%{in.iLB}"}, "iBBBB"},
		{"6", args{msgArg, "%{in.ilB}"}, ""},
		{"7", args{msgArg, "%{@upper:a}"}, "AAAA"},
		{"8", args{msgArg, "%{@lower:LA}"}, "aaaa"},
		{"9", args{msgArg, "%{@upper:in.ia}"}, "IAAAA"},
		{"10", args{msgArg, "%{@lower:in.iLA}"}, "iaaaa"},
		{"11", args{msgArg, "a%{a}"}, "aaaaa"},
		{"12", args{msgArg, "a%{a}a"}, "aaaaaa"},
		{"13", args{msgArg, "v%{a}v"}, "vaaaav"},
		{"14", args{msgArg, "v%%{a}v"}, "v%aaaav"},
		{"15", args{msgArg, "v%{{a}v"}, "v%{{a}v"},
		{"16", args{msgArg, "v%{a}}v"}, "vaaaa}v"},
		{"17", args{msgArg, "%{a}%{a}"}, "aaaaaaaa"},
		{"18", args{msgArg, "%{a}%{b}%{a}"}, "aaaabbbbaaaa"},
		{"19", args{msgArg, "%{int}"}, "123"},
		{"20", args{msgArg, "%{float}"}, "1.21"},
		{"21", args{msgArg, "%{@tag}"}, "test"},
		{"22", args{msgArg, "%{@id}"}, "123"},
	}
	for _, tt := range tests {
		if got := ReplaceStrByMsg(tt.args.msg, tt.args.v); got != tt.want {
			t.Fatalf("[%s] ReplaceStrByMsg() = %v, want %v", tt.name, got, tt.want)
		}
	}

	if !reflect.DeepEqual(msgOrig, msgArg) {
		t.Fatal("should not change msg in the arg")
	}
}

func TestGetValFromMap(t *testing.T) {
	type args struct {
		m   interface{}
		key string
	}
	tests := []struct {
		name string
		args args
		want interface{}
	}{
		{"0", args{map[string]string{"a": "b"}, "a"}, "b"},
		{"1", args{map[string]string{"a": "b"}, "a."}, nil},
		{"2", args{map[string]string{"a": "b"}, "a.b"}, nil},
		{"3", args{map[string]string{"a": "b"}, "a."}, nil},
		{"4", args{map[string]string{"a": "b"}, "b"}, nil},
		{"5", args{map[string]string{"a": "b"}, ""}, nil},
		{"6", args{map[string]string{"a": "b", "b": "a"}, "a"}, "b"},
		{"7", args{map[string]string{"a": "b", "b": "a"}, "b"}, "a"},
		{"8", args{map[string]string{"a": "b", "b": "a"}, "b.a"}, nil},
		{"9", args{map[string]string{"a": "b", "b": "a"}, "b."}, nil},
		{"10", args{map[string]string{"a": "b", "b": "a"}, "c"}, nil},
		{"11", args{map[string]string{"a": "b", "b": "a"}, ""}, nil},
		{"12", args{map[string]interface{}{"a": "b"}, "a"}, "b"},
		{"13", args{map[string]interface{}{"a": "b"}, "b"}, nil},
		{"14", args{map[string]interface{}{"a": "b"}, ""}, nil},
		{"15", args{map[string]interface{}{"a": "b"}, "a.b"}, nil},
		{"16", args{map[string]interface{}{"a": "b", "b": "a"}, "a"}, "b"},
		{"17", args{map[string]interface{}{"a": "b", "b": "a"}, "b"}, "a"},
		{"18", args{map[string]interface{}{"a": "b", "b": map[string]string{"a": "b"}}, "b"}, map[string]string{"a": "b"}},
		{"19", args{map[string]interface{}{"a": "b", "b": map[string]string{"a": "b"}}, "b.a"}, "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetValFromMap(tt.args.m, tt.args.key); !reflect.DeepEqual(got, tt.want) {
				// if got := GetValFromMap(tt.args.m, tt.args.key); !interfaceStringEqual(got, tt.want) {
				t.Fatalf("GetValFromMap() = %v, want %v", got, tt.want)
			}
		})
	}
}
