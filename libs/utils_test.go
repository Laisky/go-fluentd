package libs_test

import (
	"encoding/json"
	"fmt"
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
