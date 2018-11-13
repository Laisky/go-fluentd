package libs_test

import (
	"encoding/json"
	"testing"

	"github.com/Laisky/go-concator/libs"
)

func TestFlattenMap(t *testing.T) {
	data := map[string]interface{}{}
	j := []byte(`{"a": "1", "b": {"c": 2, "d": {"e": 3}}, "f": 4}`)
	if err := json.Unmarshal(j, &data); err != nil {
		t.Fatalf("got error: %+v", err)
	}

	libs.FlattenMap(data)
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

}
