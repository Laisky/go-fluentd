package libs_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/Laisky/go-fluentd/libs"
)

func TestFlattenMap(t *testing.T) {
	data := map[string]interface{}{}
	delimiter := "."
	j := []byte(`{"a": "1", "b": {"c": 2, "d": {"e": 3}}, "f": 4}`)
	if err := json.Unmarshal(j, &data); err != nil {
		t.Fatalf("got error: %+v", err)
	}

	// delimiter = "."
	libs.FlattenMap(data, delimiter)
	if data["a"].(string) != "1" {
		t.Fatalf("expect %v, got %v", "1", data["a"])
	}
	if int(data["b.c"].(float64)) != 2 {
		t.Fatalf("expect %v, got %v", 2, data["b.c"])
	}
	if int(data["b.d.e"].(float64)) != 3 {
		t.Fatalf("expect %v, got %v", 3, data["b.d.e"])
	}
	if int(data["f"].(float64)) != 4 {
		t.Fatalf("expect %v, got %v", 4, data["f"])
	}

	// delimiter = "_"
	if err := json.Unmarshal(j, &data); err != nil {
		t.Fatalf("got error: %+v", err)
	}
	delimiter = "_"
	libs.FlattenMap(data, delimiter)
	fmt.Println(data)
	if data["a"].(string) != "1" {
		t.Fatalf("expect %v, got %v", "1", data["a"])
	}
	if int(data["b_c"].(float64)) != 2 {
		t.Fatalf("expect %v, got %v", 2, data["b_c"])
	}
	if int(data["b_d_e"].(float64)) != 3 {
		t.Fatalf("expect %v, got %v", 3, data["b_d_e"])
	}
	if int(data["f"].(float64)) != 4 {
		t.Fatalf("expect %v, got %v", 4, data["f"])
	}

}

func TestRegexNamedSubMatch(t *testing.T) {
	raw := []byte("2018-02-05 10:33:13.408 | geely:nlcc | INFO | http-bio-8081-exec-3 | com.tservice.cc.web.interceptor.MyLoggingOutInterceptor.handleMessage:57 - Outbound Message:{ID:1, Address:http://10.133.200.77:8082/gisnavi/tservice/gisnavi/poi/poicategory, Http-Method:GET, Content-Type:application/json, Headers:{Content-Type=[application/json], Accept=[application/json]}}")
	rawRe := regexp.MustCompile(`(?ms)^(?P<time>.{23}) {0,}\| {0,}(?P<project>[^\|]+) {0,}\| {0,}(?P<level>[^\|]+) {0,}\| {0,}(?P<thread>[^\|]+) {0,}\| {0,}(?P<class>[^\:]+)\:(?P<line>\d+) {0,}- {0,}(?P<message>.+)`)
	result := map[string]interface{}{}

	if err := libs.RegexNamedSubMatch(rawRe, raw, result); err != nil {
		t.Fatalf("got error: %+v", err)
	}

	if string(result["time"].([]byte)) != "2018-02-05 10:33:13.408" ||
		string(result["project"].([]byte)) != "geely:nlcc" ||
		string(result["thread"].([]byte)) != "http-bio-8081-exec-3" ||
		string(result["class"].([]byte)) != "com.tservice.cc.web.interceptor.MyLoggingOutInterceptor.handleMessage" ||
		string(result["line"].([]byte)) != "57" ||
		string(result["level"].([]byte)) != "INFO" {
		t.Fatalf("got: %+v", result)
	}
}

func BenchmarkRegexNamedSubMatch(b *testing.B) {
	raw := []byte("2018-02-05 10:33:13.408 | geely:nlcc | INFO | http-bio-8081-exec-3 | com.tservice.cc.web.interceptor.MyLoggingOutInterceptor.handleMessage:57 - Outbound Message:{ID:1, Address:http://10.133.200.77:8082/gisnavi/tservice/gisnavi/poi/poicategory, Http-Method:GET, Content-Type:application/json, Headers:{Content-Type=[application/json], Accept=[application/json]}}")
	rawRe := regexp.MustCompile(`(?ms)^(?P<time>.{23}) {0,}\| {0,}(?P<project>[^\|]+) {0,}\| {0,}(?P<level>[^\|]+) {0,}\| {0,}(?P<thread>[^\|]+) {0,}\| {0,}(?P<class>[^\:]+)\:(?P<line>\d+) {0,}- {0,}(?P<message>.+)`)
	result := map[string]interface{}{}

	b.Run("RegexNamedSubMatch", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := libs.RegexNamedSubMatch(rawRe, raw, result); err != nil {
				b.Fatalf("got error: %+v", err)
			}
		}
	})

	if string(result["time"].([]byte)) != "2018-02-05 10:33:13.408" ||
		string(result["project"].([]byte)) != "geely:nlcc" ||
		string(result["thread"].([]byte)) != "http-bio-8081-exec-3" ||
		string(result["class"].([]byte)) != "com.tservice.cc.web.interceptor.MyLoggingOutInterceptor.handleMessage" ||
		string(result["line"].([]byte)) != "57" ||
		string(result["level"].([]byte)) != "INFO" {
		b.Fatalf("got: %+v", result)
	}
}

func TestTemplateWithMap(t *testing.T) {
	tpl := `123${k1} + ${k2}:${k-3} 22`
	data := map[string]interface{}{
		"k1":  41,
		"k2":  "abc",
		"k-3": 213.11,
	}
	want := `12341 + abc:213.11 22`
	got := libs.TemplateWithMap(tpl, data)
	if got != want {
		t.Fatalf("want `%v`, got `%v`", want, got)
	}
}
