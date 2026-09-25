package streamformat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Reference preserves the previous reader-based implementation. Independent
// value/domain tests in formats_test.go still define the public contract.
func readerJSONReference(body []byte) (interface{}, error) {
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	if err := validEscapes(body); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, err := value(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return v, nil
}

func TestJSONOwnsStringsAndDoesNotMutateInput(t *testing.T) {
	for _, size := range []int{0, 127, 4095, 4096, 8191, 16384, 65536} {
		key := strings.Repeat("k", size) + "世界"
		original := []byte(fmt.Sprintf(`{"%s":["%s","\ud83d\ude00",9223372036854775807,18446744073709551615]}`, key, strings.Repeat("v", size)))
		body := bytes.Clone(original)
		got, err := JSON(body)
		if err != nil {
			t.Fatal(err)
		}
		want, err := readerJSONReference(original)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, original) {
			t.Fatal("decoder mutated caller buffer")
		}
		for i := range body {
			body[i] = 'x'
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("result aliases mutable input at size %d", size)
		}
	}
}

func FuzzJSONBufferParity(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `null`, `{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `"\ud800"`, `"\\ud800"`, `"\ud83d\ude00"`, `18446744073709551615`, `1e-9999`, `{"x":"` + strings.Repeat("x", 8193) + `"}`, `{} {}`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<18 {
			return
		}
		original := bytes.Clone(body)
		want, we := readerJSONReference(body)
		got, ge := JSON(body)
		if (we == nil) != (ge == nil) || we == nil && !reflect.DeepEqual(want, got) {
			t.Fatalf("reader/buffer parity: %v / %v", we, ge)
		}
		if !bytes.Equal(body, original) {
			t.Fatal("input mutated")
		}
	})
}
func BenchmarkJSONLargeRecord(b *testing.B) {
	data := []byte(`{"message":"` + strings.Repeat("a", 16384) + `","n":9223372036854775807}`)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := JSON(data); err != nil {
			b.Fatal(err)
		}
	}
}
