package libs

import "testing"

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replaceByKey(tt.args.msg, tt.args.v); got != tt.want {
				t.Errorf("replaceByKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
